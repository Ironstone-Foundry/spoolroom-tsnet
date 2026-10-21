package status

import (
	"testing"
	"time"

	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/proto"
)

const host = "login.tailscale.com"

func running() Snapshot {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	return Snapshot{
		Backend:     BackendRunning,
		MagicDNS:    true,
		DNSName:     "spoolroom-watcher-ab12.tail1234.ts.net",
		CertDomains: []string{"spoolroom-watcher-ab12.tail1234.ts.net"},
		CertReady:   true,
		Serving:     true,
		KeyExpiry:   now.Add(180 * 24 * time.Hour),
		Now:         now,
		ExpiryWarn:  14 * 24 * time.Hour,
		ControlHost: host,
	}
}

func TestDeriveTable(t *testing.T) {
	cases := []struct {
		name  string
		mut   func(*Snapshot)
		state string
		check func(t *testing.T, ev proto.Event)
	}{
		{"nostate", func(s *Snapshot) { s.Backend = BackendNoState }, proto.StateStarting, nil},
		{"starting", func(s *Snapshot) { s.Backend = BackendStarting }, proto.StateStarting, nil},
		{"needs login without link yet", func(s *Snapshot) { s.Backend = BackendNeedsLogin }, proto.StateStarting, nil},
		{"needs login", func(s *Snapshot) {
			s.Backend, s.AuthURL = BackendNeedsLogin, "https://login.tailscale.com/a/abc123"
		}, proto.StateNeedsLogin, func(t *testing.T, ev proto.Event) {
			if ev.LoginURL != "https://login.tailscale.com/a/abc123" {
				t.Fatalf("loginUrl %q", ev.LoginURL)
			}
		}},
		{"needs login, foreign link dropped", func(s *Snapshot) {
			s.Backend, s.AuthURL = BackendNeedsLogin, "https://evil.example/a/abc"
		}, proto.StateStarting, nil},
		{"needs approval", func(s *Snapshot) { s.Backend = BackendNeedsMachineAuth }, proto.StateNeedsApproval, nil},
		{"magicdns off", func(s *Snapshot) { s.MagicDNS = false }, proto.StateMagicDNSOff, func(t *testing.T, ev proto.Event) {
			if ev.Need != "magicdns" {
				t.Fatalf("need %q", ev.Need)
			}
		}},
		{"https off with consent link", func(s *Snapshot) {
			s.CertDomains, s.ConsentURL = nil, "https://login.tailscale.com/f/serve?node=x"
		}, proto.StateHTTPSOff, func(t *testing.T, ev proto.Event) {
			if ev.Need != "https_certificates" || ev.ConsentURL == "" || ev.HTTPS == nil || *ev.HTTPS {
				t.Fatalf("bad https_off event %+v", ev)
			}
			if ev.DNSName == "" {
				t.Fatal("dnsName missing")
			}
		}},
		{"https off, foreign consent link dropped", func(s *Snapshot) {
			s.CertDomains, s.ConsentURL = nil, "http://login.tailscale.com/f/serve"
		}, proto.StateHTTPSOff, func(t *testing.T, ev proto.Event) {
			if ev.ConsentURL != "" {
				t.Fatalf("consent link not dropped: %q", ev.ConsentURL)
			}
		}},
		{"cert pending", func(s *Snapshot) { s.CertReady = false }, proto.StateCertPending, nil},
		{"listener not up", func(s *Snapshot) { s.Serving = false }, proto.StateCertPending, nil},
		{"running", func(s *Snapshot) {}, proto.StateRunning, func(t *testing.T, ev proto.Event) {
			if ev.URL != "https://spoolroom-watcher-ab12.tail1234.ts.net" || ev.HTTPS == nil || !*ev.HTTPS {
				t.Fatalf("bad running event %+v", ev)
			}
			if ev.KeyExpiry != "2027-04-18T12:00:00Z" {
				t.Fatalf("keyExpiry %q", ev.KeyExpiry)
			}
			if ev.LoginURL != "" {
				t.Fatal("loginUrl leaked into running")
			}
		}},
		{"key expiry disabled", func(s *Snapshot) { s.KeyExpiry = time.Time{} }, proto.StateRunning, func(t *testing.T, ev proto.Event) {
			if ev.KeyExpiry != "" {
				t.Fatal("unexpected keyExpiry")
			}
		}},
		{"key expiring", func(s *Snapshot) { s.KeyExpiry = s.Now.Add(3 * 24 * time.Hour) }, proto.StateKeyExpiring, func(t *testing.T, ev proto.Event) {
			if ev.URL == "" || ev.KeyExpiry == "" {
				t.Fatalf("key_expiring must still carry url and expiry: %+v", ev)
			}
		}},
		{"control unreachable while starting", func(s *Snapshot) { s.Backend, s.ControlTimedOut = BackendStarting, true }, proto.StateControlDown, func(t *testing.T, ev proto.Event) {
			if ev.Code != "control_unreachable" {
				t.Fatalf("code %q", ev.Code)
			}
		}},
		{"control unreachable before a link", func(s *Snapshot) { s.Backend, s.ControlTimedOut = BackendNeedsLogin, true }, proto.StateControlDown, nil},
		{"link wins over timeout", func(s *Snapshot) {
			s.Backend, s.ControlTimedOut, s.AuthURL = BackendNeedsLogin, true, "https://login.tailscale.com/a/x"
		}, proto.StateNeedsLogin, nil},
		{"tailnet locked", func(s *Snapshot) { s.TailnetLocked = true }, proto.StateTailnetLocked, func(t *testing.T, ev proto.Event) {
			if ev.Code != "tailnet_locked" || ev.URL != "" {
				t.Fatalf("bad event %+v", ev)
			}
		}},
		{"paused wins", func(s *Snapshot) { s.Paused = true }, proto.StatePaused, nil},
		{"fatal wins", func(s *Snapshot) { s.Paused, s.FatalCode = true, "start_failed" }, proto.StateError, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := running()
			c.mut(&s)
			ev := Derive(s)
			if ev.State != c.state {
				t.Fatalf("state = %q, want %q (%+v)", ev.State, c.state, ev)
			}
			if c.check != nil {
				c.check(t, ev)
			}
		})
	}
}

func TestCertDomainMustMatchNode(t *testing.T) {
	s := running()
	s.CertDomains = []string{"other.tail1234.ts.net"}
	if ev := Derive(s); ev.State != proto.StateHTTPSOff {
		t.Fatalf("got %q", ev.State)
	}
}

func TestSafeLink(t *testing.T) {
	ok := []string{"https://login.tailscale.com/a/abc"}
	bad := []string{"", "javascript:alert(1)", "https://user:pw@login.tailscale.com/a/x", "https://login.tailscale.com.evil.io/a", "ftp://login.tailscale.com/a"}
	for _, u := range ok {
		if SafeLink(u, host) == "" {
			t.Errorf("rejected %q", u)
		}
	}
	for _, u := range bad {
		if SafeLink(u, host) != "" {
			t.Errorf("accepted %q", u)
		}
	}
	if SafeLink("http://127.0.0.1:9911/a/x", "127.0.0.1:9911") == "" {
		t.Error("loopback test control rejected")
	}
}
