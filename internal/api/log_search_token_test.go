package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/logs"
)

const gib = int64(1) << 30

// newTokenLogSearchEnv is newLogSearchEnv with access tokens on. The searcher's own cap is 10 GiB, as a session sees.
func newTokenLogSearchEnv(t *testing.T, tweak func(*config.Config), wrap ...func(awsx.Clients) awsx.Clients) *logSearchEnv {
	t.Helper()
	env := &logSearchEnv{}
	env.srv, env.api, env.fk, env.st, _ = newServerCustomAPI(t, true, withTokens(tweak, func(s *Server) {
		for _, w := range wrap {
			s.Clients = w(s.Clients)
		}
		s.LogSearch = &logs.Searcher{Clients: s.Clients, Now: s.Now, PollEvery: 5 * time.Millisecond, Timeout: 3 * time.Second, MaxBytes: 10 * gib}
	}))
	seedMockupTags(t, env.st)
	for _, u := range []string{"oncall", "omer", "dana"} {
		setPassword(t, env.st, u)
	}
	pollAll(t, env.st, env.fk)
	return env
}

// scanningWhileRunning makes every query report scanned bytes while it runs and never finish, so a search ends only by
// the scan cap or because the client went away.
type scanningWhileRunning struct {
	awsx.Clients
	bytes float64
}

func (s scanningWhileRunning) Logs(ctx context.Context, t awsx.Target) (awsx.LogsAPI, error) {
	api, err := s.Clients.Logs(ctx, t)
	if err != nil {
		return nil, err
	}
	return runningLogs{LogsAPI: api, bytes: s.bytes}, nil
}

type runningLogs struct {
	awsx.LogsAPI
	bytes float64
}

func (r runningLogs) GetQueryResults(context.Context, *cloudwatchlogs.GetQueryResultsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.GetQueryResultsOutput, error) {
	return &cloudwatchlogs.GetQueryResultsOutput{
		Status:     logstypes.QueryStatusRunning,
		Statistics: &logstypes.QueryStatistics{BytesScanned: r.bytes},
	}, nil
}

func scanning(bytes int64) func(awsx.Clients) awsx.Clients {
	return func(c awsx.Clients) awsx.Clients { return scanningWhileRunning{Clients: c, bytes: float64(bytes)} }
}

type tokenSearchBody struct {
	BytesScanned     int64 `json:"bytes_scanned"`
	ScanLimitBytes   int64 `json:"scan_limit_bytes"`
	ScanLimitReached bool  `json:"scan_limit_reached"`
}

func tokenUsage(t *testing.T, env *logSearchEnv, username string) int64 {
	t.Helper()
	user, err := env.st.GetUserByUsername(t.Context(), username)
	if err != nil {
		t.Fatal(err)
	}
	used, _, err := env.st.TokenLogScanUsage(t.Context(), user.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return used
}

func addTokenUsage(t *testing.T, env *logSearchEnv, username string, bytes int64) {
	t.Helper()
	user, err := env.st.GetUserByUsername(t.Context(), username)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.st.AddTokenLogScanUsage(t.Context(), user.ID, time.Now(), bytes); err != nil {
		t.Fatal(err)
	}
}

func TestTokenLogSearch_UsesTokenCap(t *testing.T) {
	env := newTokenLogSearchEnv(t, nil)
	token := tokenClient(t, env.srv, env.st, "sam")
	var viaToken, viaSession tokenSearchBody
	resp := token.post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token search: status %d", resp.StatusCode)
	}
	decode(t, resp, &viaToken)
	decode(t, login(t, env.srv, "sam", testPassword).post("/api/log-search", searchReq(paymentsID())), &viaSession)
	if viaToken.ScanLimitBytes != 2*gib {
		t.Errorf("token scan_limit_bytes = %d, want 2 GiB", viaToken.ScanLimitBytes)
	}
	if viaSession.ScanLimitBytes != 10*gib {
		t.Errorf("session scan_limit_bytes = %d, want 10 GiB", viaSession.ScanLimitBytes)
	}
}

func TestTokenLogSearch_GlobalCapLowersTheTokenCap(t *testing.T) {
	env := newTokenLogSearchEnv(t, func(cfg *config.Config) { cfg.LogSearchMaxGB = 1 })
	var body tokenSearchBody
	decode(t, tokenClient(t, env.srv, env.st, "sam").post("/api/log-search", searchReq(paymentsID())), &body)
	if body.ScanLimitBytes != gib {
		t.Errorf("scan_limit_bytes = %d, want 1 GiB (the global cap is below the token cap)", body.ScanLimitBytes)
	}
}

