package control

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/proto"
	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/proxy"
	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/status"
)

// Config wires a Controller.
type Config struct {
	Node         Node
	Out          *proto.Writer
	Handler      http.Handler // the proxy
	Wipe         func() error // removes the state dir after logout
	ControlHost  string       // links are reported only on this host
	ExpiryWarn   time.Duration
	Poll         time.Duration // status poll, also while HTTPS is off
	CertRenew    time.Duration // how often to top up certificate validity
	StartTimeout time.Duration // starting longer than this is control_unreachable
	ConsentRetry time.Duration // retry a failed HTTPS consent query after this
	MaxConns     int           // concurrent tailnet connections
	CheckPath    string
	// StartPaused defers Node.Start until a resume command, so a paused
	// member's device stays offline across helper restarts.
	StartPaused bool
	ErrLog      *log.Logger
	Now         func() time.Time
}

// Controller owns all mutable state; only Run's goroutine touches it.
type Controller struct {
	cfg Config

	started     bool
	paused      bool
	srv         *http.Server
	srvCancel   context.CancelFunc
	certDomain  string
	certReady   bool
	certBusy    bool
	certNext    time.Time
	certBackoff time.Duration
	certRenewAt time.Time
	consent     string
	consentFor  string // dns name the consent link was fetched for
	consentNext time.Time
	waitSince   time.Time // when the node began waiting on control
	badLink     string    // last rejected link, reported once
	certDone    chan certResult
	last        status.Snapshot
}

type certResult struct {
	domain string
	err    error
}

// New returns a Controller with defaults filled in.
func New(cfg Config) *Controller {
	if cfg.Poll == 0 {
		cfg.Poll = 10 * time.Second
	}
	if cfg.CertRenew == 0 {
		cfg.CertRenew = 24 * time.Hour
	}
	if cfg.StartTimeout == 0 {
		cfg.StartTimeout = 45 * time.Second
	}
	if cfg.ConsentRetry == 0 {
		cfg.ConsentRetry = time.Minute
	}
	if cfg.MaxConns == 0 {
		cfg.MaxConns = proxy.MaxConns
	}
	if cfg.CheckPath == "" {
		cfg.CheckPath = "/v1/info"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Controller{cfg: cfg, certDone: make(chan certResult, 1)}
}

// ErrSignedOut is returned by Run after a logout.
var ErrSignedOut = errors.New("signed out")

// Run starts the node (unless StartPaused) and serves until cmds closes
// (stdin EOF), ctx ends, or a logout completes. Closing cmds never signs out.
func (c *Controller) Run(ctx context.Context, cmds <-chan proto.Command) error {
	defer c.shutdown()
	if c.cfg.StartPaused {
		c.paused = true
		c.emit(proto.Event{Type: proto.TypeState, State: proto.StatePaused})
	} else if err := c.start(); err != nil {
		return err
	}
	tick := time.NewTicker(c.cfg.Poll)
	defer tick.Stop()
	c.refresh(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case cmd, ok := <-cmds:
			if !ok {
				return nil
			}
			if err := c.handle(ctx, cmd); err != nil {
				return err
			}
		case msg := <-c.cfg.Node.Changes():
			if msg != "" {
				c.emit(proto.Event{Type: proto.TypeError, Code: "backend", Detail: proto.Scrub(msg, 200)})
			}
		case r := <-c.certDone:
			c.onCert(r)
		case <-tick.C:
		}
		c.refresh(ctx)
	}
}

func (c *Controller) start() error {
	c.emit(proto.Event{Type: proto.TypeState, State: proto.StateStarting})
	if err := c.cfg.Node.Start(); err != nil {
		c.emit(proto.Event{Type: proto.TypeState, State: proto.StateError, Code: "start_failed", Detail: proto.Scrub(err.Error(), 200)})
		return err
	}
	c.started = true
	c.waitSince = c.cfg.Now()
	return nil
}

func (c *Controller) emit(ev proto.Event) { c.cfg.Out.Emit(ev) }

func (c *Controller) ack(cmd proto.Command, err error) {
	ok := err == nil
	ev := proto.Event{Type: proto.TypeAck, Cmd: cmd.Cmd, Re: cmd.ID, OK: &ok}
	if err != nil {
		ev.Detail = proto.Scrub(err.Error(), 200)
	}
	c.emit(ev)
}

var errNotStarted = errors.New("paused: send resume first")

