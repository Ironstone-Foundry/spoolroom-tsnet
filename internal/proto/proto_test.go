package proto

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEmitOneLineEachAndVersion(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	w.Emit(Event{Type: TypeState, State: StateStarting})
	w.Emit(Event{Type: TypeError, Code: "x", Detail: "y"})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %q", buf.String())
	}
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad json %q: %v", l, err)
		}
		if m["v"] != float64(1) {
			t.Fatalf("missing v:1 in %q", l)
		}
	}
	if lines[0] != `{"v":1,"type":"state","state":"starting"}` {
		t.Fatalf("unexpected encoding %s", lines[0])
	}
}

func TestEmitDropsDuplicateStates(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	if !w.Emit(Event{Type: TypeState, State: StateStarting}) {
		t.Fatal("first emit dropped")
	}
	if w.Emit(Event{Type: TypeState, State: StateStarting}) {
		t.Fatal("duplicate not dropped")
	}
	// Errors are never deduplicated.
	w.Emit(Event{Type: TypeError, Code: "a"})
	w.Emit(Event{Type: TypeError, Code: "a"})
	w.Forget()
	if !w.Emit(Event{Type: TypeState, State: StateStarting}) {
		t.Fatal("Forget did not reset filter")
	}
	if n := strings.Count(buf.String(), "\n"); n != 4 {
		t.Fatalf("want 4 lines, got %d: %s", n, buf.String())
	}
}

func TestParse(t *testing.T) {
	c, err := Parse([]byte(`{"cmd":"pause","id":"7"}`))
	if err != nil || c.Cmd != CmdPause || c.ID != "7" {
		t.Fatalf("got %+v %v", c, err)
	}
	if _, err := Parse([]byte(`{"cmd":"funnel"}`)); !errors.Is(err, ErrUnknown) {
		t.Fatalf("want ErrUnknown, got %v", err)
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("want error for bad json")
	}
	c, _ = Parse([]byte(`{"cmd":"check","id":"` + strings.Repeat("x", 100) + `"}`))
	if len(c.ID) != 64 {
		t.Fatalf("id not truncated: %d", len(c.ID))
	}
}

func TestReadSkipsBadLinesAndStopsAtEOF(t *testing.T) {
	in := "{\"cmd\":\"status\"}\n\nnope\n{\"cmd\":\"logout\"}\n"
	var got []string
	var bad int
	err := Read(strings.NewReader(in), func(c Command) { got = append(got, c.Cmd) }, func(Command, error) { bad++ })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "status,logout" || bad != 1 {
		t.Fatalf("got %v bad=%d", got, bad)
	}
}

func TestReadSkipsOversizeLineAndContinues(t *testing.T) {
	in := `{"cmd":"status","id":"` + strings.Repeat("x", 3*MaxLine) + "\"}\n{\"cmd\":\"pause\"}\n"
	var got []string
	var bads []error
	err := Read(strings.NewReader(in), func(c Command) { got = append(got, c.Cmd) }, func(_ Command, err error) { bads = append(bads, err) })
	if err != nil {
		t.Fatal(err)
	}
	if len(bads) != 1 || !errors.Is(bads[0], ErrTooLong) || strings.Join(got, ",") != "pause" {
		t.Fatalf("got %v bads=%v", got, bads)
	}
}

func TestReadLastLineWithoutNewline(t *testing.T) {
	var got []string
	Read(strings.NewReader(`{"cmd":"check"}`), func(c Command) { got = append(got, c.Cmd) }, func(Command, error) {})
	if strings.Join(got, ",") != "check" {
		t.Fatalf("got %v", got)
	}
}

func TestIDTruncationIsRuneSafe(t *testing.T) {
	c, _ := Parse([]byte(`{"cmd":"check","id":"` + strings.Repeat("é", 100) + `"}`))
	if r := []rune(c.ID); len(r) != 64 || strings.ContainsRune(c.ID, '\uFFFD') {
		t.Fatalf("bad truncation: %d runes", len(r))
	}
}

func TestScrub(t *testing.T) {
	in := "To start, go to: https://login.tailscale.com/a/abc123 now\n key tskey-auth-kXYZ-123"
	out := Scrub(in, 200)
	if strings.Contains(out, "login.tailscale.com") || strings.Contains(out, "abc123") || strings.Contains(out, "tskey-auth") {
		t.Fatalf("not scrubbed: %q", out)
	}
	for _, in := range []string{
		"proxyconnect tcp: dial http://alice:s3cret@proxy.lan:3128 failed",
		"using proxy alice:s3cret@proxy.lan:3128",
		"Proxy-Authorization: Basic YWxpY2U6czNjcmV0",
		"authorization=Bearer abc.def.ghi",
	} {
		out := Scrub(in, 200)
		for _, bad := range []string{"s3cret", "YWxpY2U6czNjcmV0", "abc.def.ghi"} {
			if strings.Contains(out, bad) {
				t.Errorf("Scrub(%q) = %q leaks %q", in, out, bad)
			}
		}
	}
	if got := Scrub(strings.Repeat("é", 300), 200); len([]rune(got)) != 200 {
		t.Fatalf("not truncated to 200 runes: %d", len([]rune(got)))
	}
}
