package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestParseTarget(t *testing.T) {
	good := []string{"http://127.0.0.1:8445", "http://127.0.0.1:8445/", "http://[::1]:1"}
	bad := []string{
		"", "127.0.0.1:8445", "https://127.0.0.1:8445", "http://localhost:8445",
		"http://192.168.1.10:8445", "http://10.0.0.1:80", "http://100.64.0.1:80",
		"http://0.0.0.0:8445", "http://example.com:80", "http://127.0.0.1",
		"http://127.0.0.1:0", "http://127.0.0.1:70000", "http://127.0.0.1:08445",
		"http://127.0.0.1:8445/v1", "http://127.0.0.1:8445?x=1", "http://u:p@127.0.0.1:8445",
		"http://127.0.0.2:8445",
	}
	for _, s := range good {
		if _, err := ParseTarget(s); err != nil {
			t.Errorf("ParseTarget(%q) = %v", s, err)
		}
	}
	for _, s := range bad {
		if _, err := ParseTarget(s); err == nil {
			t.Errorf("ParseTarget(%q) accepted", s)
		}
	}
}

func TestTransportRefusesOtherAddresses(t *testing.T) {
	tr := Transport(&url.URL{Scheme: "http", Host: "127.0.0.1:1"})
	for _, a := range []string{"127.0.0.1:2", "example.com:80", "100.100.100.100:80", "[::1]:1"} {
		if _, err := tr.DialContext(context.Background(), "tcp", a); !errors.Is(err, ErrForbiddenTarget) {
			t.Errorf("dial %s: got %v, want ErrForbiddenTarget", a, err)
		}
	}
	if tr.Proxy != nil {
		t.Error("transport must ignore proxy env")
	}
}

// startProxy fronts backend with the proxy and returns the proxy's URL.
func startProxy(t *testing.T, backend *httptest.Server) (*httptest.Server, *url.URL) {
	t.Helper()
	target, err := ParseTarget(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewUnstartedServer(New(target, Options{}))
	front.Config = NewServer(front.Config.Handler, nil, nil)
	front.Start()
	t.Cleanup(front.Close)
	return front, target
}

func TestHeaders(t *testing.T) {
	got := make(chan *http.Request, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Clone(context.Background())
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer backend.Close()
	front, target := startProxy(t, backend)

	req, _ := http.NewRequest("GET", front.URL+"/v1/info?x=1", nil)
	req.Host = "spoolroom-watcher-ab12.tail1234.ts.net"
	for k, v := range map[string]string{
		"Origin": "https://spoolroom.app", "Authorization": "Bearer t", "Range": "bytes=0-",
		"X-Spoolroom-Stream": "1", "Access-Control-Request-Method": "GET",
		"X-Forwarded-For": "6.6.6.6", "Forwarded": "for=6.6.6.6", "X-Real-Ip": "6.6.6.6",
		"Tailscale-User-Login": "mallory@example.com",
	} {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	r := <-got
	if r.Host != target.Host {
		t.Errorf("Host = %q, want %q", r.Host, target.Host)
	}
	if r.URL.RequestURI() != "/v1/info?x=1" {
		t.Errorf("path = %q", r.URL.RequestURI())
	}
	for _, k := range []string{"Origin", "Authorization", "Range", "X-Spoolroom-Stream", "Access-Control-Request-Method"} {
		if r.Header.Get(k) == "" {
			t.Errorf("%s not passed through", k)
		}
	}
	for _, k := range []string{"X-Forwarded-For", "Forwarded", "X-Real-Ip", "Tailscale-User-Login"} {
		if v := r.Header.Get(k); v != "" {
			t.Errorf("%s leaked: %q", k, v)
		}
	}
	if r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("X-Forwarded-Host") != req.Host {
		t.Errorf("forwarded proto/host not set: %v", r.Header)
	}
}

func TestAbsoluteFormStillGoesToTarget(t *testing.T) {
	hits := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- r.Host + " " + r.URL.Path
	}))
	defer backend.Close()
	front, target := startProxy(t, backend)

	c, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "GET http://169.254.169.254/latest/meta-data HTTP/1.1\r\nHost: 169.254.169.254\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case h := <-hits:
		if h != target.Host+" /latest/meta-data" {
			t.Fatalf("backend saw %q", h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not reach the configured target")
	}
}

func TestConnectRefused(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("CONNECT reached backend")
	}))
	defer backend.Close()
	front, _ := startProxy(t, backend)
	c, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("CONNECT status %d", resp.StatusCode)
	}
}

