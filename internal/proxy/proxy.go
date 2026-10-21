// Package proxy is the streaming reverse proxy from the tailnet listener to
// the watcher's loopback gateway. It forwards to one loopback port only.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"golang.org/x/net/netutil"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrForbiddenTarget is returned for any dial other than the configured one.
var ErrForbiddenTarget = errors.New("proxy: target not allowed")

// Limits. A browser opens at most a few streams per camera; these caps keep
// a misbehaving peer from exhausting the watcher.
const (
	MaxConns        = 128 // concurrent tailnet connections accepted
	MaxUpstreamConn = 64  // concurrent connections to the gateway
	BodyTimeout     = 30 * time.Second
)

// ParseTarget accepts only http://127.0.0.1:<port> or http://[::1]:<port>.
// Names such as "localhost" are refused because they resolve through DNS.
func ParseTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("target: %w", err)
	}
	if u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, fmt.Errorf("target must be http://127.0.0.1:<port>")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("target must not have a path")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return nil, fmt.Errorf("target needs a port: %w", err)
	}
	if host != "127.0.0.1" && host != "::1" {
		return nil, fmt.Errorf("target host must be 127.0.0.1 or ::1, got %q", host)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 || strconv.Itoa(p) != port {
		return nil, fmt.Errorf("target port invalid: %q", port)
	}
	return &url.URL{Scheme: "http", Host: u.Host}, nil
}

// dropHeader reports whether an inbound header must not reach the gateway:
// forwarding chains a peer could forge, and any Tailscale identity header
// (the helper vouches for none). Underscore spellings count too, since
// some servers treat "_" and "-" alike.
func dropHeader(name string) bool {
	n := strings.ReplaceAll(strings.ToLower(name), "_", "-")
	switch n {
	case "forwarded", "x-forwarded-for", "x-forwarded-host", "x-forwarded-proto",
		"x-forwarded-port", "x-real-ip", "x-client-ip", "true-client-ip":
		return true
	}
	return strings.HasPrefix(n, "tailscale-") || strings.HasPrefix(n, "x-forwarded-")
}

// Transport returns a transport that can only reach target.Host.
func Transport(target *url.URL) *http.Transport {
	addr := target.Host
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy: nil, // never honour HTTP(S)_PROXY
		DialContext: func(ctx context.Context, network, a string) (net.Conn, error) {
			if a != addr {
				return nil, ErrForbiddenTarget
			}
			return d.DialContext(ctx, "tcp", addr)
		},
		DisableCompression:    true, // pass encodings through untouched
		MaxConnsPerHost:       MaxUpstreamConn,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// Options tune New; zero values take the package defaults.
type Options struct {
	ErrLog      *log.Logger
	BodyTimeout time.Duration // a request body must arrive within this
}

// New returns the proxy handler. Responses are flushed as they arrive.
func New(target *url.URL, o Options) http.Handler {
	if o.BodyTimeout == 0 {
		o.BodyTimeout = BodyTimeout
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target) // scheme, host and Host header become the target
			for name := range pr.Out.Header {
				if dropHeader(name) {
					pr.Out.Header.Del(name)
				}
			}
			pr.Out.Header.Set("X-Forwarded-Proto", "https")
			pr.Out.Header.Set("X-Forwarded-Host", pr.In.Host)
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("X-Content-Type-Options", "nosniff")
			rewriteLocation(resp, target)
			return nil
		},
		Transport:     Transport(target),
		FlushInterval: -1, // MJPEG, FLV and SSE must not be buffered
		ErrorLog:      o.ErrLog,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if o.ErrLog != nil {
				o.ErrLog.Printf("proxy: %s %s: %v", r.Method, r.URL.Path, err)
			}
			http.Error(w, "watcher gateway unavailable", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodConnect || r.Method == http.MethodTrace:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		case r.Header.Get("Upgrade") != "" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade"):
			// The gateway has no WebSocket or other upgrade endpoint, and
			// a hijacked connection would outlive a pause.
			http.Error(w, "upgrades not supported", http.StatusBadRequest)
			return
		}
		bodyDeadline(w, r, o.BodyTimeout)
		rp.ServeHTTP(w, r)
	})
}

// bodyDeadline bounds how long a request body may take to arrive, then
// lifts the deadline so long streaming responses are never cut. (A server
// ReadTimeout would also cancel streams once it passed.)
func bodyDeadline(w http.ResponseWriter, r *http.Request, d time.Duration) {
	rc := http.NewResponseController(w)
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		_ = rc.SetReadDeadline(time.Time{})
		return
	}
	_ = rc.SetReadDeadline(time.Now().Add(d))
	r.Body = &deadlineBody{ReadCloser: r.Body, done: func() { _ = rc.SetReadDeadline(time.Time{}) }}
}

type deadlineBody struct {
	io.ReadCloser
	done func()
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if errors.Is(err, io.EOF) && b.done != nil {
		b.done()
		b.done = nil
	}
	return n, err
}

// rewriteLocation points a redirect at the gateway's own loopback address
// back at the tailnet name the client used.
func rewriteLocation(resp *http.Response, target *url.URL) {
	loc := resp.Header.Get("Location")
	if loc == "" || resp.Request == nil {
		return
	}
	u, err := url.Parse(loc)
	if err != nil || u.Host != target.Host {
		return
	}
	pub := resp.Request.Header.Get("X-Forwarded-Host")
	if pub == "" {
		return
	}
	u.Scheme, u.Host = "https", pub
	resp.Header.Set("Location", u.String())
}

// NewServer wraps h with timeouts suited to long streams: a bounded header
// read, request bodies bounded by the handler, and no write timeout so a
// 15-minute MJPEG view is not cut. base, if set, is every request's parent
// context; cancelling it ends in-flight requests.
func NewServer(h http.Handler, errLog *log.Logger, base context.Context) *http.Server {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
		ErrorLog:          errLog,
	}
	if base != nil {
		srv.BaseContext = func(net.Listener) context.Context { return base }
	}
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetHTTP1(true)
	srv.Protocols.SetHTTP2(true)
	return srv
}

// Limit caps concurrent accepted connections on ln. Apply it to the raw
// listener below TLS: wrapping a TLS listener hides *tls.Conn from
// http.Server, which then cannot speak HTTP/2.
func Limit(ln net.Listener, n int) net.Listener { return netutil.LimitListener(ln, n) }
