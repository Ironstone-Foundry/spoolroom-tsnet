package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func frontWith(t *testing.T, backend http.Handler, o Options) (*httptest.Server, string) {
	t.Helper()
	be := httptest.NewServer(backend)
	t.Cleanup(be.Close)
	target, err := ParseTarget(be.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewUnstartedServer(nil)
	front.Config = NewServer(New(target, o), nil, nil)
	front.Start()
	t.Cleanup(front.Close)
	return front, target.Host
}

func TestUpgradeRefused(t *testing.T) {
	front, _ := frontWith(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("upgrade reached the gateway")
	}), Options{})
	req, _ := http.NewRequest("GET", front.URL+"/v1/ws", nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestUnderscoreAndTailscaleHeadersDropped(t *testing.T) {
	got := make(chan http.Header, 1)
	front, _ := frontWith(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Clone() }), Options{})
	c, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "GET /v1/info HTTP/1.1\r\nHost: x\r\nTailscale_User_Login: m@example.com\r\nX_Forwarded_For: 6.6.6.6\r\nTailscale-Funnel-Request: 1\r\nX-Forwarded-Port: 1\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := <-got
	for k := range h {
		if dropHeader(k) && k != "X-Forwarded-Proto" && k != "X-Forwarded-Host" {
			t.Errorf("header %q reached the gateway", k)
		}
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
}

func TestLocationRewritten(t *testing.T) {
	var self string
	front, host := frontWith(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/abs":
			w.Header().Set("Location", "http://"+self+"/v1/next?x=1")
		case "/rel":
			w.Header().Set("Location", "/v1/next")
		case "/other":
			w.Header().Set("Location", "https://spoolroom.app/x")
		}
		w.WriteHeader(http.StatusFound)
	}), Options{})
	self = host
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	cases := map[string]string{
		"/abs":   "https://spoolroom-watcher-ab12.tail1234.ts.net/v1/next?x=1",
		"/rel":   "/v1/next",
		"/other": "https://spoolroom.app/x",
	}
	for path, want := range cases {
		req, _ := http.NewRequest("GET", front.URL+path, nil)
		req.Host = "spoolroom-watcher-ab12.tail1234.ts.net"
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Location"); got != want {
			t.Errorf("%s: Location %q, want %q", path, got, want)
		}
	}
}

func TestSlowRequestBodyTimesOut(t *testing.T) {
	done := make(chan error, 1)
	front, _ := frontWith(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		done <- err
	}), Options{BodyTimeout: 200 * time.Millisecond})
	c, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprint(c, "POST /v1/whep HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\npartial")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("gateway got a complete body")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow body never timed out")
	}
}

func TestStreamOutlivesBodyTimeout(t *testing.T) {
	front, _ := frontWith(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 8; i++ {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}), Options{BodyTimeout: 150 * time.Millisecond})
	for _, method := range []string{"GET", "POST"} {
		var body io.Reader
		if method == "POST" {
			body = strings.NewReader("offer")
		}
		req, _ := http.NewRequest(method, front.URL+"/v1/events", body)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || strings.Count(string(b), "data:") != 8 {
			t.Fatalf("%s: stream cut after body timeout: %q %v", method, b, err)
		}
	}
}

func TestListenerCap(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	srv := NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-release
	}), nil, nil)
	go srv.Serve(Limit(ln, 2))
	defer srv.Close()
	defer close(release)

	open := func() *http.Response {
		c := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := c.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	r1, r2 := open(), open()
	defer r1.Body.Close()
	defer r2.Body.Close()
	third := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+ln.Addr().String()+"/", nil)
		if resp, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req); err == nil {
			resp.Body.Close()
			close(third)
		}
	}()
	select {
	case <-third:
		t.Fatal("third connection served past the cap")
	case <-time.After(500 * time.Millisecond):
	}
}

func TestUpstreamConnCap(t *testing.T) {
	tr := Transport(mustTarget(t, "http://127.0.0.1:1"))
	if tr.MaxConnsPerHost != MaxUpstreamConn || MaxUpstreamConn <= 0 {
		t.Fatalf("MaxConnsPerHost = %d", tr.MaxConnsPerHost)
	}
}

func mustTarget(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := ParseTarget(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
