// Command drive is a minimal stand-in for the watcher's supervisor, used
// for proof runs on headless hosts and Windows (where there are no FIFOs).
// It starts the helper, keeps its stdin open, forwards each new line
// appended to a commands file, and logs every event with a timestamp.
// A line "EOF" in the commands file closes the helper's stdin.
//
//	drive -dir <work dir> -- <helper> --dir <state> --target http://127.0.0.1:18445
//
// Files in <work dir>: cmds.txt (append commands), events.log, helper.err.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	dir := flag.String("dir", ".", "work directory")
	flag.Parse()
	if flag.NArg() == 0 {
		log.Fatal("usage: drive -dir <work dir> -- <helper> [args]")
	}
	cmdsPath := filepath.Join(*dir, "cmds.txt")
	if f, err := os.OpenFile(cmdsPath, os.O_CREATE, 0o600); err == nil {
		f.Close()
	}
	events, err := os.OpenFile(filepath.Join(*dir, "events.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	errf, err := os.OpenFile(filepath.Join(*dir, "helper.err"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Fatal(err)
	}
	stamp := func(s string) { fmt.Fprintf(events, "%s %s\n", time.Now().UTC().Format("15:04:05.000"), s) }

	cmd := exec.Command(flag.Arg(0), flag.Args()[1:]...)
	cmd.Stderr = errf
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	t0 := time.Now()
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	stamp(fmt.Sprintf("drive: started pid %d", cmd.Process.Pid))
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			stamp(fmt.Sprintf("+%.3fs %s", time.Since(t0).Seconds(), sc.Text()))
		}
	}()
	go forward(cmdsPath, stdin, stamp)
	err = cmd.Wait()
	stamp(fmt.Sprintf("drive: helper exited after %.3fs: %v (code %d)", time.Since(t0).Seconds(), err, cmd.ProcessState.ExitCode()))
}

// forward tails the commands file and writes new lines to the helper.
func forward(path string, w io.WriteCloser, stamp func(string)) {
	var off int64
	for {
		time.Sleep(300 * time.Millisecond)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		f.Seek(off, io.SeekStart)
		r := bufio.NewReader(f)
		for {
			line, err := r.ReadString('\n')
			if err != nil { // partial line: wait for the rest
				break
			}
			off += int64(len(line))
			line = strings.TrimSpace(line)
			switch {
			case line == "":
			case line == "EOF":
				stamp("drive: closing helper stdin")
				w.Close()
				f.Close()
				return
			default:
				stamp("drive: > " + line)
				io.WriteString(w, line+"\n")
			}
		}
		f.Close()
	}
}
