package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/smithy-go"
	"github.com/coder/websocket"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/execbridge"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// WebSocket close codes of the terminal protocol (private range 4400-4599).
const (
	wsCloseNormal      = websocket.StatusNormalClosure
	wsCloseBadRequest  = websocket.StatusCode(4400)
	wsCloseCSRFInvalid = websocket.StatusCode(4401)
	wsCloseNotFound    = websocket.StatusCode(4404)
	wsCloseIdleTimeout = websocket.StatusCode(4408)
	wsCloseRefused     = websocket.StatusCode(4409)
	wsCloseMaxDuration = websocket.StatusCode(4410)
	wsCloseAWSError    = websocket.StatusCode(4502)
	wsCloseInternal    = websocket.StatusInternalError

	execDefaultCols     = 80
	execDefaultRows     = 24
	execMaxTerminalSize = 1000
	// execReadLimit bounds one browser frame; keystrokes and pastes are small.
	execReadLimit = 1 << 20
)

// durationText formats a session length as "7s", "4m 12s" or "1h 02m 05s".
func durationText(d time.Duration) string {
	total := int64(d / time.Second)
	hours, minutes, seconds := total/3600, total%3600/60, total%60
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %02dm %02ds", hours, minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

type execClientMessage struct {
	Type string `json:"type"`
	CSRF string `json:"csrf"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type execServerStatus struct {
	Type       string `json:"type"`
	State      string `json:"state"`
	SessionID  string `json:"session_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
	DurationMS *int64 `json:"duration_ms,omitempty"`
}

type execServerError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// execSocket wraps the browser socket; the first close or reject wins.
type execSocket struct {
	conn *websocket.Conn
	once sync.Once
}

func (x *execSocket) sendJSON(ctx context.Context, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return x.conn.Write(ctx, websocket.MessageText, raw)
}

// close sends an optional last text frame, then closes with code. Only the first call acts.
func (x *execSocket) close(code websocket.StatusCode, frame any) {
	x.once.Do(func() {
		if frame != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = x.sendJSON(ctx, frame)
			cancel()
		}
		_ = x.conn.Close(code, "")
	})
}

func (x *execSocket) reject(code websocket.StatusCode, errCode, message string) {
	x.close(code, execServerError{Type: "error", Code: errCode, Message: message})
}

// originPatterns allows the host of the configured base URL; coder/websocket already
// accepts same-host and absent Origin headers.
func (s *Server) originPatterns() []string {
	if u, err := url.Parse(s.Cfg.BaseURL); err == nil && u.Host != "" {
		return []string{u.Host}
	}
	return nil
}

// execWS answers GET /api/tasks/{taskId}/exec as a WebSocket bridging the browser to an ECS Exec
// session. Only cookie auth (middleware) and Origin are checked before the upgrade; every other
// refusal is an error frame plus a 44xx close so the browser can show it.
func (s *Server) execWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.originPatterns()})
	if err != nil {
		return // Accept already wrote the HTTP error (403 for a foreign Origin)
	}
	conn.SetReadLimit(execReadLimit)
	sock := &execSocket{conn: conn}
	defer conn.CloseNow()

	start, ok := s.readExecStart(r.Context(), sock)
	if !ok {
		return
	}
	s.runExec(r, sock, start)
}

// readExecStart waits for the first frame and validates it as a start message with the CSRF token.
func (s *Server) readExecStart(ctx context.Context, sock *execSocket) (execClientMessage, bool) {
	timer := time.AfterFunc(s.ExecStartTimeout, func() {
		sock.reject(wsCloseBadRequest, "bad_request", "The first message must be a start message.")
	})
	typ, data, err := sock.conn.Read(ctx)
	if !timer.Stop() || err != nil {
		return execClientMessage{}, false
	}
	var msg execClientMessage
	if typ != websocket.MessageText || json.Unmarshal(data, &msg) != nil || msg.Type != "start" {
		sock.reject(wsCloseBadRequest, "bad_request", "The first message must be a start message.")
		return execClientMessage{}, false
	}
	session, ok := auth.SessionFrom(ctx)
	if !ok || subtle.ConstantTimeCompare([]byte(msg.CSRF), []byte(session.CSRFToken)) != 1 {
		sock.reject(wsCloseCSRFInvalid, "csrf_invalid", "The CSRF token is not valid.")
		return execClientMessage{}, false
	}
	msg.Cols, msg.Rows = clampTerminalSize(msg.Cols, msg.Rows)
	return msg, true
}

func clampTerminalSize(cols, rows int) (int, int) {
	norm := func(v, fallback int) int {
		switch {
		case v <= 0:
			return fallback
		case v > execMaxTerminalSize:
			return execMaxTerminalSize
		}
		return v
	}
	return norm(cols, execDefaultCols), norm(rows, execDefaultRows)
}

