package control

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/proxy"
	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/status"
)

// fakeNode is an in-memory Node. Tests mutate st and poke changes.
type fakeNode struct {
	mu           sync.Mutex
	st           NodeStatus
	changes      chan string
	starts       int
	logouts      int
	logoutErr    error
	certErr      error
	retry        time.Duration
	certCalls    int
	consent      int
	want         []bool
	closed       bool
	lns          []net.Listener
	certGate     chan struct{} // if set, Cert waits for it
	consentFails int           // fail this many consent queries first
}

func newFake() *fakeNode {
	return &fakeNode{changes: make(chan string, 16), st: NodeStatus{Backend: status.BackendNoState}}
}

func (f *fakeNode) set(fn func(*NodeStatus)) {
	f.mu.Lock()
	fn(&f.st)
	f.mu.Unlock()
	f.changes <- ""
}

func (f *fakeNode) Start() error { f.mu.Lock(); f.starts++; f.mu.Unlock(); return nil }
func (f *fakeNode) Status(context.Context) (NodeStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st, nil
}
func (f *fakeNode) Changes() <-chan string { return f.changes }
func (f *fakeNode) StartLogin(context.Context) error {
	f.set(func(s *NodeStatus) { s.AuthURL = "https://login.tailscale.com/a/fresh" })
	return nil
}
func (f *fakeNode) SetWantRunning(_ context.Context, on bool) error {
	f.mu.Lock()
	f.want = append(f.want, on)
	if on {
		f.st.Backend = status.BackendRunning
	} else {
		f.st.Backend = status.BackendStopped
	}
	f.mu.Unlock()
	return nil
}
func (f *fakeNode) Logout(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logouts++
	f.st = NodeStatus{Backend: status.BackendNeedsLogin}
	return f.logoutErr
}
func (f *fakeNode) Cert(ctx context.Context, _ string, _ time.Duration) error {
	f.mu.Lock()
	f.certCalls++
	gate, err := f.certGate, f.certErr
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

type rateErr struct{}

func (rateErr) Error() string { return "429 rate limited" }

func (f *fakeNode) RetryAfter(err error) (time.Duration, bool) {
	if errors.As(err, new(rateErr)) {
		return time.Hour, true
	}
	return 0, false
}
func (f *fakeNode) HTTPSConsentURL(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consent++
	if f.consent <= f.consentFails {
		return "", errors.New("control busy")
	}
	return "https://login.tailscale.com/f/serve?node=n1", nil
}

// ListenTLS serves real TLS on loopback with a self-signed test cert,
// layered like the real node: connection cap below TLS, h2 offered.
func (f *fakeNode) ListenTLS(maxConns int) (net.Listener, error) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ln := tls.NewListener(proxy.Limit(raw, maxConns), &tls.Config{
		Certificates: []tls.Certificate{testCert},
		NextProtos:   []string{"h2", "http/1.1"},
	})
	f.mu.Lock()
	f.lns = append(f.lns, ln)
	f.mu.Unlock()
	return ln, nil
}

func (f *fakeNode) addr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.lns) == 0 {
		return ""
	}
	return f.lns[len(f.lns)-1].Addr().String()
}

func (f *fakeNode) SelfCheck(ctx context.Context, _ string, path string) (int, error) {
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+f.addr()+path, nil)
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

func (f *fakeNode) Close() error { f.mu.Lock(); f.closed = true; f.mu.Unlock(); return nil }
