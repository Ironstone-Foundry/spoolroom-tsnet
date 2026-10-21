// Package node is the real control.Node on tailscale.com/tsnet.
package node

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tsnet"
	"tailscale.com/types/logger"

	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/control"
	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/proxy"
)

// Options configure the embedded node.
type Options struct {
	Dir        string
	Hostname   string
	ControlURL string // tests only; empty means Tailscale's default
	UserLogf   logger.Logf
	Logf       logger.Logf // verbose backend logs; nil discards
}

// Node wraps a tsnet.Server.
type Node struct {
	s       *tsnet.Server
	lc      *local.Client
	changes chan string
	cancel  context.CancelFunc
	once    sync.Once
	started bool
}

// New returns an unstarted node. It never sets an auth key, tags, Funnel,
// SSH, the web client or ephemeral mode: the node belongs to whoever signs
// in through the link, and survives restarts.
func New(o Options) *Node {
	return &Node{
		s: &tsnet.Server{
			Dir:        o.Dir,
			Hostname:   o.Hostname,
			ControlURL: o.ControlURL,
			UserLogf:   o.UserLogf,
			Logf:       o.Logf,
			Ephemeral:  false,
			Port:       0, // own UDP port; never clashes with a system tailscaled
		},
		changes: make(chan string, 1),
	}
}

// Start brings the node up without waiting for sign-in (Server.Up would
// block forever in NeedsLogin) and starts watching the IPN bus.
func (n *Node) Start() error {
	if err := n.s.Start(); err != nil {
		return err
	}
	n.started = true
	lc, err := n.s.LocalClient()
	if err != nil {
		return err
	}
	n.lc = lc
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel
	w, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		cancel()
		return fmt.Errorf("watch ipn bus: %w", err)
	}
	go n.watch(ctx, w)
	return nil
}

func (n *Node) watch(ctx context.Context, w *local.IPNBusWatcher) {
	defer w.Close()
	for {
		nt, err := w.Next()
		if err != nil {
			return
		}
		msg := ""
		if nt.ErrMessage != nil {
			msg = *nt.ErrMessage
		}
		if msg == "" && nt.State == nil && nt.BrowseToURL == nil && nt.SelfChange == nil && nt.LoginFinished == nil {
			continue // peer and engine churn
		}
		select {
		case n.changes <- msg:
		case <-ctx.Done():
			return
		default: // a poke is already pending; status is re-read anyway
			if msg != "" {
				select {
				case n.changes <- msg:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

// Changes implements control.Node.
func (n *Node) Changes() <-chan string { return n.changes }

// Status implements control.Node.
func (n *Node) Status(ctx context.Context) (control.NodeStatus, error) {
	st, err := n.lc.StatusWithoutPeers(ctx)
	if err != nil {
		return control.NodeStatus{}, err
	}
	ns := control.NodeStatus{Backend: st.BackendState, AuthURL: st.AuthURL, CertDomains: st.CertDomains}
	if st.CurrentTailnet != nil {
		ns.MagicDNS = st.CurrentTailnet.MagicDNSEnabled
	}
	if st.Self != nil {
		ns.DNSName = strings.TrimSuffix(st.Self.DNSName, ".")
		if st.Self.KeyExpiry != nil {
			ns.KeyExpiry = *st.Self.KeyExpiry
		}
	}
	if st.BackendState == "Running" {
		if tl, err := n.lc.TailnetLockStatus(ctx); err == nil && tl.Enabled && tl.NodeKey != nil && !tl.NodeKeySigned {
			ns.TailnetLocked = true
		}
	}
	return ns, nil
}

// StartLogin implements control.Node.
func (n *Node) StartLogin(ctx context.Context) error { return n.lc.StartLoginInteractive(ctx) }

// SetWantRunning implements control.Node. Off disconnects from the
// tailnet but keeps the node key, so resuming needs no new sign-in.
func (n *Node) SetWantRunning(ctx context.Context, on bool) error {
	_, err := n.lc.EditPrefs(ctx, &ipn.MaskedPrefs{Prefs: ipn.Prefs{WantRunning: on}, WantRunningSet: true})
	return err
}

// Logout implements control.Node.
func (n *Node) Logout(ctx context.Context) error { return n.lc.Logout(ctx) }

// Cert implements control.Node.
func (n *Node) Cert(ctx context.Context, domain string, minValidity time.Duration) error {
	_, _, err := n.lc.CertPairWithValidity(ctx, domain, minValidity)
	return err
}

// RetryAfter implements control.Node.
func (n *Node) RetryAfter(err error) (time.Duration, bool) { return local.RateLimitRetryAfter(err) }

// HTTPSConsentURL asks control for the page that lets the tailnet owner
// turn HTTPS certificates on, the same query `tailscale serve` makes.
func (n *Node) HTTPSConsentURL(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := n.lc.QueryFeature(ctx, "serve")
	if err != nil {
		return "", err
	}
	if r.Complete {
		return "", nil
	}
	return r.URL, nil
}

// ListenTLS listens on tailnet port 443 only. Server.ListenTLS is not used
// because it calls Up, which blocks while signed out.
func (n *Node) ListenTLS(maxConns int) (net.Listener, error) {
	ln, err := n.s.Listen("tcp", ":443")
	if err != nil {
		return nil, err
	}
	return tls.NewListener(proxy.Limit(ln, maxConns), &tls.Config{
		GetCertificate: n.lc.GetCertificate,
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"h2", "http/1.1"},
	}), nil
}

// SelfCheck fetches https://<dnsName><path> through the node's own tailnet
// dialer, proving the certificate, the listener and the proxy together.
func (n *Node) SelfCheck(ctx context.Context, dnsName, path string) (int, error) {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return n.s.Dial(ctx, "tcp", net.JoinHostPort(dnsName, "443"))
		},
		TLSClientConfig: &tls.Config{ServerName: dnsName, MinVersion: tls.VersionTLS12},
	}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+dnsName+path, nil)
	if err != nil {
		return 0, err
	}
	cl := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse // report the gateway's own answer
	}}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// Close implements control.Node.
func (n *Node) Close() error {
	var err error
	n.once.Do(func() {
		if !n.started {
			return // tsnet.Server.Close must not run before Start
		}
		if n.cancel != nil {
			n.cancel()
		}
		err = n.s.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
	})
	return err
}