// runExec resolves and authorises the target, opens the session and pumps both directions.
func (s *Server) runExec(r *http.Request, sock *execSocket, start execClientMessage) {
	ctx := r.Context()
	_ = sock.sendJSON(ctx, execServerStatus{Type: "status", State: "connecting"})

	container := r.URL.Query().Get("container")
	command := r.URL.Query().Get("command")

	account, region, arn, err := model.ParseTaskID(r.PathValue("taskId"))
	if err != nil {
		sock.reject(wsCloseNotFound, "not_found", "Task not found.")
		return
	}
	task, err := s.Store.GetTask(ctx, account, region, arn)
	if errors.Is(err, store.ErrNotFound) {
		sock.reject(wsCloseNotFound, "not_found", "Task not found.")
		return
	}
	if err != nil {
		s.execInternalError(sock, "load task", err)
		return
	}
	tags, err := s.tags(r)
	if err != nil {
		s.execInternalError(sock, "load tags", err)
		return
	}
	subject := auth.SubjectFrom(ctx)
	tag := TaskTag(tags, task)

	if task.LastStatus != "RUNNING" || !authz.Can(subject, authz.View, tag) {
		sock.reject(wsCloseNotFound, "not_found", "Task not found.")
		return
	}
	if !authz.Can(subject, authz.Terminal, tag) {
		name := "this tag"
		if t, err := s.Store.GetTag(ctx, tag); err == nil {
			name = t.Name
		}
		sock.reject(wsCloseRefused, "forbidden", "You need Terminal on "+name+" to open a terminal.")
		return
	}
	if !slices.Contains(ExecCommands, command) {
		sock.reject(wsCloseRefused, "invalid_command", "Command must be /bin/sh or /bin/bash.")
		return
	}
	if !slices.ContainsFunc(task.Containers, func(c model.Container) bool { return c.Name == container }) {
		sock.reject(wsCloseNotFound, "not_found", "Container not found.")
		return
	}

	enabled := false
	if task.ServiceName == "" {
		enabled = execEnabledForStandalone(task)
	} else {
		svc, err := s.Store.GetService(ctx, model.ServiceKey{ClusterKey: task.Cluster, Service: task.ServiceName})
		switch {
		case err == nil:
			enabled = ExecEnabled(svc)
		case !errors.Is(err, store.ErrNotFound):
			s.execInternalError(sock, "load service", err)
			return
		}
	}
	if !enabled {
		what := "this standalone task (it was not started with ECS Exec enabled)"
		if task.ServiceName != "" {
			what = task.ServiceName
		}
		sock.reject(wsCloseRefused, "exec_disabled", "ECS Exec is off for "+what+".")
		return
	}
	if ExecAgentStatus(task, container) != "RUNNING" {
		sock.reject(wsCloseRefused, "agent_not_running", "The ExecuteCommandAgent is not running in container "+container+".")
		return
	}

	release, ok := s.ExecLimiter.Acquire(subject.UserID)
	if !ok {
		sock.reject(wsCloseRefused, "session_limit",
			fmt.Sprintf("You already have %d open terminal sessions.", s.Cfg.ExecMaxSessionsPerUser))
		return
	}
	defer release()

	sess, err := execbridge.Open(ctx, execbridge.Options{
		Clients:             s.Clients,
		IdleTimeout:         s.Cfg.ExecIdleTimeout,
		MaxDuration:         s.Cfg.ExecMaxDuration,
		AllowInsecureStream: s.ExecAllowInsecureStream,
		Logger:              s.log(),
	}, execbridge.Target{
		AccountID: task.Cluster.AccountID, Region: task.Cluster.Region, Cluster: task.Cluster.Cluster,
		TaskARN: task.ARN, Container: container, Command: command,
	})
	if err != nil {
		s.log().Warn("exec: open session", "error", err)
		sock.reject(wsCloseAWSError, "aws_error", awsMessage(err))
		return
	}
	defer sess.Close(execbridge.CloseByClient) // no-op once the session has ended on its own

	// Open blocked until the handshake; the start size goes out now rather than waiting for a resize.
	_ = sess.Resize(ctx, uint32(start.Cols), uint32(start.Rows))

	ops := execOperations{s: s, subject: subject, task: task, container: container, command: command, sessionID: sess.ID()}
	opened := ops.append(ctx, "Opened terminal session on task "+shortTaskID(task.ARN)+" ("+container+", "+command+").", nil)
	_ = sock.sendJSON(ctx, execServerStatus{Type: "status", State: "connected", SessionID: sess.ID()})

	// Session -> browser. It owns the final frames and the close; the loop below only feeds input.
	var closing execbridge.Event
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		closing = s.pumpSessionEvents(sock, sess)
	}()

	// Browser -> session; any read error means the browser is gone or misbehaving.
	s.pumpBrowserInput(ctx, sock, sess)
	sess.Close(execbridge.CloseByClient)
	<-pumped

	if opened {
		ops.logClosed(sess, closing.Reason)
	}
}