func TestTokenLogSearch_CapIsRemainingBudget(t *testing.T) {
	// A 1 GiB day with 0.5 GiB already used; queries that report 1 GiB scanned overrun any cap.
	env := newTokenLogSearchEnv(t, func(cfg *config.Config) { cfg.TokenLogSearchDailyGB = 1 }, scanning(gib))
	addTokenUsage(t, env, "sam", gib/2)
	c := tokenClient(t, env.srv, env.st, "sam")

	resp := c.post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first search: status %d", resp.StatusCode)
	}
	var first tokenSearchBody
	decode(t, resp, &first)
	if first.ScanLimitBytes != gib/2 || !first.ScanLimitReached {
		t.Fatalf("first search: scan_limit_bytes=%d reached=%v, want 0.5 GiB and reached", first.ScanLimitBytes, first.ScanLimitReached)
	}

	resp = c.post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second search: status %d, want 429", resp.StatusCode)
	}
	retry, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || retry < 1 || retry > 24*3600 {
		t.Errorf("Retry-After = %q, want seconds until 00:00 UTC", resp.Header.Get("Retry-After"))
	}
	code, msg := errorCode(t, resp)
	if code != "log_search_budget_exhausted" || msg != "Today's log search budget for access tokens is used up. It resets at 00:00 UTC." {
		t.Errorf("code %q message %q", code, msg)
	}
	// A session of the same user is not subject to the token budget.
	if r := login(t, env.srv, "sam", testPassword).post("/api/log-search", searchReq(paymentsID())); r.StatusCode != http.StatusOK {
		t.Errorf("session search after the budget is used up: status %d, want 200", r.StatusCode)
	}
}

func TestTokenLogSearch_ChargesScannedBytes(t *testing.T) {
	env := newTokenLogSearchEnv(t, nil)
	c := tokenClient(t, env.srv, env.st, "sam")
	var body tokenSearchBody
	decode(t, c.post("/api/log-search", searchReq(paymentsID())), &body)
	if body.BytesScanned <= 0 {
		t.Fatalf("bytes_scanned = %d, the fixture scanned nothing", body.BytesScanned)
	}
	if got := tokenUsage(t, env, "sam"); got != body.BytesScanned {
		t.Fatalf("usage = %d, want bytes_scanned %d", got, body.BytesScanned)
	}
	decode(t, c.post("/api/log-search", searchReq(paymentsID())), &body)
	if got := tokenUsage(t, env, "sam"); got != 2*body.BytesScanned {
		t.Errorf("usage after two searches = %d, want %d", got, 2*body.BytesScanned)
	}
	user, _ := env.st.GetUserByUsername(t.Context(), "sam")
	if _, searches, _ := env.st.TokenLogScanUsage(t.Context(), user.ID, time.Now()); searches != 2 {
		t.Errorf("searches = %d, want 2", searches)
	}
}

func TestTokenLogSearch_CancelledSearchStillCharged(t *testing.T) {
	// The query reports nothing scanned, and the search is dropped the moment it starts: no poll has seen a byte,
	// yet AWS bills the scan, so the reservation stays charged.
	env := newTokenLogSearchEnv(t, nil, scanning(0))
	c := tokenClient(t, env.srv, env.st, "sam")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := postWithBearer(ctx, c, searchReq(paymentsID())); err == nil {
			resp.Body.Close()
		}
	}()
	waitFor(t, "the search to start", func() bool { return env.fk.Calls()["StartQuery"] >= 1 })
	cancel()
	<-done
	waitFor(t, "the cancelled search to be settled", func() bool { return env.fk.Calls()["StopQuery"] >= 1 })
	time.Sleep(200 * time.Millisecond) // the settle runs after StopQuery
	if got := tokenUsage(t, env, "sam"); got != 2*gib {
		t.Errorf("usage = %d, want the 2 GiB reservation (a dropped search is charged what it could have scanned)", got)
	}
}

func TestTokenLogSearch_ReservesBudgetWhileRunning(t *testing.T) {
	env := newTokenLogSearchEnv(t, nil, scanning(0))
	c := tokenClient(t, env.srv, env.st, "sam")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		if resp, err := postWithBearer(ctx, c, searchReq(paymentsID())); err == nil {
			resp.Body.Close()
		}
	}()
	waitFor(t, "the reservation", func() bool { return tokenUsage(t, env, "sam") == 2*gib })
}

