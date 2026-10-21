// Package control runs the helper: it watches the node, decides the state,
// starts and stops the tailnet listener, and executes watcher commands.
package control

import (
	"context"
	"net"
	"time"
)

// NodeStatus is the subset of tailnet status the controller needs.
type NodeStatus struct {
	Backend     string
	AuthURL     string
	MagicDNS    bool
	DNSName     string // no trailing dot
	CertDomains []string
	KeyExpiry   time.Time
	// TailnetLocked: Tailnet Lock is on and this node is not signed.
	TailnetLocked bool
}

// Node abstracts the embedded Tailscale node so the controller can be
// tested without a tailnet. The real implementation is in internal/node.
type Node interface {
	Start() error
	Status(ctx context.Context) (NodeStatus, error)
	// Changes delivers a value whenever the node's state may have changed;
	// a non-empty string is a backend error message.
	Changes() <-chan string
	StartLogin(ctx context.Context) error
	SetWantRunning(ctx context.Context, on bool) error
	Logout(ctx context.Context) error
	// Cert obtains (or renews) a certificate valid for at least minValidity.
	Cert(ctx context.Context, domain string, minValidity time.Duration) error
	// RetryAfter reports whether err is a certificate rate limit.
	RetryAfter(err error) (time.Duration, bool)
	// HTTPSConsentURL asks control for the page that turns HTTPS on.
	HTTPSConsentURL(ctx context.Context) (string, error)
	// ListenTLS listens on tailnet :443 with the node's certificate. maxConns
	// caps accepted connections below TLS, so the server still gets *tls.Conn
	// and can speak HTTP/2 (a cap above TLS silently breaks h2).
	ListenTLS(maxConns int) (net.Listener, error)
	// SelfCheck requests path from the node's own URL over the tailnet.
	SelfCheck(ctx context.Context, dnsName, path string) (int, error)
	Close() error
}