func (c *Controller) handle(ctx context.Context, cmd proto.Command) error {
	switch cmd.Cmd {
	case proto.CmdStatus:
		c.cfg.Out.Forget()
		c.ack(cmd, nil)
	case proto.CmdLogin:
		if !c.started {
			c.ack(cmd, errNotStarted)
			return nil
		}
		c.ack(cmd, c.cfg.Node.StartLogin(ctx))
	case proto.CmdPause:
		c.paused = true
		c.stopServing()
		if !c.started {
			c.ack(cmd, nil)
			return nil
		}
		c.ack(cmd, c.cfg.Node.SetWantRunning(ctx, false))
	case proto.CmdResume:
		c.paused = false
		c.waitSince = c.cfg.Now()
		if !c.started {
			err := c.start()
			c.ack(cmd, err)
			return err
		}
		c.ack(cmd, c.cfg.Node.SetWantRunning(ctx, true))
	case proto.CmdCheck:
		c.ack(cmd, nil)
		go c.selfCheck(ctx, c.last.DNSName, c.srv != nil)
	case proto.CmdLogout:
		return c.logout(ctx, cmd)
	}
	return nil
}

// refresh reads the node, reconciles the listener and certificate, and
// emits the derived state.
func (c *Controller) refresh(ctx context.Context) {
	if !c.started {
		c.emit(proto.Event{Type: proto.TypeState, State: proto.StatePaused})
		return
	}
	ns, err := c.cfg.Node.Status(ctx)
	if err != nil {
		c.emit(proto.Event{Type: proto.TypeError, Code: "status", Detail: proto.Scrub(err.Error(), 200)})
		return
	}
	now := c.cfg.Now()
	s := status.Snapshot{
		Paused: c.paused, Backend: ns.Backend, AuthURL: ns.AuthURL,
		MagicDNS: ns.MagicDNS, DNSName: ns.DNSName, CertDomains: ns.CertDomains,
		KeyExpiry: ns.KeyExpiry, Now: now, ExpiryWarn: c.cfg.ExpiryWarn,
		ControlHost: c.cfg.ControlHost, TailnetLocked: ns.TailnetLocked,
	}
	c.reportBadLink("bad_auth_host", ns.AuthURL)
	waiting := ns.Backend != status.BackendRunning && ns.Backend != status.BackendNeedsMachineAuth &&
		status.SafeLink(ns.AuthURL, c.cfg.ControlHost) == ""
	if !waiting || c.paused {
		c.waitSince = now
	}
	s.ControlTimedOut = waiting && !c.paused && now.Sub(c.waitSince) > c.cfg.StartTimeout

	ready := !c.paused && ns.Backend == status.BackendRunning && !ns.TailnetLocked &&
		ns.MagicDNS && ns.DNSName != "" && s.HTTPSReady()
	if c.certDomain != ns.DNSName {
		c.certDomain, c.certReady = ns.DNSName, false
	}
	switch {
	case !ready:
		c.stopServing()
	case !c.certReady:
		c.startCert(ctx, ns.DNSName, 0)
	default:
		c.startServing()
		if !c.certBusy && !now.Before(c.certRenewAt) {
			c.startCert(ctx, ns.DNSName, 14*24*time.Hour)
		}
	}
	c.queryConsent(ctx, ns, s.HTTPSReady())
	s.ConsentURL = c.consent
	s.CertReady, s.Serving = c.certReady, c.srv != nil
	c.last = s
	c.emit(status.Derive(s))
}

// queryConsent asks control once per device name for the HTTPS consent
// page, retrying after ConsentRetry if the query fails.
func (c *Controller) queryConsent(ctx context.Context, ns NodeStatus, httpsReady bool) {
	if ns.Backend != status.BackendRunning || ns.DNSName == "" || httpsReady {
		return
	}
	if c.consentFor == ns.DNSName || c.cfg.Now().Before(c.consentNext) {
		return
	}
	u, err := c.cfg.Node.HTTPSConsentURL(ctx)
	if err != nil {
		c.consentNext = c.cfg.Now().Add(c.cfg.ConsentRetry)
		return
	}
	c.consentFor, c.consent = ns.DNSName, u
	c.reportBadLink("bad_consent_host", u)
}

// reportBadLink emits code once for a link the helper refuses to forward,
// naming only its host.
func (c *Controller) reportBadLink(code, raw string) {
	if raw == "" || raw == c.badLink || status.SafeLink(raw, c.cfg.ControlHost) != "" {
		return
	}
	c.badLink = raw
	host := "unparsable"
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		host = u.Hostname()
	}
	c.emit(proto.Event{Type: proto.TypeError, Code: code, Detail: proto.Scrub(host, 100)})
}

func (c *Controller) startCert(ctx context.Context, domain string, minValidity time.Duration) {
	if c.certBusy || c.cfg.Now().Before(c.certNext) {
		return
	}
	c.certBusy = true
	go func() {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		c.certDone <- certResult{domain, c.cfg.Node.Cert(cctx, domain, minValidity)}
	}()
}