func (s *Server) execInternalError(sock *execSocket, what string, err error) {
	s.log().Error("exec: "+what, "error", err)
	sock.reject(wsCloseInternal, "internal", "Something went wrong on our side.")
}

// shortTaskID is the first 12 characters of the task id (the last ARN segment).
func shortTaskID(taskARN string) string {
	id := taskARN[strings.LastIndex(taskARN, "/")+1:]
	return id[:min(12, len(id))]
}

func awsMessage(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorMessage() != "" {
		return apiErr.ErrorMessage()
	}
	return "SSM session ended unexpectedly."
}

// pumpSessionEvents forwards output to the browser until the session ends, then sends the final
// status or error frame and closes the socket. It returns the closing event.
func (s *Server) pumpSessionEvents(sock *execSocket, sess *execbridge.Session) execbridge.Event {
	for ev := range sess.Events() {
		switch ev.Kind {
		case execbridge.EventOutput:
			writeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := sock.conn.Write(writeCtx, websocket.MessageBinary, ev.Data)
			cancel()
			if err != nil {
				sess.Close(execbridge.CloseByClient) // the browser stopped reading
			}
		case execbridge.EventClosed:
			duration := sess.Duration().Milliseconds()
			status := execServerStatus{Type: "status", State: "closed", Reason: string(ev.Reason), SessionID: sess.ID(), DurationMS: &duration}
			switch ev.Reason {
			case execbridge.CloseIdle:
				sock.close(wsCloseIdleTimeout, status)
			case execbridge.CloseMaxDuration:
				sock.close(wsCloseMaxDuration, status)
			case execbridge.CloseSSMError:
				if ev.Err != nil {
					s.log().Warn("exec: ssm session failed", "session", sess.ID(), "error", ev.Err)
				}
				sock.reject(wsCloseAWSError, "aws_error", "SSM session ended unexpectedly.")
			default:
				sock.close(wsCloseNormal, status)
			}
			return ev
		}
	}
	return execbridge.Event{}
}

// pumpBrowserInput applies browser frames until the socket fails: binary frames are keystrokes,
// text frames are resize messages.
func (s *Server) pumpBrowserInput(ctx context.Context, sock *execSocket, sess *execbridge.Session) {
	for {
		typ, data, err := sock.conn.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			if sess.Write(ctx, data) != nil {
				return
			}
		case websocket.MessageText:
			var msg execClientMessage
			if json.Unmarshal(data, &msg) != nil || msg.Type != "resize" {
				sock.reject(wsCloseBadRequest, "bad_request", "Unknown message type.")
				return
			}
			cols, rows := clampTerminalSize(msg.Cols, msg.Rows)
			if sess.Resize(ctx, uint32(cols), uint32(rows)) != nil {
				return
			}
		}
	}
}

// execOperations writes the open and close rows of one terminal session. It never records input.
type execOperations struct {
	s                             *Server
	subject                       authz.Subject
	task                          model.Task
	container, command, sessionID string
}

// append writes one row and reports whether it was stored.
func (o execOperations) append(ctx context.Context, text string, extra map[string]any) bool {
	details := map[string]any{
		"task_arn": o.task.ARN, "container": o.container, "command": o.command, "session_id": o.sessionID,
	}
	for k, v := range extra {
		details[k] = v
	}
	var service *string
	if o.task.ServiceName != "" {
		service = &o.task.ServiceName
	}
	actor := o.subject.UserID
	err := o.s.Store.AppendOperation(ctx, store.Operation{
		AccountID: o.task.Cluster.AccountID, Region: o.task.Cluster.Region, Cluster: o.task.Cluster.Cluster,
		Service: service, ActorUserID: &actor, Source: "harbormaster", Text: text, Details: details,
	})
	if err != nil {
		o.s.log().Error("exec: append operation", "error", err)
	}
	return err == nil
}

func (o execOperations) logClosed(sess *execbridge.Session, reason execbridge.CloseReason) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	duration := sess.Duration()
	o.append(ctx, "Closed terminal session on task "+shortTaskID(o.task.ARN)+" ("+o.container+") after "+durationText(duration)+".",
		map[string]any{"duration_ms": duration.Milliseconds(), "reason": string(reason)})
}
