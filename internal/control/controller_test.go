package control

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/proto"
	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/status"
)

var testCert = func() tls.Certificate {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"test"}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
}()

const dns = "spoolroom-watcher-ab12.tail1234.ts.net"

// streamEnded receives when a /v1/stream request's context is cancelled.
var streamEnded = make(chan struct{}, 4)

type harness struct {
	t      *testing.T
	node   *fakeNode
	cmds   chan proto.Command
	events chan proto.Event
	done   chan error
	wiped  chan struct{}
}

func start(t *testing.T, node *fakeNode, opts ...func(*Config)) *harness {
	t.Helper()
	pr, pw := io.Pipe()
	h := &harness{t: t, node: node, cmds: make(chan proto.Command), events: make(chan proto.Event, 64),
		done: make(chan error, 1), wiped: make(chan struct{}, 1)}
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			var ev proto.Event
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				t.Errorf("bad line %q", sc.Text())
			}
			h.events <- ev
		}
	}()
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/stream" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			streamEnded <- struct{}{}
			return
		}
		w.WriteHeader(http.StatusUnauthorized) // the gateway's answer without a ticket
	})
	cfg := Config{
		Node: node, Out: proto.NewWriter(pw), Handler: backend,
		Wipe:        func() error { h.wiped <- struct{}{}; return nil },
		ControlHost: "login.tailscale.com", ExpiryWarn: 14 * 24 * time.Hour,
		Poll: 20 * time.Millisecond,
	}
	for _, o := range opts {
		o(&cfg)
	}
	c := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { h.done <- c.Run(ctx, h.cmds); pw.Close() }()
	return h
}

