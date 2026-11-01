package execbridge_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/execbridge"
)

const waitLimit = 10 * time.Second

type rig struct {
	f      *fake.Fake
	srv    *fake.SSMServer
	target execbridge.Target
}

// newRig builds the seeded estate with an exec server and picks a running task of service.
func newRig(t *testing.T, so fake.SSMServerOptions, service string) *rig {
	t.Helper()
	f := fake.New(fake.MockupEstate(time.Now))
	f.ExecServer = fake.NewSSMServer(so)
	f.ExecServer.Start()
	t.Cleanup(f.ExecServer.Close)

	f.Estate.Mu.Lock()
	defer f.Estate.Mu.Unlock()
	account := f.Estate.Accounts[0].ID
	for _, task := range f.Estate.Tasks {
		if task.AccountID == account && aws.ToString(task.Task.Group) == "service:"+service &&
			aws.ToString(task.Task.LastStatus) == "RUNNING" {
			return &rig{f: f, srv: f.ExecServer, target: execbridge.Target{
				AccountID: account, Region: task.Region, Cluster: task.ClusterName,
				TaskARN: aws.ToString(task.Task.TaskArn), Container: aws.ToString(task.Task.Containers[0].Name),
				Command: "/bin/sh",
			}}
		}
	}
	t.Fatalf("no running task for %s", service)
	return nil
}

func (r *rig) options() execbridge.Options {
	return execbridge.Options{Clients: r.f, AllowInsecureStream: true, HandshakeTimeout: 2 * time.Second}
}

func (r *rig) open(t *testing.T, o execbridge.Options) *execbridge.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()
	s, err := execbridge.Open(ctx, o, r.target)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close(execbridge.CloseByClient) })
	return s
}

// collector drains a session's events.
type collector struct {
	mu     sync.Mutex
	output []byte
	closed *execbridge.Event
	events int
	done   chan struct{}
}

func collect(s *execbridge.Session) *collector {
	c := &collector{done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for ev := range s.Events() {
			c.mu.Lock()
			c.events++
			switch ev.Kind {
			case execbridge.EventOutput:
				c.output = append(c.output, ev.Data...)
			case execbridge.EventClosed:
				if c.closed != nil {
					panic("EventClosed delivered twice")
				}
				e := ev
				c.closed = &e
			}
			c.mu.Unlock()
		}
	}()
	return c
}

func (c *collector) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.output)
}

