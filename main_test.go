package main

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
)

func TestVersionAndLicenses(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, nil, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "spoolroom-tsnet ") {
		t.Fatalf("--version: %d %q", code, out.String())
	}
	out.Reset()
	run([]string{"--licenses"}, nil, &out, &errb)
	for _, want := range []string{"MIT License", "BSD 3-Clause License", "Tailscale Inc", "not made, endorsed"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("--licenses missing %q", want)
		}
	}
}

func TestRefusesBadFlagsBeforeStarting(t *testing.T) {
	dir := t.TempDir()
	cases := [][]string{
		{"--dir", dir, "--target", "http://192.168.1.5:8445"},
		{"--dir", dir, "--target", "http://localhost:8445"},
		{"--dir", dir},
		{"--target", "http://127.0.0.1:8445"},
		{"--dir", dir, "--target", "http://127.0.0.1:8445", "--hostname", "Bad_Name"},
	}
	for _, args := range cases {
		var out, errb bytes.Buffer
		if code := run(args, strings.NewReader(""), &out, &errb); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if out.Len() != 0 {
			t.Errorf("%v: wrote to stdout: %q", args, out.String())
		}
	}
}

func TestScrubWriter(t *testing.T) {
	var b bytes.Buffer
	scrubWriter{&b}.Write([]byte("go to: https://login.tailscale.com/a/secret123\n"))
	if strings.Contains(b.String(), "secret123") {
		t.Fatalf("link leaked: %q", b.String())
	}
}

func TestStdLogIsScrubbed(t *testing.T) {
	defer log.SetOutput(os.Stderr)
	defer log.SetPrefix("")
	var out, errb bytes.Buffer
	run([]string{"--dir", t.TempDir()}, strings.NewReader(""), &out, &errb) // exits on the missing target
	errb.Reset()
	log.Printf("proxyconnect tcp: dial http://alice:s3cret@proxy.lan:3128")
	log.Printf("sent Proxy-Authorization: Basic YWxpY2U6czNjcmV0")
	if s := errb.String(); strings.Contains(s, "s3cret") || strings.Contains(s, "YWxpY2U6czNjcmV0") || !strings.Contains(s, "proxyconnect") {
		t.Fatalf("std log not scrubbed: %q", s)
	}
}

// --paused must report paused and exit on EOF without starting tsnet,
// so it never contacts Tailscale.
func TestPausedStartNeverStartsNode(t *testing.T) {
	defer log.SetOutput(os.Stderr)
	defer log.SetPrefix("")
	var out, errb bytes.Buffer
	dir := t.TempDir() + "/tsnet"
	code := run([]string{"--dir", dir, "--target", "http://127.0.0.1:18445", "--paused"}, strings.NewReader(`{"cmd":"status"}`+"\n"), &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, `"type":"hello"`) || !strings.Contains(s, `"state":"paused"`) || strings.Contains(s, `"state":"starting"`) {
		t.Fatalf("stdout: %s", s)
	}
	if _, err := os.Stat(dir + "/tailscaled.state"); err == nil {
		t.Fatal("node state written while paused")
	}
}
