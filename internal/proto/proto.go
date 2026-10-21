// Package proto is the JSON-lines control channel between the helper and
// the Spoolroom watcher: events on stdout, commands on stdin.
package proto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"sync"
)

// Version is the protocol version carried in every event as "v".
const Version = 1

// Event types.
const (
	TypeHello = "hello"
	TypeState = "state"
	TypeError = "error"
	TypeCheck = "check"
	TypeAck   = "ack"
)

// States reported in a "state" event.
const (
	StateStarting      = "starting"
	StateNeedsLogin    = "needs_login"
	StateNeedsApproval = "needs_approval"
	StateMagicDNSOff   = "magicdns_off"
	StateHTTPSOff      = "https_off"
	StateCertPending   = "cert_pending"
	StateRunning       = "running"
	StateKeyExpiring   = "key_expiring"
	StatePaused        = "paused"
	StateSignedOut     = "signed_out"
	StateTailnetLocked = "tailnet_locked"      // Tailnet Lock: sign this device from a trusted one
	StateControlDown   = "control_unreachable" // no answer from the coordination server
	StateError         = "error"
)

// Event is one line on stdout. Empty fields are omitted.
type Event struct {
	V    int    `json:"v"`
	Type string `json:"type"`

	// hello
	Version   string `json:"version,omitempty"`
	Tailscale string `json:"tailscale,omitempty"`

	// state
	State      string `json:"state,omitempty"`
	LoginURL   string `json:"loginUrl,omitempty"`   // needs_login only
	ConsentURL string `json:"consentUrl,omitempty"` // https_off, if control offers one
	Need       string `json:"need,omitempty"`       // magicdns_off / https_off: "magicdns" or "https_certificates"
	URL        string `json:"url,omitempty"`        // https://<dnsName>
	DNSName    string `json:"dnsName,omitempty"`
	HTTPS      *bool  `json:"https,omitempty"`
	KeyExpiry  string `json:"keyExpiry,omitempty"` // RFC 3339, UTC

	// error / check
	Code   string `json:"code,omitempty"`
	Detail string `json:"detail,omitempty"`
	OK     *bool  `json:"ok,omitempty"`
	Status int    `json:"status,omitempty"`

	// ack: echoes a command's id
	Re  string `json:"re,omitempty"`
	Cmd string `json:"cmd,omitempty"`
}

// Writer serialises events to one line each and drops a state event that
// is identical to the previous one.
type Writer struct {
	mu   sync.Mutex
	w    io.Writer
	last *Event
}

// NewWriter returns a Writer on w.
func NewWriter(w io.Writer) *Writer { return &Writer{w: w} }

// Emit writes ev. It reports whether a line was written.
func (pw *Writer) Emit(ev Event) bool {
	ev.V = Version
	pw.mu.Lock()
	defer pw.mu.Unlock()
	if ev.Type == TypeState {
		if pw.last != nil && reflect.DeepEqual(*pw.last, ev) {
			return false
		}
		cp := ev
		pw.last = &cp
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return false
	}
	_, err = pw.w.Write(append(b, '\n'))
	return err == nil
}

// Forget clears the duplicate filter so the next state is always written.
func (pw *Writer) Forget() {
	pw.mu.Lock()
	pw.last = nil
	pw.mu.Unlock()
}

// Commands accepted on stdin.
const (
	CmdStatus = "status" // re-emit the current state
	CmdLogin  = "login"  // ask control for a fresh sign-in link
	CmdPause  = "pause"  // stop serving, node offline, keep state
	CmdResume = "resume"
	CmdLogout = "logout" // sign out, wipe state, exit
	CmdCheck  = "check"  // self-check through the tailnet
)

var knownCmds = map[string]bool{
	CmdStatus: true, CmdLogin: true, CmdPause: true,
	CmdResume: true, CmdLogout: true, CmdCheck: true,
}

// Command is one line on stdin.
type Command struct {
	Cmd string `json:"cmd"`
	ID  string `json:"id,omitempty"`
}

// MaxLine bounds a command line.
const MaxLine = 4096

// ErrUnknown is returned for a well-formed line naming no known command.
var ErrUnknown = errors.New("unknown command")

// ErrTooLong is reported for a line longer than MaxLine; it is skipped.
var ErrTooLong = errors.New("too_long")

// Parse decodes one command line.
func Parse(line []byte) (Command, error) {
	var c Command
	if err := json.Unmarshal(line, &c); err != nil {
		return Command{}, err
	}
	if r := []rune(c.ID); len(r) > 64 {
		c.ID = string(r[:64])
	}
	if !knownCmds[c.Cmd] {
		return c, ErrUnknown
	}
	return c, nil
}

// Read sends each command line from r to fn until EOF. A bad or oversize
// line is reported through bad and skipped; reading continues.
func Read(r io.Reader, fn func(Command), bad func(Command, error)) error {
	br := bufio.NewReaderSize(r, MaxLine+1)
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			for errors.Is(err, bufio.ErrBufferFull) { // discard to the newline
				_, err = br.ReadSlice('\n')
			}
			bad(Command{}, ErrTooLong)
		} else if l := bytes.TrimSpace(line); len(l) > 0 {
			if c, perr := Parse(l); perr != nil {
				bad(c, perr)
			} else {
				fn(c)
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
