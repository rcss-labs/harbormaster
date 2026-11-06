package api

import (
	"cmp"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/logs"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Close codes of the live tail socket (the browser Logs tab switches on these).
const (
	closeBadStart      websocket.StatusCode = 4400
	closeCSRFInvalid   websocket.StatusCode = 4401
	closeForbidden     websocket.StatusCode = 4403
	closeNotFound      websocket.StatusCode = 4404
	closeIdle          websocket.StatusCode = 4408
	closeTailLimit     websocket.StatusCode = 4409
	closeInvalid       websocket.StatusCode = 4422
	closeAWSError      websocket.StatusCode = 4502
	tailStartTimeout                        = 5 * time.Second
	defaultTailIdle                         = 30 * time.Minute
	defaultHistoryRows                      = 500
)

// LogContainerDTO describes how one container logs. Reason is "driver" (not awslogs) or
// "other_group" (awslogs, but to a different group than the one being shown).
type LogContainerDTO struct {
	Name         string `json:"name"`
	Driver       string `json:"driver"`
	Supported    bool   `json:"supported"`
	StreamPrefix string `json:"stream_prefix,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type LogLineDTO struct {
	T         time.Time `json:"t"`
	Container string    `json:"container"`
	TaskID    string    `json:"task_id"`
	Message   string    `json:"message"`
}

// LogsDTO is the body of GET /api/services/{serviceId}/logs. Slices are never null.
type LogsDTO struct {
	Group       string            `json:"group"`
	Region      string            `json:"region"`
	Containers  []LogContainerDTO `json:"containers"`
	Lines       []LogLineDTO      `json:"lines"`
	OlderBefore *time.Time        `json:"older_before"`
	Truncated   bool              `json:"truncated"`
}

// logsSelection is what a request resolved to: where to read and which containers that covers.
// group is "" when no supported container was selected.
type logsSelection struct {
	accountID  string
	group      string
	region     string
	prefixes   []string
	containers []LogContainerDTO
}

// errLogsForbidden is a service the caller can see but may not read logs of.
type errLogsForbidden struct{ tag string }

func (e errLogsForbidden) Error() string { return "You need Logs on " + e.tag }

// resolveLogs authorizes the caller and picks the log group, region and stream prefixes from the
// primary task definition. container "" or "all" selects every supported container sharing the first
// supported container's group; a name selects that container (and its group).
func (s *Server) resolveLogs(r *http.Request, container string) (logsSelection, error) {
	key, err := serviceKeyFrom(r)
	if err != nil {
		return logsSelection{}, err
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		return logsSelection{}, err
	}
	ctx := r.Context()
	row, err := s.Store.GetServiceView(ctx, key, sc.Vis)
	if err != nil {
		return logsSelection{}, err
	}
	if !authz.Can(sc.Subject, authz.Logs, row.EffectiveTagID) {
		tags, err := s.tagDTOs(ctx)
		if err != nil {
			return logsSelection{}, err
		}
		return logsSelection{}, errLogsForbidden{tag: tags[row.EffectiveTagID].Name}
	}

	taskDefARN := row.TaskDefinitionARN
	for _, d := range row.Deployments {
		if d.Status == "PRIMARY" {
			taskDefARN = d.TaskDefinitionARN
			break
		}
	}
	td, err := s.Store.GetTaskDefinition(ctx, key.AccountID, key.Region, taskDefARN)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return logsSelection{}, err
	}
	configs, err := logs.ConfigsFromTaskDefinition(td)
	if err != nil {
		return logsSelection{}, err
	}

	all := container == "" || container == "all"
	selected := -1
	for i, c := range configs {
		if (all && c.Supported()) || (!all && c.Container == container) {
			selected = i
			break
		}
	}
	if !all && selected < 0 {
		return logsSelection{}, invalid("Unknown container " + container + ".")
	}

	sel := logsSelection{accountID: key.AccountID, containers: make([]LogContainerDTO, 0, len(configs)), prefixes: []string{}}
	// anchorGroup is the group the container list is judged against: the selected container's, or
	// (when the selected one is unsupported) the first supported container's.
	anchorGroup := ""
	if selected >= 0 && configs[selected].Supported() {
		sel.group = configs[selected].Group
		sel.region = cmp.Or(configs[selected].Region, key.Region)
		anchorGroup = sel.group
	} else {
		for _, c := range configs {
			if c.Supported() {
				anchorGroup = c.Group
				break
			}
		}
	}
	for i, c := range configs {
		dto := LogContainerDTO{Name: c.Container, Driver: c.Driver}
		switch {
		case !c.Supported():
			dto.Reason = "driver"
		case c.Group != anchorGroup:
			dto.Reason = "other_group"
		default:
			dto.Supported = true
			dto.StreamPrefix = logs.StreamPrefix(c)
			if sel.group != "" && (all || i == selected) {
				sel.prefixes = append(sel.prefixes, dto.StreamPrefix)
			}
		}
		sel.containers = append(sel.containers, dto)
	}
	return sel, nil
}

// minTaskIDChars is the shortest task id prefix the logs endpoint accepts (the drawer shows 8).
const minTaskIDChars = 8

// restrictToTask narrows the selected stream prefixes to one task of the service. The task is
// named by the leading characters of its id; it is resolved against the service's stored tasks, so
// a prefix that matches no task (or, absurdly, two) is "not found" rather than a wider read. A
// stream is "<prefix>/<container>/<task id>", so "<container prefix><task id>" selects exactly
// that task's stream of each selected container.
func (s *Server) restrictToTask(ctx context.Context, sel *logsSelection, key model.ServiceKey, short string) error {
	if len(short) < minTaskIDChars {
		return invalid(fmt.Sprintf("task must be at least %d characters.", minTaskIDChars))
	}
	rows, _, err := s.Store.ListTasks(ctx, store.TaskQuery{
		TargetFilter: store.TargetFilter{AccountID: key.AccountID, Region: key.Region},
		Vis:          store.Visibility{Admin: true}, // the caller was authorized on the service by resolveLogs
		Cluster:      key.Cluster, Service: key.Service,
	})
	if err != nil {
		return err
	}
	var taskIDs []string
	for _, row := range rows {
		id := row.ARN[strings.LastIndex(row.ARN, "/")+1:]
		if strings.HasPrefix(id, short) {
			taskIDs = append(taskIDs, id)
		}
	}
	if len(taskIDs) != 1 {
		return store.ErrNotFound
	}
	for i, prefix := range sel.prefixes {
		sel.prefixes[i] = prefix + taskIDs[0]
	}
	return nil
}

// getLogs answers GET /api/services/{serviceId}/logs.
func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sel, err := s.resolveLogs(r, q.Get("container"))
	var forbidden errLogsForbidden
	if errors.As(err, &forbidden) {
		forbiddenAction(w, authz.Logs, forbidden.tag)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	var before time.Time
	if raw := q.Get("before"); raw != "" {
		if before, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			s.fail(w, invalid("before must be an RFC 3339 timestamp."))
			return
		}
	}
	// limit narrows the page (the task drawer wants the newest 20 lines); it never exceeds the configured history size.
	limit := cmp.Or(s.Cfg.LogHistoryLimit, defaultHistoryRows)
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			s.fail(w, invalid("limit must be a positive whole number."))
			return
		}
		limit = min(limit, n)
	}
	key, _ := serviceKeyFrom(r)
	if short := q.Get("task"); short != "" {
		if err := s.restrictToTask(r.Context(), &sel, key, short); err != nil {
			s.fail(w, err)
			return
		}
	}
	out := LogsDTO{Group: sel.group, Region: sel.region, Containers: sel.containers, Lines: []LogLineDTO{}}
	if sel.group == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	page, err := s.Logs.History(r.Context(), awsx.Target{AccountID: key.AccountID, Region: key.Region}, logs.HistoryRequest{
		Group: sel.group, Region: sel.region, Prefixes: sel.prefixes, Before: before, Query: q.Get("q"),
		Limit: limit,
	})
	if err != nil {
		s.fail(w, err)
		return
	}
	out.Lines = logLineDTOs(page.Lines)
	out.OlderBefore = page.OlderBefore
	out.Truncated = page.Truncated
	writeJSON(w, http.StatusOK, out)
}

func logLineDTOs(lines []logs.Line) []LogLineDTO {
	out := make([]LogLineDTO, 0, len(lines))
	for _, l := range lines {
		out = append(out, LogLineDTO{T: l.T, Container: l.Container, TaskID: l.TaskID, Message: l.Message})
	}
	return out
}

type tailFrame struct {
	Type string `json:"type"`
	CSRF string `json:"csrf"`
}

// closeWith ends the socket with a close code; the reason is the short machine/human text the tab shows.
func closeWith(conn *websocket.Conn, code websocket.StatusCode, reason string) {
	_ = conn.Close(code, truncateCloseReason(reason))
}

// maxCloseReasonBytes is the WebSocket control-frame payload limit (125) minus the 2-byte code.
const (
	maxCloseReasonBytes = 123
	defaultTailsPerUser = 2 // used when Cfg.LogTailsPerUser is unset
)

// truncateCloseReason cuts reason to the close-frame limit on a UTF-8 rune boundary;
// a longer reason makes the library drop the whole frame (browser sees 1006).
func truncateCloseReason(reason string) string {
	if len(reason) <= maxCloseReasonBytes {
		return reason
	}
	cut := maxCloseReasonBytes
	for cut > 0 && !utf8.RuneStart(reason[cut]) {
		cut--
	}
	return reason[:cut]
}

// tailLogs serves GET /api/services/{serviceId}/logs/tail as a WebSocket.
// Protocol: client sends {"type":"start","csrf":...} within 5 s; the server answers
// {"type":"ready","group","prefixes"} then {"type":"lines","items":[...]} batches; the client may send
// {"type":"keepalive"}; the server ends with {"type":"end","reason":"idle|error|closed","message"} and
// a close frame (1000, 4408 or 4502).
func (s *Server) tailLogs(w http.ResponseWriter, r *http.Request) {
	// Same Origin policy as the exec socket: same host or the configured base URL's host; anything else gets a 403.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.originPatterns()})
	if err != nil {
		return
	}
	defer conn.CloseNow()

	// The socket context ends when the handler returns; every exit path cancels the tail with it.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	start, ok := readTailStart(ctx, conn)
	if !ok {
		closeWith(conn, closeBadStart, "bad_start")
		return
	}
	session, haveSession := auth.SessionFrom(r.Context())
	if !haveSession || session.CSRFToken == "" || subtle.ConstantTimeCompare([]byte(start.CSRF), []byte(session.CSRFToken)) != 1 {
		closeWith(conn, closeCSRFInvalid, "csrf_invalid")
		return
	}

	sel, err := s.resolveLogs(r, r.URL.Query().Get("container"))
	var forbidden errLogsForbidden
	var apiErr *APIError
	switch {
	case errors.As(err, &forbidden):
		closeWith(conn, closeForbidden, forbidden.Error())
		return
	case errors.Is(err, store.ErrNotFound):
		closeWith(conn, closeNotFound, "not_found")
		return
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		closeWith(conn, closeNotFound, "not_found")
		return
	case errors.As(err, &apiErr):
		closeWith(conn, closeInvalid, apiErr.Message)
		return
	case err != nil:
		s.log().Error("resolve log tail", "error", err)
		closeWith(conn, closeAWSError, "aws_error")
		return
	}
	if sel.group == "" {
		closeWith(conn, closeInvalid, "This service has no container that logs to CloudWatch (awslogs).")
		return
	}

	user, _ := auth.UserFrom(r.Context())
	release, acquired := s.TailLimiter.Acquire(user.ID)
	if !acquired {
		closeWith(conn, closeTailLimit, fmt.Sprintf("You already have %d live tails open.", cmp.Or(s.Cfg.LogTailsPerUser, defaultTailsPerUser)))
		return
	}
	defer release()

	key, _ := serviceKeyFrom(r)
	tail, err := s.Logs.StartTail(ctx, awsx.Target{AccountID: key.AccountID, Region: key.Region}, logs.TailRequest{
		AccountID: sel.accountID, Group: sel.group, Region: sel.region, Prefixes: sel.prefixes, Query: r.URL.Query().Get("q"),
	}, cmp.Or(s.Cfg.LogTailIdle, defaultTailIdle))
	if err != nil {
		s.log().Warn("start live tail", "error", err)
		closeWith(conn, closeAWSError, "aws_error")
		return
	}
	defer tail.Close()

	if err := writeJSONFrame(ctx, conn, map[string]any{"type": "ready", "group": sel.group, "prefixes": sel.prefixes}); err != nil {
		return
	}

	// One goroutine reads client frames; any read error (including the client closing) ends the tail.
	clientGone := make(chan struct{})
	go func() {
		defer close(clientGone)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var frame tailFrame
			if json.Unmarshal(data, &frame) == nil && frame.Type == "keepalive" {
				tail.Touch()
			}
		}
	}()

	for {
		select {
		case batch, open := <-tail.Lines():
			if !open {
				s.endTail(ctx, conn, tail)
				return
			}
			if err := writeJSONFrame(ctx, conn, map[string]any{"type": "lines", "items": logLineDTOs(batch)}); err != nil {
				return
			}
		case <-clientGone:
			return
		}
	}
}

// endTail reports why the tail ended (end frame) and closes the socket with the matching code.
func (s *Server) endTail(ctx context.Context, conn *websocket.Conn, tail *logs.Tail) {
	<-tail.Done()
	err := tail.Err()
	switch {
	case errors.Is(err, logs.ErrIdle):
		_ = writeJSONFrame(ctx, conn, map[string]any{"type": "end", "reason": "idle", "message": "Live tail stopped after being idle."})
		closeWith(conn, closeIdle, "idle")
	case err == nil || errors.Is(err, context.Canceled):
		_ = writeJSONFrame(ctx, conn, map[string]any{"type": "end", "reason": "closed", "message": "Live tail closed."})
		closeWith(conn, websocket.StatusNormalClosure, "closed")
	default:
		s.log().Warn("live tail ended", "error", err)
		_ = writeJSONFrame(ctx, conn, map[string]any{"type": "end", "reason": "error", "message": "The live tail stopped because AWS returned an error."})
		closeWith(conn, closeAWSError, "aws_error")
	}
}

// readTailStart reads the first frame, which must be a text {"type":"start"} within the start timeout.
func readTailStart(ctx context.Context, conn *websocket.Conn) (tailFrame, bool) {
	readCtx, cancel := context.WithTimeout(ctx, tailStartTimeout)
	defer cancel()
	typ, data, err := conn.Read(readCtx)
	if err != nil || typ != websocket.MessageText {
		return tailFrame{}, false
	}
	var frame tailFrame
	if json.Unmarshal(data, &frame) != nil || frame.Type != "start" {
		return tailFrame{}, false
	}
	return frame, true
}

func writeJSONFrame(ctx context.Context, conn *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, raw)
}