func (c *collector) waitText(t *testing.T, cond func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for time.Now().Before(deadline) {
		if txt := c.text(); cond(txt) {
			return txt
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met; output so far %q", c.text())
	return ""
}

func (c *collector) waitClosed(t *testing.T) execbridge.Event {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(waitLimit):
		t.Fatal("events channel not closed in time")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed == nil {
		t.Fatal("events channel closed without EventClosed")
	}
	return *c.closed
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitLimit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSession_Open_HandshakesAndReturnsID(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	s := r.open(t, r.options())
	if !strings.HasPrefix(s.ID(), "ecs-execute-command-") {
		t.Fatalf("ID() = %q, want ecs-execute-command- prefix", s.ID())
	}
	snap, ok := r.srv.Session(s.ID())
	if !ok || snap.OpenInput.ClientVersion != execbridge.ClientVersion {
		t.Fatalf("server saw %+v (known=%v), want ClientVersion %q", snap.OpenInput, ok, execbridge.ClientVersion)
	}
	if s.StartedAt().IsZero() {
		t.Fatal("StartedAt is zero")
	}
}

func TestSession_WriteAndReadThrough(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	s := r.open(t, r.options())
	c := collect(s)
	if err := s.Write(context.Background(), []byte("hostname\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	c.waitText(t, func(txt string) bool { return strings.Contains(txt, "ip-10-20-1-149.ec2.internal") })
}

func TestSession_Reorder_And_Duplicate_DeliverOnceInOrder(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{ReorderOnce: true, DuplicateOnce: true}, "payments-api")
	s := r.open(t, r.options())
	c := collect(s)
	prompt := r.target.Container + ":/app $ "
	c.waitText(t, func(txt string) bool { return txt == prompt })
	if err := s.Write(context.Background(), []byte("ls\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	want := prompt + "ls\r\n" + "bin  dev  etc  home  proc  sys  tmp  usr  var\r\n" + prompt
	got := c.waitText(t, func(txt string) bool { return len(txt) >= len(want) })
	// give any stray duplicate a moment to (wrongly) show up
	time.Sleep(100 * time.Millisecond)
	if got = c.text(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestSession_ResizeBeforeHandshake_IsSentAfterHandshake(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	// Open only returns once the handshake is done, so the earliest a caller can
	// resize is immediately after Open, racing the post-handshake flush.
	s := r.open(t, r.options())
	if err := s.Resize(context.Background(), 120, 32); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	eventually(t, "server to see size 120x32", func() bool {
		snap, _ := r.srv.Session(s.ID())
		return snap.LastSize.Cols == 120 && snap.LastSize.Rows == 32
	})
	if snap, _ := r.srv.Session(s.ID()); snap.SizeCount != 1 {
		t.Fatalf("SizeCount = %d, want exactly 1", snap.SizeCount)
	}
}

func TestSession_Resize_OnlyWhenChanged(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	s := r.open(t, r.options())
	c := collect(s)
	for i := 0; i < 2; i++ {
		if err := s.Resize(context.Background(), 80, 24); err != nil {
			t.Fatalf("Resize: %v", err)
		}
	}
	// A later keystroke round trip proves the earlier (in-order) size messages were processed.
	if err := s.Write(context.Background(), []byte("pwd\r")); err != nil {
		t.Fatal(err)
	}
	c.waitText(t, func(txt string) bool { return strings.Contains(txt, "/app\r\n") })
	if snap, _ := r.srv.Session(s.ID()); snap.SizeCount != 1 {
		t.Fatalf("SizeCount = %d after two identical Resize calls, want 1", snap.SizeCount)
	}
}

func TestSession_AgentExit_ClosesWithChannelClosedAndTerminates(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	s := r.open(t, r.options())
	c := collect(s)
	if err := s.Write(context.Background(), []byte("exit\r")); err != nil {
		t.Fatal(err)
	}
	ev := c.waitClosed(t)
	if ev.Reason != execbridge.CloseByAgent {
		t.Fatalf("Reason = %q, want %q", ev.Reason, execbridge.CloseByAgent)
	}
	select {
	case <-s.Done():
	case <-time.After(waitLimit):
		t.Fatal("Done() not closed")
	}
	if snap, _ := r.srv.Session(s.ID()); !snap.Terminated {
		t.Fatal("session not terminated on the SSM side after channel_closed")
	}
	if n := r.f.Calls()["TerminateSession"]; n != 1 {
		t.Fatalf("TerminateSession calls = %d, want 1", n)
	}
}

func TestSession_ClientClose_TerminatesAndIsIdempotent(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	s := r.open(t, r.options())
	c := collect(s)
	s.Close(execbridge.CloseByClient)
	s.Close(execbridge.CloseByClient)
	ev := c.waitClosed(t)
	if ev.Reason != execbridge.CloseByClient {
		t.Fatalf("Reason = %q, want client", ev.Reason)
	}
	if n := r.f.Calls()["TerminateSession"]; n != 1 {
		t.Fatalf("TerminateSession calls = %d, want 1", n)
	}
	if snap, _ := r.srv.Session(s.ID()); !snap.Terminated {
		t.Fatal("not terminated")
	}
	if err := s.Write(context.Background(), []byte("x")); err == nil {
		t.Fatal("Write after Close succeeded, want error")
	}
}

func TestSession_IdleTimeout_Closes(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	o := r.options()
	o.IdleTimeout = 200 * time.Millisecond
	s := r.open(t, o)
	c := collect(s)
	time.Sleep(100 * time.Millisecond)
	if err := s.Write(context.Background(), []byte("pwd\r")); err != nil {
		t.Fatal(err)
	}
	wrote := time.Now()
	ev := c.waitClosed(t)
	if ev.Reason != execbridge.CloseIdle {
		t.Fatalf("Reason = %q, want idle_timeout", ev.Reason)
	}
	if since := time.Since(wrote); since < 150*time.Millisecond {
		t.Fatalf("closed %v after the write, want the write to have postponed idle expiry by ~200ms", since)
	}
}

func TestSession_IdleTimeout_OutputDoesNotCount(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	o := r.options()
	o.IdleTimeout = 150 * time.Millisecond
	s := r.open(t, o)
	c := collect(s)
	if ev := c.waitClosed(t); ev.Reason != execbridge.CloseIdle {
		t.Fatalf("Reason = %q, want idle_timeout", ev.Reason)
	}
}

func TestSession_MaxDuration_ClosesEvenWhenActive(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	o := r.options()
	o.IdleTimeout = 5 * time.Second
	o.MaxDuration = 300 * time.Millisecond
	s := r.open(t, o)
	c := collect(s)
	go func() {
		for {
			select {
			case <-s.Done():
				return
			case <-time.After(10 * time.Millisecond):
				_ = s.Write(context.Background(), []byte("a"))
			}
		}
	}()
	if ev := c.waitClosed(t); ev.Reason != execbridge.CloseMaxDuration {
		t.Fatalf("Reason = %q, want max_duration", ev.Reason)
	}
	if d := s.Duration(); d < 250*time.Millisecond {
		t.Fatalf("Duration = %v, want >= ~300ms", d)
	}
}

func TestSession_UpstreamSocketDrop_ClosesWithSSMError(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	s := r.open(t, r.options())
	c := collect(s)
	if !r.srv.Terminate(s.ID()) {
		t.Fatal("server does not know the session")
	}
	ev := c.waitClosed(t)
	if ev.Reason != execbridge.CloseSSMError || ev.Err == nil {
		t.Fatalf("closed = reason %q err %v, want ssm_error with an error", ev.Reason, ev.Err)
	}
	if n := r.f.Calls()["TerminateSession"]; n != 1 {
		t.Fatalf("TerminateSession calls = %d, want 1", n)
	}
}

func TestSession_Open_ExecuteCommandError_IsOpenErrorStage(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "cert-renewer")
	_, err := execbridge.Open(context.Background(), r.options(), r.target)
	var oe *execbridge.OpenError
	if !errors.As(err, &oe) || oe.Stage != "execute_command" {
		t.Fatalf("err = %v, want *OpenError stage execute_command", err)
	}
	if n := r.f.Calls()["TerminateSession"]; n != 0 {
		t.Fatalf("TerminateSession calls = %d, want 0 (no session was created)", n)
	}
}

// evilClients makes ECS ExecuteCommand return a stream URL outside Amazon.
type evilClients struct{ *fake.Fake }

type evilECS struct{ awsx.ECSAPI }

func (c evilClients) ECS(ctx context.Context, tg awsx.Target) (awsx.ECSAPI, error) {
	api, err := c.Fake.ECS(ctx, tg)
	return evilECS{api}, err
}

func (e evilECS) ExecuteCommand(ctx context.Context, in *ecs.ExecuteCommandInput, opts ...func(*ecs.Options)) (*ecs.ExecuteCommandOutput, error) {
	out, err := e.ECSAPI.ExecuteCommand(ctx, in, opts...)
	if err == nil {
		out.Session = &ecstypes.Session{SessionId: out.Session.SessionId, TokenValue: out.Session.TokenValue,
			StreamUrl: aws.String("wss://evil.example/x")}
	}
	return out, err
}

func TestSession_Open_BadStreamURL_Terminates(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	o := r.options()
	o.Clients = evilClients{r.f}
	o.AllowInsecureStream = false
	_, err := execbridge.Open(context.Background(), o, r.target)
	var oe *execbridge.OpenError
	if !errors.As(err, &oe) || oe.Stage != "stream_url" || !errors.Is(err, execbridge.ErrStreamURLNotAllowed) {
		t.Fatalf("err = %v, want *OpenError stage stream_url wrapping ErrStreamURLNotAllowed", err)
	}
	if n := r.f.Calls()["TerminateSession"]; n != 1 {
		t.Fatalf("TerminateSession calls = %d, want 1", n)
	}
}

func TestSession_Open_HandshakeTimeout_Terminates(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{SilentAgent: true}, "payments-api")
	o := r.options()
	o.HandshakeTimeout = 300 * time.Millisecond
	_, err := execbridge.Open(context.Background(), o, r.target)
	var oe *execbridge.OpenError
	if !errors.As(err, &oe) || oe.Stage != "handshake" || !errors.Is(err, execbridge.ErrHandshakeTimeout) {
		t.Fatalf("err = %v, want *OpenError stage handshake wrapping ErrHandshakeTimeout", err)
	}
	if n := r.f.Calls()["TerminateSession"]; n != 1 {
		t.Fatalf("TerminateSession calls = %d, want 1", n)
	}
}

func TestSession_Duration_IsMeasured(t *testing.T) {
	r := newRig(t, fake.SSMServerOptions{}, "payments-api")
	s := r.open(t, r.options())
	c := collect(s)
	time.Sleep(30 * time.Millisecond)
	s.Close(execbridge.CloseByClient)
	c.waitClosed(t)
	d := s.Duration()
	if d < 20*time.Millisecond {
		t.Fatalf("Duration = %v, want >= 20ms", d)
	}
	time.Sleep(50 * time.Millisecond)
	if s.Duration() != d {
		t.Fatal("Duration keeps growing after Close")
	}
}
