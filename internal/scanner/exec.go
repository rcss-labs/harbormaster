// Package scanner is the scan task: it reads a job from the bucket, pulls the
// listed ECR images, runs Syft and Grype and writes results back.
// It imports only internal/scan and the AWS SDK.
package scanner

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const maxStderrTail = 256

// CommandResult is what a finished command produced.
type CommandResult struct {
	Stdout     []byte
	StderrTail string // last non-empty stderr line, at most 256 characters
	ExitCode   int
}

// Command runs an external program with exactly the given environment.
// A non-zero exit is reported in the result, not as an error; an error means
// the program could not run or the context ended first.
type Command interface {
	Run(ctx context.Context, name string, args []string, env []string, stdoutMax int64) (CommandResult, error)
}

// OSCommand runs programs with os/exec.
type OSCommand struct {
	// WaitDelay bounds how long Run waits for output pipes after the process ends or is killed.
	WaitDelay time.Duration
}

type limitedBuffer struct {
	buf bytes.Buffer
	max int64
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - int64(l.buf.Len()); room > 0 {
		l.buf.Write(p[:min(int64(len(p)), room)])
	}
	return len(p), nil // excess output is dropped, not an error
}

// tailBuffer keeps only the last max bytes written.
type tailBuffer struct {
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return len(p), nil
}

const (
	stderrTailWindow = 8 << 10
	defaultWaitDelay = 5 * time.Second
)

func (c OSCommand) Run(ctx context.Context, name string, args []string, env []string, stdoutMax int64) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	// The tool runs in its own process group so a timeout kills its children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = c.WaitDelay
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = defaultWaitDelay
	}
	stdout := &limitedBuffer{max: stdoutMax}
	stderr := &tailBuffer{max: stderrTailWindow}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	err := cmd.Run()
	result := CommandResult{Stdout: stdout.buf.Bytes(), StderrTail: lastLine(string(stderr.buf))}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	case errors.Is(err, exec.ErrWaitDelay):
		return result, nil // the process succeeded; a child kept the pipes open
	}
	return result, err
}

// lastLine is the last non-empty line, cut to maxStderrTail bytes on a rune boundary.
func lastLine(text string) string {
	text = strings.ToValidUTF8(strings.TrimSpace(text), "")
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		text = strings.TrimSpace(text[i+1:])
	}
	if len(text) > maxStderrTail {
		cut := maxStderrTail
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text
}