func TestTokenLogSearch_ReservationIsCappedByRemainingBudget(t *testing.T) {
	env := newTokenLogSearchEnv(t, func(cfg *config.Config) { cfg.TokenLogSearchDailyGB = 3 }, scanning(0))
	addTokenUsage(t, env, "sam", 5*gib/2) // 0.5 GiB left, below the 2 GiB per-search cap
	c := tokenClient(t, env.srv, env.st, "sam")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		if resp, err := postWithBearer(ctx, c, searchReq(paymentsID())); err == nil {
			resp.Body.Close()
		}
	}()
	waitFor(t, "the reservation", func() bool { return tokenUsage(t, env, "sam") == 3*gib })
}

// postWithBearer sends a log search as the token client that the caller can abandon.
func postWithBearer(ctx context.Context, c *apiClient, body any) (*http.Response, error) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/log-search", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.bearer)
	return c.http.Do(req)
}

func TestTokenLogSearch_SessionSearchIsNeverCharged(t *testing.T) {
	env := newTokenLogSearchEnv(t, nil)
	resp := login(t, env.srv, "sam", testPassword).post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := tokenUsage(t, env, "sam"); got != 0 {
		t.Errorf("a session search was charged %d bytes to the token budget", got)
	}
}

func TestTokenLogSearch_ZeroBudgetTurnsItOff(t *testing.T) {
	env := newTokenLogSearchEnv(t, func(cfg *config.Config) { cfg.TokenLogSearchDailyGB = 0 })
	resp := tokenClient(t, env.srv, env.st, "sam").post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, want 403", resp.StatusCode)
	}
	if code, msg := errorCode(t, resp); code != "forbidden" || msg != "Log search is turned off for access tokens." {
		t.Errorf("code %q message %q", code, msg)
	}
	if n := env.fk.Calls()["StartQuery"]; n != 0 {
		t.Errorf("StartQuery called %d times while the budget is 0", n)
	}
	if r := login(t, env.srv, "sam", testPassword).post("/api/log-search", searchReq(paymentsID())); r.StatusCode != http.StatusOK {
		t.Errorf("session search with the token budget at 0: status %d, want 200", r.StatusCode)
	}
}

func TestTokenLogSearch_AuditSaysViaToken(t *testing.T) {
	env := newTokenLogSearchEnv(t, nil)
	c := tokenClient(t, env.srv, env.st, "sam")
	c.post("/api/log-search", searchReq(paymentsID())).Body.Close()
	login(t, env.srv, "sam", testPassword).post("/api/log-search", searchReq(paymentsID())).Body.Close()
	var viaToken, plain int
	for _, e := range auditRows(t, env.st, "log_search") {
		if e.Details["via"] == "token" {
			viaToken++
			if id, _ := e.Details["token_id"].(string); len(id) != len("hmp_")+12 {
				t.Errorf("token_id = %q", id)
			}
		} else {
			plain++
			if _, has := e.Details["token_id"]; has {
				t.Error("session audit row carries a token id")
			}
		}
	}
	if viaToken != 1 || plain != 1 {
		t.Errorf("audit rows via token=%d session=%d, want 1 and 1", viaToken, plain)
	}
}

func TestTokenLogSearchEstimate_ReportsBudget(t *testing.T) {
	env := newTokenLogSearchEnv(t, nil)
	env.fk.Estate.SetLogGroupStoredBytes(paymentsGroup, 3*gib)
	addTokenUsage(t, env, "sam", 5*gib)
	var token struct {
		estimateBody
		BudgetBytes          *int64 `json:"budget_bytes"`
		BudgetRemainingBytes *int64 `json:"budget_remaining_bytes"`
	}
	decode(t, tokenClient(t, env.srv, env.st, "sam").post("/api/log-search/estimate", searchReq(paymentsID())), &token)
	if token.ScanLimitBytes != 2*gib || !token.ExceedsScanLimit {
		t.Errorf("token cap: limit=%d exceeds=%v, want 2 GiB and true for 3 GiB stored", token.ScanLimitBytes, token.ExceedsScanLimit)
	}
	if token.BudgetBytes == nil || *token.BudgetBytes != 20*gib || token.BudgetRemainingBytes == nil || *token.BudgetRemainingBytes != 15*gib {
		t.Errorf("budget = %v remaining = %v, want 20 GiB and 15 GiB", token.BudgetBytes, token.BudgetRemainingBytes)
	}

	var session map[string]any
	decode(t, login(t, env.srv, "sam", testPassword).post("/api/log-search/estimate", searchReq(paymentsID())), &session)
	if _, has := session["budget_bytes"]; has {
		t.Error("a session estimate carries budget_bytes")
	}
	if _, has := session["budget_remaining_bytes"]; has {
		t.Error("a session estimate carries budget_remaining_bytes")
	}
	if session["scan_limit_bytes"] != float64(10*gib) {
		t.Errorf("session scan_limit_bytes = %v, want 10 GiB", session["scan_limit_bytes"])
	}
}
