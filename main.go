// Command spoolroom-tsnet puts a Spoolroom watcher's loopback gateway on
// the member's own tailnet. It uses Tailscale (tailscale.com/tsnet) and is
// run as a child of the watcher, which talks to it over stdin/stdout.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"tailscale.com/envknob"
	"tailscale.com/logtail"
	tsversion "tailscale.com/version"

	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/control"
	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/node"
	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/proto"
	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/proxy"
	"github.com/Ironstone-Foundry/spoolroom-tsnet/internal/statedir"
)

const defaultControl = "https://controlplane.tailscale.com"

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spoolroom-tsnet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "state directory (required; created with mode 0700)")
	target := fs.String("target", "", "watcher gateway, http://127.0.0.1:<port> (required)")
	hostname := fs.String("hostname", "", "tailnet device name (default spoolroom-watcher-<4 random chars>, kept in --dir)")
	controlURL := fs.String("control-url", "", "TEST ONLY: coordination server URL; never set in production")
	paused := fs.Bool("paused", false, "start paused: report state paused and stay off the tailnet until a resume command")
	verbose := fs.Bool("verbose", false, "send scrubbed backend logs to stderr")
	expiryWarn := fs.Duration("expiry-warn", 14*24*time.Hour, "report key_expiring this long before the node key expires")
	showVersion := fs.Bool("version", false, "print version and exit")
	showLicenses := fs.Bool("licenses", false, "print licenses and exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch {
	case *showVersion:
		fmt.Fprintf(stdout, "spoolroom-tsnet %s (tailscale.com %s, client version %s)\n", version, tailscaleVersion(), tsversion.Long())
		return 0
	case *showLicenses:
		fmt.Fprint(stdout, licenses())
		return 0
	}

	// Log upload to Tailscale is off (owner decision D4). The release build
	// also omits logtail entirely with the ts_omit_logtail tag.
	logtail.Disable()
	envknob.SetNoLogsNoSupport()

	// Everything that logs, including the std logger used by dependencies,
	// goes through the scrubber.
	log.SetOutput(scrubWriter{stderr})
	log.SetPrefix("spoolroom-tsnet: ")
	errLog := log.New(scrubWriter{stderr}, "spoolroom-tsnet: ", log.LstdFlags)
	fail := func(format string, a ...any) int { errLog.Printf(format, a...); return 2 }

	tgt, err := proxy.ParseTarget(*target)
	if err != nil {
		return fail("--target: %v", err)
	}
	if *dir == "" {
		return fail("--dir is required")
	}
	absDir, err := filepath.Abs(*dir)
	if err != nil {
		return fail("--dir: %v", err)
	}
	if err := statedir.Prepare(absDir); err != nil {
		return fail("--dir: %v", err)
	}
	host := *hostname
	if host == "" {
		if host, err = statedir.DefaultHostname(absDir, "spoolroom-watcher"); err != nil {
			return fail("hostname: %v", err)
		}
	} else if !statedir.ValidHostname(host) {
		return fail("--hostname must be one lowercase DNS label")
	}
	ctl := *controlURL
	if ctl == "" {
		ctl = defaultControl
	}
	cu, err := url.Parse(ctl)
	if err != nil || cu.Host == "" {
		return fail("--control-url: invalid")
	}
	linkHost := cu.Host
	if *controlURL == "" {
		linkHost = "login.tailscale.com" // sign-in and consent pages live here
	}

	out := proto.NewWriter(stdout)
	out.Emit(proto.Event{Type: proto.TypeHello, Version: version, Tailscale: tailscaleVersion()})

	userLogf := func(format string, a ...any) {
		// tsnet repeats its sign-in hint every few seconds; the link is on
		// the protocol already, so keep it out of the watcher's log.
		if strings.HasPrefix(format, "To start this tsnet server") {
			return
		}
		errLog.Printf(format, a...)
	}
	var backendLogf func(string, ...any)
	if *verbose {
		backendLogf = userLogf
	}
	n := node.New(node.Options{Dir: absDir, Hostname: host, ControlURL: *controlURL, UserLogf: userLogf, Logf: backendLogf})

	c := control.New(control.Config{
		Node: n, Out: out, Handler: proxy.New(tgt, proxy.Options{ErrLog: errLog}),
		StartPaused: *paused,
		Wipe:        func() error { return statedir.Wipe(absDir) },
		ControlHost: linkHost, ExpiryWarn: *expiryWarn, ErrLog: errLog,
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = c.Run(ctx, control.ReadCommands(stdin, out))
	switch {
	case err == nil, errors.Is(err, control.ErrSignedOut):
		return 0
	default:
		errLog.Printf("exit: %v", err)
		return 1
	}
}

// scrubWriter strips links and keys from every log line before stderr.
type scrubWriter struct{ w io.Writer }

func (s scrubWriter) Write(p []byte) (int, error) {
	if _, err := io.WriteString(s.w, proto.Scrub(string(p), 2000)+"\n"); err != nil {
		return 0, err
	}
	return len(p), nil
}