func (c *Controller) onCert(r certResult) {
	c.certBusy = false
	now := c.cfg.Now()
	if r.err == nil {
		c.certBackoff, c.certNext = 0, time.Time{}
		c.certRenewAt = now.Add(c.cfg.CertRenew)
		if r.domain == c.certDomain {
			c.certReady = true
		}
		return
	}
	code := "cert_failed"
	if wait, ok := c.cfg.Node.RetryAfter(r.err); ok {
		code, c.certNext = "rate_limited", now.Add(wait)
	} else {
		c.certBackoff = min(max(2*c.certBackoff, time.Minute), 30*time.Minute)
		c.certNext = now.Add(c.certBackoff)
	}
	c.emit(proto.Event{Type: proto.TypeError, Code: code, Detail: proto.Scrub(r.err.Error(), 200)})
}

func (c *Controller) startServing() {
	if c.srv != nil {
		return
	}
	ln, err := c.cfg.Node.ListenTLS(c.cfg.MaxConns)
	if err != nil {
		c.emit(proto.Event{Type: proto.TypeError, Code: "listen_failed", Detail: proto.Scrub(err.Error(), 200)})
		return
	}
	base, cancel := context.WithCancel(context.Background())
	srv := proxy.NewServer(c.cfg.Handler, c.cfg.ErrLog, base)
	c.srv, c.srvCancel = srv, cancel
	go func(ln net.Listener) {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && c.cfg.ErrLog != nil {
			c.cfg.ErrLog.Printf("serve: %v", err)
		}
	}(ln)
}

// stopServing cancels every in-flight request, then closes the listener
// and all connections.
func (c *Controller) stopServing() {
	if c.srv == nil {
		return
	}
	c.srvCancel()
	c.srv.Close()
	c.srv, c.srvCancel = nil, nil
}

func (c *Controller) selfCheck(ctx context.Context, dnsName string, serving bool) {
	f := false
	ev := proto.Event{Type: proto.TypeCheck, OK: &f}
	if !serving || dnsName == "" {
		ev.Code = "not_running"
		c.emit(ev)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	code, err := c.cfg.Node.SelfCheck(cctx, dnsName, c.cfg.CheckPath)
	ev.Status = code
	switch {
	case err != nil:
		ev.Code, ev.Detail = "self_dial_failed", proto.Scrub(err.Error(), 200)
	case code == http.StatusBadGateway:
		ev.Code = "proxy_target_down"
	case code >= 500:
		ev.Code = "gateway_error"
	default:
		t := true
		ev.OK = &t
	}
	c.emit(ev)
}

// logout signs the node out at control (best effort), wipes local state
// and ends Run. It can take up to about 35 s (15 s to reconnect a paused
// node, 20 s for control); the supervisor must not kill the helper then.
// If control cannot be reached the node key is still deleted locally; the
// device then lingers in the admin console until removed.
func (c *Controller) logout(ctx context.Context, cmd proto.Command) error {
	c.stopServing()
	n := c.cfg.Node
	if !c.started {
		if err := c.start(); err != nil {
			c.ack(cmd, err)
			return err
		}
		c.waitBackend(ctx, 15*time.Second)
	} else if c.paused { // control is disconnected while paused
		_ = n.SetWantRunning(ctx, true)
		c.waitBackend(ctx, 15*time.Second)
	}
	lctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	remoteErr := n.Logout(lctx)
	cancel()
	n.Close()
	c.started = false
	wipeErr := c.cfg.Wipe()
	remote := remoteErr == nil
	ev := proto.Event{Type: proto.TypeState, State: proto.StateSignedOut, OK: &remote}
	if remoteErr != nil {
		ev.Code, ev.Detail = "remote_logout_failed", proto.Scrub(remoteErr.Error(), 200)
	}
	if wipeErr != nil {
		ev.Code, ev.Detail = "wipe_failed", proto.Scrub(wipeErr.Error(), 200)
	}
	c.ack(cmd, errors.Join(remoteErr, wipeErr))
	c.emit(ev)
	return ErrSignedOut
}

func (c *Controller) waitBackend(ctx context.Context, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ns, err := c.cfg.Node.Status(ctx); err == nil && ns.Backend != status.BackendStopped && ns.Backend != status.BackendNoState {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func (c *Controller) shutdown() {
	c.stopServing()
	if c.started {
		c.cfg.Node.Close()
	}
}

// ReadCommands feeds parsed stdin commands to a channel that closes at EOF.
// Malformed, unknown or oversize lines are answered with an error event and
// skipped.
func ReadCommands(r io.Reader, out *proto.Writer) <-chan proto.Command {
	ch := make(chan proto.Command, 8)
	go func() {
		defer close(ch)
		err := proto.Read(r, func(c proto.Command) { ch <- c }, func(c proto.Command, err error) {
			code := "bad_command"
			if errors.Is(err, proto.ErrTooLong) {
				code = "too_long"
			}
			out.Emit(proto.Event{Type: proto.TypeError, Code: code, Re: c.ID, Detail: proto.Scrub(err.Error(), 200)})
		})
		if err != nil {
			out.Emit(proto.Event{Type: proto.TypeError, Code: "stdin", Detail: proto.Scrub(err.Error(), 200)})
		}
	}()
	return ch
}
