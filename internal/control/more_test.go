package control

import (
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/proto"
	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/status"
)

func TestStartPausedDefersStart(t *testing.T) {
	n := newFake()
	n.st.Backend = status.BackendRunning
	h := start(t, n, func(c *Config) { c.StartPaused = true })
	h.waitState(proto.StatePaused)
	time.Sleep(100 * time.Millisecond)
	n.mu.Lock()
	starts, lns := n.starts, len(n.lns)
	n.mu.Unlock()
	if starts != 0 || lns != 0 {
		t.Fatalf("paused start touched the node: starts=%d listeners=%d", starts, lns)
	}
	if ev := h.send(proto.CmdLogin); *ev.OK {
		t.Fatal("login accepted while not started")
	}
	h.send(proto.CmdStatus)
	h.waitState(proto.StatePaused)
	n.set(joined)
	h.send(proto.CmdResume)
	h.waitState(proto.StateRunning)
	if n.starts != 1 {
		t.Fatalf("starts=%d", n.starts)
	}
}

func TestStartPausedThenEOFNeverStarts(t *testing.T) {
	n := newFake()
	h := start(t, n, func(c *Config) { c.StartPaused = true })
	h.waitState(proto.StatePaused)
	close(h.cmds)
	if err := <-h.done; err != nil {
		t.Fatal(err)
	}
	if n.starts != 0 || n.closed {
		t.Fatalf("starts=%d closed=%v", n.starts, n.closed)
	}
}

func TestStartPausedLogoutStillSignsOut(t *testing.T) {
	n := newFake()
	h := start(t, n, func(c *Config) { c.StartPaused = true })
	h.waitState(proto.StatePaused)
	n.set(joined)
	h.cmds <- proto.Command{Cmd: proto.CmdLogout}
	h.waitState(proto.StateSignedOut)
	<-h.wiped
	if n.starts != 1 || n.logouts != 1 {
		t.Fatalf("starts=%d logouts=%d", n.starts, n.logouts)
	}
}

func TestPauseCancelsInFlightStreams(t *testing.T) {
	n := newFake()
	h := start(t, n)
	n.set(joined)
	h.waitState(proto.StateRunning)
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := c.Get("https://" + n.addr() + "/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	go io.Copy(io.Discard, resp.Body)
	h.send(proto.CmdPause)
	select {
	case <-streamEnded:
	case <-time.After(2 * time.Second):
		t.Fatal("stream outlived pause")
	}
}

func TestBadAuthHostReported(t *testing.T) {
	n := newFake()
	h := start(t, n)
	n.set(func(s *NodeStatus) {
		s.Backend, s.AuthURL = status.BackendNeedsLogin, "https://evil.example/a/secret123"
	})
	ev := h.wait("bad_auth_host", func(ev proto.Event) bool { return ev.Type == proto.TypeError && ev.Code == "bad_auth_host" })
	if ev.Detail != "evil.example" || strings.Contains(ev.Detail, "secret123") {
		t.Fatalf("detail %q", ev.Detail)
	}
}

func TestConsentQueryRetried(t *testing.T) {
	n := newFake()
	n.consentFails = 1
	n.st = NodeStatus{Backend: status.BackendRunning, MagicDNS: true, DNSName: dns}
	h := start(t, n, func(c *Config) { c.ConsentRetry = 50 * time.Millisecond })
	ev := h.wait("consent link", func(ev proto.Event) bool { return ev.State == proto.StateHTTPSOff && ev.ConsentURL != "" })
	if ev.Need != "https_certificates" {
		t.Fatalf("%+v", ev)
	}
}

func TestControlUnreachable(t *testing.T) {
	n := newFake()
	n.st.Backend = status.BackendStarting
	h := start(t, n, func(c *Config) { c.StartTimeout = 100 * time.Millisecond })
	h.waitState(proto.StateStarting)
	if ev := h.waitState(proto.StateControlDown); ev.Code != "control_unreachable" {
		t.Fatalf("%+v", ev)
	}
	n.set(func(s *NodeStatus) {
		s.Backend, s.AuthURL = status.BackendNeedsLogin, "https://login.tailscale.com/a/x"
	})
	h.waitState(proto.StateNeedsLogin)
}

func TestTailnetLocked(t *testing.T) {
	n := newFake()
	h := start(t, n)
	n.set(func(s *NodeStatus) { joined(s); s.TailnetLocked = true })
	h.waitState(proto.StateTailnetLocked)
	if n.addr() != "" {
		t.Fatal("serving while locked out")
	}
	n.set(func(s *NodeStatus) { s.TailnetLocked = false })
	h.waitState(proto.StateRunning)
}

func TestTooLongLineAnswered(t *testing.T) {
	pr, pw := io.Pipe()
	out := proto.NewWriter(pw)
	in := `{"cmd":"status","id":"` + strings.Repeat("x", 2*proto.MaxLine) + "\"}\n{\"cmd\":\"pause\"}\n"
	ch := ReadCommands(strings.NewReader(in), out)
	var got []string
	go func() {
		for c := range ch {
			got = append(got, c.Cmd)
		}
		pw.Close()
	}()
	b, _ := io.ReadAll(pr)
	if !strings.Contains(string(b), `"code":"too_long"`) {
		t.Fatalf("got %s", b)
	}
	time.Sleep(10 * time.Millisecond)
	if strings.Join(got, ",") != "pause" {
		t.Fatalf("reader stopped after the long line: %v", got)
	}
}
