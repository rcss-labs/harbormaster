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
type OSCommand struct{}

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

func (OSCommand) Run(ctx context.Context, name string, args []string, env []string, stdoutMax int64) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	stdout := &limitedBuffer{max: stdoutMax}
	stderr := &limitedBuffer{max: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr

	err := cmd.Run()
	result := CommandResult{Stdout: stdout.buf.Bytes(), StderrTail: lastLine(stderr.buf.String())}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, err
}

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	line := strings.TrimSpace(lines[len(lines)-1])
	if len(line) > maxStderrTail {
		line = line[:maxStderrTail]
	}
	return line
}
