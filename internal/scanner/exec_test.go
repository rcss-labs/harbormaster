package scanner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func runShell(t *testing.T, cmd OSCommand, ctx context.Context, script string, stdoutMax int64) (CommandResult, error, time.Duration) {
	t.Helper()
	start := time.Now()
	res, err := cmd.Run(ctx, "/bin/sh", []string{"-c", script}, []string{"PATH=/usr/bin:/bin"}, stdoutMax)
	return res, err, time.Since(start)
}

func TestOSCommand_TimeoutKillsTheWholeProcessGroupPromptly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// the shell waits on a child that also holds the output pipes
	_, err, took := runShell(t, OSCommand{WaitDelay: time.Second}, ctx, "sleep 30 & wait", 1024)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if took > 3*time.Second {
		t.Errorf("took %v, want a prompt kill", took)
	}
}

func TestOSCommand_ChildHoldingStdoutDoesNotBlockPastWaitDelay(t *testing.T) {
	res, err, took := runShell(t, OSCommand{WaitDelay: 300 * time.Millisecond}, context.Background(), "sleep 3 & echo hello", 1024)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("err = %v exit = %d, want a clean result", err, res.ExitCode)
	}
	if took > 2*time.Second {
		t.Errorf("took %v, want Run to return near the wait delay", took)
	}
	if !strings.Contains(string(res.Stdout), "hello") {
		t.Errorf("stdout = %q, want hello", res.Stdout)
	}
}

func TestOSCommand_StdoutIsCappedAndExitCodeReported(t *testing.T) {
	res, err, _ := runShell(t, OSCommand{}, context.Background(), "head -c 100000 /dev/zero; exit 3", 1000)
	if err != nil || res.ExitCode != 3 || len(res.Stdout) != 1000 {
		t.Errorf("err=%v exit=%d stdout=%d bytes, want nil, 3, 1000", err, res.ExitCode, len(res.Stdout))
	}
}

func TestOSCommand_StderrTailIsTheLastLine(t *testing.T) {
	res, err, _ := runShell(t, OSCommand{}, context.Background(),
		"i=0; while [ $i -lt 4000 ]; do echo 'noise noise noise noise noise noise noise noise'>&2; i=$((i+1)); done; echo 'the final line' >&2; exit 1", 1024)
	if err != nil || res.StderrTail != "the final line" {
		t.Errorf("err=%v tail=%q, want the last line after a lot of stderr", err, res.StderrTail)
	}
}

func TestOSCommand_StderrTailCutsOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", 300)
	res, _, _ := runShell(t, OSCommand{}, context.Background(), "echo '"+long+"' >&2; exit 1", 1024)
	if len(res.StderrTail) > maxStderrTail || !utf8.ValidString(res.StderrTail) || res.StderrTail == "" {
		t.Errorf("tail has %d bytes valid=%v, want at most %d bytes and valid UTF-8", len(res.StderrTail), utf8.ValidString(res.StderrTail), maxStderrTail)
	}
}