func TestTargetDownIs502WithoutDetail(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens now
	target, _ := ParseTarget("http://" + addr)
	front := httptest.NewServer(New(target, Options{}))
	defer front.Close()
	resp, err := http.Get(front.URL + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway || strings.Contains(string(b), addr) {
		t.Fatalf("got %d %q", resp.StatusCode, b)
	}
}

// mjpegBackend serves n frames and waits for a signal before each next one,
// so a frame can only reach the client if the proxy does not buffer.
func mjpegBackend(n int, next <-chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mw := multipart.NewWriter(w)
		w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+mw.Boundary())
		w.WriteHeader(http.StatusOK)
		for i := 0; i < n; i++ {
			if i > 0 {
				select {
				case <-next:
				case <-r.Context().Done():
					return
				}
			}
			pw, _ := mw.CreatePart(map[string][]string{"Content-Type": {"image/jpeg"}})
			fmt.Fprintf(pw, "\xff\xd8frame-%d\xff\xd9", i)
			w.(http.Flusher).Flush()
		}
		mw.Close()
	})
}

// readFrames reads the raw multipart stream. A multipart.Reader cannot end
// a part until the next boundary arrives, so it would hide what we test:
// each frame's bytes must arrive before the backend produces the next one.
func readFrames(t *testing.T, resp *http.Response, n int, next chan<- struct{}) {
	t.Helper()
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || params["boundary"] == "" {
		t.Fatalf("bad content type %q", resp.Header.Get("Content-Type"))
	}
	chunks := make(chan []byte, 64)
	go func() {
		defer close(chunks)
		buf := make([]byte, 4096)
		for {
			k, err := resp.Body.Read(buf)
			if k > 0 {
				chunks <- append([]byte(nil), buf[:k]...)
			}
			if err != nil {
				return
			}
		}
	}()
	var seen []byte
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("\xff\xd8frame-%d\xff\xd9", i)
		deadline := time.After(2 * time.Second)
		for !strings.Contains(string(seen), want) {
			select {
			case c, ok := <-chunks:
				if !ok {
					t.Fatalf("stream ended before frame %d", i)
				}
				seen = append(seen, c...)
			case <-deadline:
				t.Fatalf("frame %d buffered: not delivered before the next was produced", i)
			}
		}
		if strings.Contains(string(seen), fmt.Sprintf("frame-%d", i+1)) {
			t.Fatalf("frame %d arrived before it was released", i+1)
		}
		if i < n-1 {
			next <- struct{}{}
		}
	}
}

func TestMJPEGStreamsUnbuffered(t *testing.T) {
	next := make(chan struct{})
	backend := httptest.NewServer(mjpegBackend(5, next))
	defer backend.Close()
	front, _ := startProxy(t, backend)
	resp, err := http.Get(front.URL + "/v1/camera/1/mjpeg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	readFrames(t, resp, 5, next)
}

func TestMJPEGStreamsUnbufferedOverHTTP2TLS(t *testing.T) {
	next := make(chan struct{})
	backend := httptest.NewServer(mjpegBackend(3, next))
	defer backend.Close()
	target, _ := ParseTarget(backend.URL)

	front := httptest.NewUnstartedServer(nil)
	front.EnableHTTP2 = true
	front.Config = NewServer(New(target, Options{}), nil, nil)
	front.StartTLS()
	defer front.Close()

	resp, err := front.Client().Get(front.URL + "/v1/camera/1/mjpeg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("want HTTP/2, got %s", resp.Proto)
	}
	readFrames(t, resp, 3, next)
}

func TestSSEStreamsUnbuffered(t *testing.T) {
	next := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 3; i++ {
			if i > 0 {
				<-next
			}
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
		}
	}))
	defer backend.Close()
	front, _ := startProxy(t, backend)
	resp, err := http.Get(front.URL + "/v1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	for i := 0; i < 3; i++ {
		line := make(chan string, 1)
		go func() { s, _ := br.ReadString('\n'); br.ReadString('\n'); line <- s }()
		select {
		case s := <-line:
			if s != fmt.Sprintf("data: %d\n", i) {
				t.Fatalf("event %d = %q", i, s)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("event %d buffered", i)
		}
		if i < 2 {
			next <- struct{}{}
		}
	}
}

func TestPostBodyPassesThrough(t *testing.T) {
	// WebRTC signalling (WHEP) is a POST with an SDP body and an SDP answer.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/sdp")
		w.Header().Set("Location", "/v1/whep/session/1")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("answer:" + string(b)))
	}))
	defer backend.Close()
	front, _ := startProxy(t, backend)
	resp, err := http.Post(front.URL+"/v1/whep", "application/sdp", strings.NewReader("v=0 offer"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated || string(b) != "answer:v=0 offer" || resp.Header.Get("Location") == "" {
		t.Fatalf("got %d %q %v", resp.StatusCode, b, resp.Header)
	}
}
