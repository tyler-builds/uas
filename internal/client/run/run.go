// Package run executes the system tools that backends drive (flatpak, brew, rpm-ostree, pkexec).
package run

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Runner runs a command. onLine, if non-nil, receives each line of standard output as it arrives; lines
// ending in a carriage return (progress bars) count as lines too. Run returns the full standard output.
type Runner interface {
	Run(ctx context.Context, name string, args []string, onLine func(string)) (string, error)
}

// Error is returned when a command exits unsuccessfully. Stderr holds the end of its error output.
type Error struct {
	Cmd    string
	Err    error
	Stderr string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		return fmt.Sprintf("%s: %v", e.Cmd, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Cmd, msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Exec runs commands as real processes.
type Exec struct{}

// maxStderr bounds how much error output is kept for messages.
const maxStderr = 8 << 10

// Run implements Runner.
func (Exec) Run(ctx context.Context, name string, args []string, onLine func(string)) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Stable, untranslated output that parsers can rely on.
	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8", "LANG=C.UTF-8")
	cmd.Stdin = nil
	stderr := &tailBuffer{max: maxStderr}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	var out bytes.Buffer
	sc := bufio.NewScanner(io.TeeReader(stdout, &out))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	sc.Split(scanLinesOrCR)
	for sc.Scan() {
		if onLine != nil {
			if line := strings.TrimSpace(sc.Text()); line != "" {
				onLine(line)
			}
		}
	}
	// Drain anything left if the scanner stopped early, so the process is not blocked on a full pipe.
	_, _ = io.Copy(&out, stdout)

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return out.String(), ctx.Err()
		}
		return out.String(), &Error{Cmd: name + " " + strings.Join(args, " "), Err: err, Stderr: stderr.String()}
	}
	return out.String(), nil
}

// scanLinesOrCR splits on "\n", "\r\n" or a lone "\r".
func scanLinesOrCR(data []byte, atEOF bool) (int, []byte, error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		adv := i + 1
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			adv++
		} else if data[i] == '\r' && i+1 == len(data) && !atEOF {
			// Might be the start of "\r\n"; wait for more.
			return 0, nil, nil
		}
		return adv, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// IsNotFound reports whether err means the command does not exist.
func IsNotFound(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
}