// wait returns the first event matching fn, failing after 3 s.
func (h *harness) wait(what string, fn func(proto.Event) bool) proto.Event {
	h.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if fn(ev) {
				return ev
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (h *harness) waitState(s string) proto.Event {
	h.t.Helper()
	return h.wait("state "+s, func(ev proto.Event) bool { return ev.Type == proto.TypeState && ev.State == s })
}

func (h *harness) send(cmd string) proto.Event {
	h.t.Helper()
	h.cmds <- proto.Command{Cmd: cmd, ID: cmd + "-1"}
	return h.wait("ack "+cmd, func(ev proto.Event) bool { return ev.Type == proto.TypeAck && ev.Re == cmd+"-1" })
}

func joined(s *NodeStatus) {
	s.Backend, s.AuthURL, s.MagicDNS, s.DNSName = status.BackendRunning, "", true, dns
	s.CertDomains = []string{dns}
	s.KeyExpiry = time.Now().Add(180 * 24 * time.Hour)
}

func TestSignInToRunning(t *testing.T) {
	n := newFake()
	h := start(t, n)
	h.waitState(proto.StateStarting)
	n.set(func(s *NodeStatus) {
		s.Backend, s.AuthURL = status.BackendNeedsLogin, "https://login.tailscale.com/a/abc123"
	})
	ev := h.waitState(proto.StateNeedsLogin)
	if ev.LoginURL != "https://login.tailscale.com/a/abc123" {
		t.Fatalf("loginUrl %q", ev.LoginURL)
	}
	n.set(joined)
	ev = h.waitState(proto.StateRunning)
	if ev.URL != "https://"+dns || ev.DNSName != dns || ev.HTTPS == nil || !*ev.HTTPS || ev.KeyExpiry == "" || ev.LoginURL != "" {
		t.Fatalf("bad running event %+v", ev)
	}
	if n.starts != 1 {
		t.Fatalf("node started %d times", n.starts)
	}
}

func TestLoginCommandGivesFreshLink(t *testing.T) {
	n := newFake()
	n.st.Backend = status.BackendNeedsLogin
	h := start(t, n)
	h.waitState(proto.StateStarting)
	h.send(proto.CmdLogin)
	if ev := h.waitState(proto.StateNeedsLogin); !strings.HasSuffix(ev.LoginURL, "/a/fresh") {
		t.Fatalf("got %q", ev.LoginURL)
	}
}

func TestHTTPSOffThenOnWithoutRestart(t *testing.T) {
	n := newFake()
	n.st = NodeStatus{Backend: status.BackendRunning, MagicDNS: true, DNSName: dns}
	h := start(t, n)
	ev := h.waitState(proto.StateHTTPSOff)
	if ev.Need != "https_certificates" || ev.ConsentURL == "" {
		t.Fatalf("bad https_off %+v", ev)
	}
	time.Sleep(100 * time.Millisecond) // several polls
	if n.consent != 1 {
		t.Fatalf("consent queried %d times, want 1", n.consent)
	}
	n.set(func(s *NodeStatus) { s.CertDomains = []string{dns} })
	h.waitState(proto.StateRunning)
	if n.starts != 1 {
		t.Fatal("node was restarted")
	}
}

func TestMagicDNSOffAndApproval(t *testing.T) {
	n := newFake()
	n.st.Backend = status.BackendNeedsMachineAuth
	h := start(t, n)
	h.waitState(proto.StateNeedsApproval)
	n.set(func(s *NodeStatus) { s.Backend = status.BackendRunning })
	if ev := h.waitState(proto.StateMagicDNSOff); ev.Need != "magicdns" {
		t.Fatalf("need %q", ev.Need)
	}
}

func TestCertPendingThenRunning(t *testing.T) {
	n := newFake()
	n.certGate = make(chan struct{})
	n.st.Backend = status.BackendNeedsLogin
	h := start(t, n)
	n.set(joined)
	h.waitState(proto.StateCertPending)
	if n.addr() != "" {
		t.Fatal("listening before the certificate exists")
	}
	close(n.certGate)
	h.waitState(proto.StateRunning)
}

func TestCertRateLimited(t *testing.T) {
	n := newFake()
	n.certErr = rateErr{}
	h := start(t, n)
	n.set(joined)
	h.waitState(proto.StateCertPending)
	h.wait("rate_limited", func(ev proto.Event) bool { return ev.Type == proto.TypeError && ev.Code == "rate_limited" })
	time.Sleep(100 * time.Millisecond)
	if n.certCalls != 1 {
		t.Fatalf("retried during Retry-After: %d calls", n.certCalls)
	}
}

func TestKeyExpiring(t *testing.T) {
	n := newFake()
	h := start(t, n)
	n.set(func(s *NodeStatus) { joined(s); s.KeyExpiry = time.Now().Add(48 * time.Hour) })
	if ev := h.waitState(proto.StateKeyExpiring); ev.URL == "" || ev.KeyExpiry == "" {
		t.Fatalf("bad key_expiring %+v", ev)
	}
}

func get(t *testing.T, addr string) (int, error) {
	t.Helper()
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := c.Get("https://" + addr + "/v1/info")
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// Browsers negotiate h2; a connection cap above TLS once hid *tls.Conn from
// the server, which then read the h2 preface as an HTTP/1 request.
func TestServesHTTP2(t *testing.T) {
	n := newFake()
	h := start(t, n)
	n.set(joined)
	h.waitState(proto.StateRunning)
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}},
	}}
	resp, err := c.Get("https://" + n.addr() + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.StatusCode != 401 {
		t.Fatalf("got %s %d, want HTTP/2 401", resp.Proto, resp.StatusCode)
	}
}

func TestPauseResume(t *testing.T) {
	n := newFake()
	h := start(t, n)
	n.set(joined)
	h.waitState(proto.StateRunning)
	addr := n.addr()
	if code, err := get(t, addr); err != nil || code != 401 {
		t.Fatalf("proxy not serving: %d %v", code, err)
	}
	h.send(proto.CmdPause)
	h.waitState(proto.StatePaused)
	if _, err := get(t, addr); err == nil {
		t.Fatal("still serving while paused")
	}
	if len(n.want) != 1 || n.want[0] {
		t.Fatalf("WantRunning calls %v", n.want)
	}
	h.send(proto.CmdResume)
	h.waitState(proto.StateRunning)
	if code, err := get(t, n.addr()); err != nil || code != 401 {
		t.Fatalf("not serving after resume: %d %v", code, err)
	}
	if n.logouts != 0 {
		t.Fatal("pause signed out")
	}
}

