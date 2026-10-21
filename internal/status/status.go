// Package status maps the node's condition to one protocol state event.
// It is pure so every transition can be tested without a tailnet.
package status

import (
	"net/url"
	"strings"
	"time"

	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/proto"
)

// Backend states as reported by ipnstate.Status.BackendState.
const (
	BackendNoState          = "NoState"
	BackendNeedsLogin       = "NeedsLogin"
	BackendNeedsMachineAuth = "NeedsMachineAuth"
	BackendStopped          = "Stopped"
	BackendStarting         = "Starting"
	BackendRunning          = "Running"
)

// Snapshot is everything Derive needs.
type Snapshot struct {
	Paused      bool
	Backend     string
	AuthURL     string
	MagicDNS    bool   // tailnet has MagicDNS on
	DNSName     string // node FQDN, no trailing dot
	CertDomains []string
	ConsentURL  string // from QueryFeature("serve"), may be empty
	CertReady   bool   // a certificate for DNSName is on disk
	Serving     bool   // the :443 listener is up
	KeyExpiry   time.Time
	Now         time.Time
	ExpiryWarn  time.Duration // warn this long before key expiry
	ControlHost string        // only links on this host are reported
	FatalCode   string        // set when the node cannot start
	FatalDetail string
	// TailnetLocked: Tailnet Lock is on and this node's key is unsigned.
	TailnetLocked bool
	// ControlTimedOut: still starting after the start timeout.
	ControlTimedOut bool
}

// HTTPSReady reports whether the tailnet issues certificates for the node.
func (s Snapshot) HTTPSReady() bool {
	for _, d := range s.CertDomains {
		if strings.EqualFold(d, s.DNSName) {
			return true
		}
	}
	return false
}

// Derive returns the state event for s.
func Derive(s Snapshot) proto.Event {
	ev := proto.Event{Type: proto.TypeState}
	switch {
	case s.FatalCode != "":
		ev.State, ev.Code, ev.Detail = proto.StateError, s.FatalCode, s.FatalDetail
		return ev
	case s.Paused:
		ev.State = proto.StatePaused
		return ev
	}
	switch s.Backend {
	case BackendNeedsLogin:
		if u := SafeLink(s.AuthURL, s.ControlHost); u != "" {
			ev.State, ev.LoginURL = proto.StateNeedsLogin, u
		} else {
			ev.State = proto.StateStarting // link not issued yet
			if s.ControlTimedOut {
				ev.State, ev.Code = proto.StateControlDown, proto.StateControlDown
			}
		}
		return ev
	case BackendNeedsMachineAuth:
		ev.State = proto.StateNeedsApproval
		return ev
	case BackendRunning:
	default:
		ev.State = proto.StateStarting
		if s.ControlTimedOut {
			ev.State, ev.Code = proto.StateControlDown, proto.StateControlDown
		}
		return ev
	}
	if s.TailnetLocked {
		ev.State, ev.Code = proto.StateTailnetLocked, proto.StateTailnetLocked
		return ev
	}

	ev.DNSName = s.DNSName
	if !s.MagicDNS || s.DNSName == "" {
		ev.State, ev.Need = proto.StateMagicDNSOff, "magicdns"
		return ev
	}
	https := s.HTTPSReady()
	ev.HTTPS = &https
	if !https {
		ev.State, ev.Need = proto.StateHTTPSOff, "https_certificates"
		ev.ConsentURL = SafeLink(s.ConsentURL, s.ControlHost)
		return ev
	}
	if !s.CertReady || !s.Serving {
		ev.State = proto.StateCertPending
		return ev
	}
	ev.URL = "https://" + s.DNSName
	ev.State = proto.StateRunning
	if !s.KeyExpiry.IsZero() {
		ev.KeyExpiry = s.KeyExpiry.UTC().Format(time.RFC3339)
		if s.ExpiryWarn > 0 && s.KeyExpiry.Sub(s.Now) < s.ExpiryWarn {
			ev.State = proto.StateKeyExpiring
		}
	}
	return ev
}

// SafeLink returns raw only if it is an https URL on host with no
// credentials; otherwise "". The helper never forwards other links.
func SafeLink(raw, host string) string {
	if raw == "" || host == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" {
		return ""
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return ""
	}
	if !strings.EqualFold(u.Host, host) {
		return ""
	}
	return u.String()
}

func isLoopback(h string) bool { return h == "127.0.0.1" || h == "localhost" || h == "::1" }