func TestCheck(t *testing.T) {
	n := newFake()
	h := start(t, n)
	h.send(proto.CmdCheck)
	if ev := h.wait("check", func(ev proto.Event) bool { return ev.Type == proto.TypeCheck }); *ev.OK || ev.Code != "not_running" {
		t.Fatalf("check before running: %+v", ev)
	}
	n.set(joined)
	h.waitState(proto.StateRunning)
	h.send(proto.CmdCheck)
	ev := h.wait("check", func(ev proto.Event) bool { return ev.Type == proto.TypeCheck })
	if !*ev.OK || ev.Status != 401 {
		t.Fatalf("check: %+v", ev)
	}
}

func TestLogoutWipesAndExits(t *testing.T) {
	n := newFake()
	h := start(t, n)
	n.set(joined)
	h.waitState(proto.StateRunning)
	h.send(proto.CmdPause) // logout must also work from paused
	h.waitState(proto.StatePaused)
	h.cmds <- proto.Command{Cmd: proto.CmdLogout}
	ev := h.waitState(proto.StateSignedOut)
	if ev.OK == nil || !*ev.OK {
		t.Fatalf("signed_out %+v", ev)
	}
	select {
	case <-h.wiped:
	default:
		t.Fatal("state not wiped")
	}
	if err := <-h.done; !errors.Is(err, ErrSignedOut) {
		t.Fatalf("Run returned %v", err)
	}
	if n.logouts != 1 || !n.closed {
		t.Fatalf("logouts=%d closed=%v", n.logouts, n.closed)
	}
	if n.want[len(n.want)-1] != true {
		t.Fatal("paused node not reconnected before logout")
	}
}

func TestLogoutOfflineStillWipes(t *testing.T) {
	n := newFake()
	n.logoutErr = errors.New("control unreachable")
	h := start(t, n)
	h.cmds <- proto.Command{Cmd: proto.CmdLogout}
	ev := h.waitState(proto.StateSignedOut)
	if ev.OK == nil || *ev.OK || ev.Code != "remote_logout_failed" {
		t.Fatalf("signed_out %+v", ev)
	}
	<-h.wiped
}

func TestEOFExitsWithoutSignOut(t *testing.T) {
	n := newFake()
	h := start(t, n)
	n.set(joined)
	h.waitState(proto.StateRunning)
	close(h.cmds)
	if err := <-h.done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if n.logouts != 0 || !n.closed {
		t.Fatalf("logouts=%d closed=%v", n.logouts, n.closed)
	}
	select {
	case <-h.wiped:
		t.Fatal("EOF wiped state")
	default:
	}
}

func TestStatusReemits(t *testing.T) {
	n := newFake()
	h := start(t, n)
	h.waitState(proto.StateStarting)
	h.send(proto.CmdStatus)
	h.waitState(proto.StateStarting)
}

func TestReadCommandsReportsBadLines(t *testing.T) {
	pr, pw := io.Pipe()
	out := proto.NewWriter(pw)
	ch := ReadCommands(strings.NewReader("{\"cmd\":\"funnel\",\"id\":\"9\"}\n{\"cmd\":\"pause\"}\n"), out)
	go func() {
		for range ch {
		}
		pw.Close()
	}()
	b, _ := io.ReadAll(pr)
	if !strings.Contains(string(b), `"code":"bad_command"`) || !strings.Contains(string(b), `"re":"9"`) {
		t.Fatalf("got %s", b)
	}
}
