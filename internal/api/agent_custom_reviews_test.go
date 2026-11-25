package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const customReviewText = "Compare the largest clusters.\nName the three that grew most."

// fakeKey has the shape of a provider key and is assembled so that no scanner takes this file for a leak.
var fakeKey = "AKIA" + "QWERTYUIOPASDFGH"

// newCustomEnv is the reviews environment with HM_AGENT_CUSTOM_REVIEWS on.
func newCustomEnv(t *testing.T) *reviewsEnv {
	t.Helper()
	e := newReviewsEnv(t, false)
	e.api.Cfg.AgentCustomReviews = true
	return e
}

func (e *reviewsEnv) custom(text string, mutate ...func(map[string]any)) map[string]any {
	return e.input(append([]func(map[string]any){with("name", "Cluster growth"), with("type", "custom"), with("instructions", text)}, mutate...)...)
}

func (e *reviewsEnv) storedReview(id int64) store.Review {
	e.t.Helper()
	r, err := e.st.GetReview(e.t.Context(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *reviewsEnv) auditCount() int {
	e.t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(e.t.Context(), `select count(*) from audit_log`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *reviewsEnv) runCount() int {
	e.t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(e.t.Context(), `select count(*) from agent_review_runs`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// expectRefused checks the status, code and field of a refusal and closes the body.
func expectRefused(t *testing.T, label string, resp *http.Response, code, field string) {
	t.Helper()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("%s: status %d, want 422", label, resp.StatusCode)
		resp.Body.Close()
		return
	}
	if gotCode, gotField := refused(t, resp); gotCode != code || gotField != field {
		t.Errorf("%s: code %q field %q, want %q and %q", label, gotCode, gotField, code, field)
	}
}

func wholeBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAgentReviews_CustomRoundTrip(t *testing.T) {
	e := newCustomEnv(t)
	created := e.create(e.custom(customReviewText))
	if created.Type != "custom" || created.Instructions != customReviewText || created.Version != 1 {
		t.Fatalf("created = %+v", created)
	}
	if got := e.firstReview(); got.Instructions != customReviewText {
		t.Errorf("listed instructions = %q", got.Instructions)
	}
	if got := e.storedReview(created.ID); got.Type != "custom" || got.Instructions != customReviewText {
		t.Errorf("stored = %+v", got)
	}
	// The longest text the limits allow: 2000 characters of four bytes each.
	long := strings.Repeat("\U0001F6A2", 2000)
	resp := e.update(created.ID, e.custom(long, with("version", 1)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update with the longest text: status %d", resp.StatusCode)
	}
	var updated ReviewDTO
	decode(t, resp, &updated)
	if updated.Instructions != long || updated.Version != 2 || e.storedReview(created.ID).Instructions != long {
		t.Errorf("updated: version %d, %d bytes", updated.Version, len(updated.Instructions))
	}
	if builtIn := e.create(e.input()); builtIn.Instructions != "" {
		t.Errorf("built-in instructions = %q, want none", builtIn.Instructions)
	}
	// Between built-in types a change stays possible.
	for _, r := range e.list() {
		if r.Type == "cost" {
			if resp := e.update(r.ID, e.input(with("version", r.Version), with("type", "capacity"))); resp.StatusCode != http.StatusOK {
				t.Errorf("cost to capacity: status %d", resp.StatusCode)
			}
		}
	}
}

// instructionProblems is a text for each problem code, with the marker in it where the problem allows any text.
func instructionProblems(marker string) []struct{ name, text, code string } {
	return []struct{ name, text, code string }{
		{"empty", "", "instructions_required"},
		{"white space", " \r\n\t ", "instructions_required"},
		{"2001 characters", marker + strings.Repeat("a", 2001-len(marker)), "instructions_too_long"},
		{"more than 8000 bytes", marker + strings.Repeat("\U0001F6A2", 2000), "instructions_too_long"},
		{"a NUL", marker + "\x00 look", "instructions_not_text"},
		{"a hidden character", marker + "\u200b look", "instructions_hidden"},
		{"a control character", marker + "\x1b look", "instructions_hidden"},
		{"a secret", marker + " use " + fakeKey + " to sign in", "instructions_secret"},
	}
}

func TestAgentReviews_InstructionProblemsTable(t *testing.T) {
	e := newCustomEnv(t)
	review := e.create(e.custom(customReviewText))
	audits, kicks := e.auditCount(), e.kicks.Load()
	for _, tc := range instructionProblems("Look") {
		expectRefused(t, "create, "+tc.name, e.admin.post("/api/agent/reviews", e.custom(tc.text, with("name", "Second"))), tc.code, "instructions")
		expectRefused(t, "update, "+tc.name, e.update(review.ID, e.custom(tc.text, with("version", 1))), tc.code, "instructions")
	}
	// A body without the member (an older browser bundle) clears nothing.
	missing := e.custom("", with("version", 1))
	delete(missing, "instructions")
	expectRefused(t, "update without the member", e.update(review.ID, missing), "instructions_required", "instructions")
	if rows := e.stored(); len(rows) != 1 || rows[0].Instructions != customReviewText || rows[0].Version != 1 {
		t.Errorf("stored = %+v, want the first text at version 1", rows)
	}
	if e.auditCount() != audits || e.kicks.Load() != kicks {
		t.Errorf("a refusal was audited (%d new) or kicked (%d new)", e.auditCount()-audits, e.kicks.Load()-kicks)
	}
	// What to do about a text taken for a secret is in the message, and that it would leave the installation.
	_, msg := errorCode(t, e.admin.post("/api/agent/reviews", e.custom("use "+fakeKey, with("name", "Second"))))
	for _, part := range []string{"model provider", "tag", "scope"} {
		if !strings.Contains(msg, part) {
			t.Errorf("secret message %q does not mention the %s", msg, part)
		}
	}
}

func TestAgentReviews_InstructionsAreCleanedBeforeStoring(t *testing.T) {
	e := newCustomEnv(t)
	const raw, clean = "\r\n  First line.\r\nSecond  line.\rThird line. \t\n", "First line.\nSecond  line.\nThird line."
	created := e.create(e.custom(raw))
	if created.Instructions != clean || e.storedReview(created.ID).Instructions != clean {
		t.Errorf("created %q, stored %q, want %q", created.Instructions, e.storedReview(created.ID).Instructions, clean)
	}
	resp := e.update(created.ID, e.custom(" Other text.\r\nMore. ", with("version", 1)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := e.storedReview(created.ID).Instructions; got != "Other text.\nMore." {
		t.Errorf("stored after the update = %q", got)
	}
	rows := settingsAuditRows(t, e.st, "agent_review_create")
	if after, _ := rows[0].Details["after"].(map[string]any); after["instructions"] != clean {
		t.Errorf("audited text = %q, want the cleaned one", after["instructions"])
	}
}

func TestAgentReviews_BuiltInTypeRefusesInstructions(t *testing.T) {
	e := newCustomEnv(t)
	review := e.create(e.input())
	audits := e.auditCount()
	for _, on := range []bool{true, false} {
		e.api.Cfg.AgentCustomReviews = on
		for _, typ := range []string{"cost", "security", "reliability", "capacity"} {
			expectRefused(t, "create "+typ, e.admin.post("/api/agent/reviews", e.input(with("name", "Second"), with("type", typ), with("instructions", "Look here."))),
				"instructions_not_allowed", "instructions")
			expectRefused(t, "update "+typ, e.update(review.ID, e.input(with("version", 1), with("type", typ), with("instructions", "Look here."))),
				"instructions_not_allowed", "instructions")
		}
	}
	if rows := e.stored(); len(rows) != 1 || rows[0].Version != 1 || rows[0].Instructions != "" || e.auditCount() != audits {
		t.Errorf("stored = %+v, %d new audit entries", rows, e.auditCount()-audits)
	}
	// White space is no text.
	if resp := e.update(review.ID, e.input(with("version", 1), with("instructions", " \r\n "))); resp.StatusCode != http.StatusOK {
		t.Errorf("white space on a built-in type: status %d, want 200", resp.StatusCode)
	}
}

func TestAgentReviews_TypeCannotChangeToOrFromCustom(t *testing.T) {
	e := newCustomEnv(t)
	builtIn, custom := e.create(e.input()), e.create(e.custom(customReviewText))
	audits := e.auditCount()
	expectRefused(t, "cost to custom", e.update(builtIn.ID, e.custom(customReviewText, with("name", "Weekly cost"), with("version", 1))), "type_fixed", "type")
	expectRefused(t, "cost to custom without text", e.update(builtIn.ID, e.custom("", with("name", "Weekly cost"), with("version", 1))), "type_fixed", "type")
	expectRefused(t, "custom to cost", e.update(custom.ID, e.input(with("name", "Cluster growth"), with("version", 1))), "type_fixed", "type")
	expectRefused(t, "custom to cost with the text", e.update(custom.ID, e.custom(customReviewText, with("type", "cost"), with("version", 1))), "type_fixed", "type")
	// Turning it off in the same request does not let the change through.
	expectRefused(t, "custom to cost, off", e.update(custom.ID, e.input(with("name", "Cluster growth"), with("version", 1), with("enabled", false))), "type_fixed", "type")
	if a, b := e.storedReview(builtIn.ID), e.storedReview(custom.ID); a.Type != "cost" || a.Version != 1 || b.Type != "custom" || b.Version != 1 || b.Instructions != customReviewText {
		t.Errorf("stored = %+v and %+v", a, b)
	}
	if e.auditCount() != audits {
		t.Error("a refused type change was audited")
	}
	// The store's own refusal (a change that raced the check) reads the same.
	if err := reviewStoreError(fmt.Errorf("save: %w", store.ErrReviewTypeFixed)); err == nil {
		t.Error("no error for the store's refusal")
	} else if fieldErr, ok := err.(*reviewFieldError); !ok || fieldErr.code != "type_fixed" || fieldErr.field != "type" {
		t.Errorf("store refusal maps to %#v", err)
	}
}

func TestAgentReviews_CustomWhileTheSwitchIsOff(t *testing.T) {
	e := newCustomEnv(t)
	e.setReviewerModel()
	custom := e.create(e.custom(customReviewText))
	builtIn := e.create(e.input())
	e.api.Cfg.AgentCustomReviews = false
	audits, kicks := e.auditCount(), e.kicks.Load()
	const code, field = "custom_reviews_off", "type"
	expectRefused(t, "create", e.admin.post("/api/agent/reviews", e.custom(customReviewText, with("name", "Second"))), code, field)
	expectRefused(t, "update", e.update(custom.ID, e.custom("Other text.", with("version", 1))), code, field)
	expectRefused(t, "update of the name", e.update(custom.ID, e.custom(customReviewText, with("name", "Renamed"), with("version", 1))), code, field)
	expectRefused(t, "turning it off", e.update(custom.ID, e.custom(customReviewText, with("enabled", false), with("version", 1))), code, field)
	expectRefused(t, "custom to cost", e.update(custom.ID, e.input(with("name", "Cluster growth"), with("version", 1))), code, field)
	expectRefused(t, "cost to custom", e.update(builtIn.ID, e.custom(customReviewText, with("name", "Weekly cost"), with("version", 1))), code, field)
	expectRefused(t, "run now", e.runNow(custom.ID), code, field)
	expectRefused(t, "test run", e.testRun(custom.ID), code, field)
	if e.auditCount() != audits || e.kicks.Load() != kicks || e.runCount() != 0 || e.storedReview(custom.ID).Version != 1 || len(e.stored()) != 2 {
		t.Errorf("a refusal wrote something: %d audits, %d kicks, %d runs", e.auditCount()-audits, e.kicks.Load()-kicks, e.runCount())
	}

	listed := map[string]ReviewDTO{}
	for _, r := range e.list() {
		listed[r.Type] = r
	}
	if listed["custom"].Instructions != customReviewText {
		t.Errorf("listed custom review = %+v, want it with its text", listed["custom"])
	}
	// Built-in reviews are not touched by the switch.
	if resp := e.testRun(builtIn.ID); resp.StatusCode != http.StatusAccepted {
		t.Errorf("test run of a built-in review: status %d", resp.StatusCode)
	}
	if resp := e.update(builtIn.ID, e.input(with("version", 1), with("name", "Renamed cost"))); resp.StatusCode != http.StatusOK {
		t.Errorf("update of a built-in review: status %d", resp.StatusCode)
	}
	if resp := e.admin.do(http.MethodDelete, fmt.Sprintf("/api/agent/reviews/%d", custom.ID), nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete: status %d, want 204", resp.StatusCode)
	}
}

func TestAgentSettings_OffersCustomOnlyWhenSwitchedOn(t *testing.T) {
	e := newReviewsEnv(t, false)
	read := func() AgentReviewsDTO {
		t.Helper()
		var out struct {
			Reviews AgentReviewsDTO `json:"reviews"`
		}
		decode(t, e.admin.get("/api/agent/settings"), &out)
		return out.Reviews
	}
	off := read()
	if off.CustomEnabled || off.MaxInstructionChars != 2000 || len(off.Types) != 4 || slices.Contains(off.SupportedTypes, "custom") {
		t.Errorf("switch off: %+v", off)
	}
	e.api.Cfg.AgentCustomReviews = true
	on := read()
	if !on.CustomEnabled || on.MaxInstructionChars != 2000 || len(on.Types) != 5 || on.Types[len(on.Types)-1] != (AgentReviewTypeDTO{ID: "custom", Label: "Custom"}) {
		t.Errorf("switch on: %+v, want custom as the last type", on)
	}
	if fmt.Sprint(on.SupportedTypes) != "[cost security reliability custom]" {
		t.Errorf("supported types = %v", on.SupportedTypes)
	}
}

func TestAgentReviews_AuditHoldsOldAndNewTextWithHashes(t *testing.T) {
	e := newCustomEnv(t)
	hash := func(text string) string {
		sum := sha256.Sum256([]byte(text))
		return hex.EncodeToString(sum[:])
	}
	side := func(row store.AuditEntry, key string) (text, sum any) {
		t.Helper()
		view, ok := row.Details[key].(map[string]any)
		if !ok {
			t.Fatalf("%s: no %q in %v", row.Action, key, row.Details)
		}
		return view["instructions"], view["instructions_sha256"]
	}
	const second = "Only the production clusters, please."
	review := e.create(e.custom(customReviewText))
	e.update(review.ID, e.custom(" "+second+"\r\n", with("version", 1))).Body.Close()
	e.admin.do(http.MethodDelete, fmt.Sprintf("/api/agent/reviews/%d", review.ID), nil, nil).Body.Close()
	for _, tc := range []struct{ action, key, text string }{
		{"agent_review_create", "after", customReviewText},
		{"agent_review_update", "before", customReviewText},
		{"agent_review_update", "after", second},
		{"agent_review_delete", "before", second},
	} {
		rows := settingsAuditRows(t, e.st, tc.action)
		if len(rows) != 1 {
			t.Fatalf("%s: %d rows, want 1", tc.action, len(rows))
		}
		if text, sum := side(rows[0], tc.key); text != tc.text || sum != hash(tc.text) {
			t.Errorf("%s %s: text %q hash %q, want %q and %q", tc.action, tc.key, text, sum, tc.text, hash(tc.text))
		}
		if strings.Contains(rows[0].Summary+rows[0].Target, "clusters") {
			t.Errorf("%s: summary or target holds the text: %q %q", tc.action, rows[0].Summary, rows[0].Target)
		}
	}
	// A built-in review has neither a text nor a hash.
	e.create(e.input())
	if text, sum := side(settingsAuditRows(t, e.st, "agent_review_create")[0], "after"); text != "" || sum != "" {
		t.Errorf("built-in: text %q hash %q, want both empty", text, sum)
	}
}

func TestAgentReviews_RefusedTextIsInNoLogAuditOrErrorBody(t *testing.T) {
	e := newCustomEnv(t)
	const marker = "ZEBRAMARKER"
	builtIn, custom := e.create(e.input()), e.create(e.custom(customReviewText))
	pager := e.emailRoute("Pager", "pagerduty")
	text := marker + " look"
	var bodies []string
	send := func(label string, resp *http.Response, status int) {
		t.Helper()
		if resp.StatusCode != status {
			t.Errorf("%s: status %d, want %d", label, resp.StatusCode, status)
		}
		bodies = append(bodies, label+": "+wholeBody(t, resp))
	}
	customPath := fmt.Sprintf("/api/agent/reviews/%d", custom.ID)
	for _, tc := range instructionProblems(marker) {
		send("create, "+tc.name, e.admin.post("/api/agent/reviews", e.custom(tc.text, with("name", "Second"))), 422)
		send("update, "+tc.name, e.update(custom.ID, e.custom(tc.text, with("version", 1))), 422)
		send("preview, "+tc.name, e.admin.post("/api/agent/reviews/preview", e.custom(tc.text)), 200)
	}
	send("preview of a good text", e.admin.post("/api/agent/reviews/preview", e.custom(text)), 200)
	send("text on a built-in type", e.admin.post("/api/agent/reviews", e.input(with("name", "Second"), with("instructions", text))), 422)
	send("type change", e.update(builtIn.ID, e.custom(text, with("name", "Weekly cost"), with("version", 1))), 422)
	send("stale version", e.update(custom.ID, e.custom(text, with("version", 7))), 409)
	send("name taken", e.admin.post("/api/agent/reviews", e.custom(text, with("name", "Weekly cost"))), 422)
	send("bad schedule", e.admin.post("/api/agent/reviews", e.custom(text, with("name", "Second"), withSchedule(map[string]any{"cadence": "hourly"}))), 422)
	send("bad route", e.admin.post("/api/agent/reviews", e.custom(text, with("name", "Second"), with("route_ids", []int64{pager.ID}))), 422)
	send("unknown member", e.admin.post("/api/agent/reviews", e.custom(text, with(marker, true))), 422)
	send("wrong kind", e.admin.post("/api/agent/reviews", e.custom("", with("instructions", []string{marker}))), 422)
	send("run with the text", e.admin.post(customPath+"/run", map[string]any{"instructions": text}), 422)
	e.api.Cfg.AgentCustomReviews = false
	send("switch off, create", e.admin.post("/api/agent/reviews", e.custom(text, with("name", "Second"))), 422)
	send("switch off, update", e.update(custom.ID, e.custom(text, with("version", 1))), 422)
	e.api.Cfg.AgentCustomReviews = true
	// The database refuses the row: the error is logged (which shows the log is captured) without the row's text.
	if _, err := e.st.Pool.Exec(t.Context(), `alter table agent_reviews add constraint drill check (instructions not like '%ZEBRA%')`); err != nil {
		t.Fatal(err)
	}
	send("refused by the database, create", e.admin.post("/api/agent/reviews", e.custom(text, with("name", "Second"))), 500)
	send("refused by the database, update", e.update(custom.ID, e.custom(text, with("version", 1))), 500)

	for _, body := range bodies {
		if strings.Contains(body, marker) || strings.Contains(body, fakeKey) {
			t.Errorf("a response holds the submitted text: %s", body)
		}
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "drill") {
		t.Fatalf("the log of the failed write was not captured: %q", logs)
	}
	var audited string
	if err := e.st.Pool.QueryRow(t.Context(), `select coalesce(string_agg(a::text, ' '), '') from audit_log a`).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"log": logs, "audit table": audited} {
		if strings.Contains(text, marker) || strings.Contains(text, fakeKey) {
			t.Errorf("the %s holds the submitted text", name)
		}
	}
	if strings.Contains(logs, "largest clusters") {
		t.Error("the log holds a saved text")
	}
}

func TestAgentReviews_InstructionEditKeepsNextRun(t *testing.T) {
	e := newCustomEnv(t)
	review := e.create(e.custom(customReviewText))
	before := e.storedReview(review.ID)
	resp := e.update(review.ID, e.custom("Other text.", with("version", 1)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update: status %d", resp.StatusCode)
	}
	var updated ReviewDTO
	decode(t, resp, &updated)
	after := e.storedReview(review.ID)
	if updated.NextRunAt == nil || review.NextRunAt == nil || !updated.NextRunAt.Equal(*review.NextRunAt) || updated.Version != 2 ||
		!after.ScheduleChangedAt.Equal(before.ScheduleChangedAt) || after.Instructions != "Other text." {
		t.Errorf("before %+v, after %+v, next run %v then %v", before, after, review.NextRunAt, updated.NextRunAt)
	}
}

// Turning a review off keeps what is stored; with an edited text it is a full save, never a silently dropped edit.
func TestAgentReviews_TurningOffWithAnEditedTextIsAFullSave(t *testing.T) {
	e := newCustomEnv(t)
	good, pager := e.emailRoute("Good", "email"), e.emailRoute("Soon pager", "email")
	setup := func(name string) ReviewDTO {
		t.Helper()
		return e.create(e.custom(customReviewText, with("name", name), with("route_ids", []int64{good.ID, pager.ID})))
	}
	off := func(r ReviewDTO, text string, routes ...int64) *http.Response {
		return e.update(r.ID, e.custom(text, with("name", r.Name), with("version", r.Version), with("enabled", false), with("route_ids", routes)))
	}
	edited := setup("Edited")
	resp := off(edited, "Other text.", good.ID, pager.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("off with another text: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := e.storedReview(edited.ID); got.Enabled || got.Instructions != "Other text." {
		t.Errorf("stored = %+v, want off with the new text", got)
	}

	plain := setup("Plain")
	if _, err := e.st.Pool.Exec(t.Context(), `update notification_routes set destination = 'pagerduty' where id = $1`, pager.ID); err != nil {
		t.Fatal(err)
	}
	expectRefused(t, "off with another text and a broken route", off(plain, "Other text.", good.ID, pager.ID), "route_not_supported", "route_ids")
	expectRefused(t, "off with a secret", off(plain, "use "+fakeKey, good.ID), "instructions_secret", "instructions")
	expectRefused(t, "off without a text", off(plain, "", good.ID), "instructions_required", "instructions")
	if got := e.storedReview(plain.ID); !got.Enabled || got.Instructions != customReviewText || got.Version != 1 {
		t.Errorf("stored = %+v, want it untouched", got)
	}
	// The same text, as a form may send it back, is no edit: the stored routes stay whatever they became.
	resp = off(plain, "  "+strings.ReplaceAll(customReviewText, "\n", "\r\n")+"\n", good.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("off with the same text: status %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := e.storedReview(plain.ID); got.Enabled || got.Instructions != customReviewText || len(got.RouteIDs) != 2 {
		t.Errorf("stored = %+v, want off with the stored text and routes", got)
	}
}

// Every route of the reviews API refuses everyone but an admin's session, and says nothing of a stored text.
func TestAgentReviews_AdminOnlyHidesInstructions(t *testing.T) {
	e := newCustomEnv(t)
	review := e.create(e.custom(customReviewText))
	runTable := func(label string, id int64, do func(method, path string) *http.Response, wantCode string) {
		t.Helper()
		for _, r := range reviewRouteTable {
			path := strings.Replace(r.path, "/reviews/1", fmt.Sprintf("/reviews/%d", id), 1)
			resp := do(r.method, path)
			body := wholeBody(t, resp)
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, wantCode) {
				t.Errorf("%s: %s %s: status %d body %s, want 403 %s", label, r.method, path, resp.StatusCode, body, wantCode)
			}
			if strings.Contains(body, "instructions") || strings.Contains(body, "largest clusters") {
				t.Errorf("%s: %s %s: the body holds instructions: %s", label, r.method, path, body)
			}
		}
	}
	as := func(c *apiClient) func(method, path string) *http.Response {
		return func(method, path string) *http.Response {
			return c.do(method, path, e.custom("Other text.", with("version", 1)), nil)
		}
	}
	runTable("a non-admin", review.ID, as(e.omer), "forbidden")

	srv, api, _, st, _ := newServerCustomAPI(t, true, withTokens(func(c *config.Config) {
		c.AgentURL, c.AgentRunTokenTTL, c.AgentCustomReviews = testAgentURL, 15*time.Minute, true
	}, nil))
	oauth := newOAuthAPIEnvWith(t, true, withAgent(nil))
	granted := oauth.connect(t, oauth.sam)
	seed := func(st *store.Store) int64 {
		t.Helper()
		route, err := st.CreateNotificationRoute(t.Context(), store.NotificationRoute{Name: "Ops mail", Destination: "email", MinSeverity: "warning",
			Kinds: []string{"service_failing"}, QuietTimezone: "UTC", Enabled: true}, 0)
		if err != nil {
			t.Fatal(err)
		}
		r, err := st.CreateReview(t.Context(), store.Review{Name: "Cluster growth", Type: "custom", Enabled: true, Cadence: "daily", Timezone: "UTC",
			RouteIDs: []int64{route.ID}, Instructions: customReviewText}, 0)
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	id := seed(st)
	runTable("an admin's personal token", id, as(tokenClient(t, srv, st, "sam")), "token_not_allowed")
	runTable("an admin's run token", id, as(runTokenClient(t, srv, api, st, "sam")), "token_not_allowed")
	runTable("an admin's OAuth token", seed(oauth.st), func(method, path string) *http.Response {
		return oauth.bearer(t, method, path, granted.AccessToken)
	}, "token_not_allowed")
	if got := e.storedReview(review.ID); got.Version != 1 || got.Instructions != customReviewText || e.runCount() != 0 {
		t.Errorf("a refused caller changed something: %+v", got)
	}
}

func TestReviewPreview_ReportsTheInstructionProblemWithoutAModelCall(t *testing.T) {
	e := newCustomEnv(t)
	e.setReviewerModel()
	e.seedTaggedServices(map[model.ServiceKey]string{nxKey: "prod", nzKey: ""})
	audits, kicks := e.auditCount(), e.kicks.Load()
	plain := e.preview(e.input())
	for _, tc := range instructionProblems("Look") {
		got := e.preview(e.custom(tc.text))
		if got.InstructionsProblem != tc.code || got.ServicesTotal != 2 || got.NextRunAt == nil {
			t.Errorf("%s: %+v, want %s with the counts", tc.name, got, tc.code)
		}
	}
	if got := e.preview(e.input(with("instructions", "Look here."))); got.InstructionsProblem != "instructions_not_allowed" {
		t.Errorf("text on a built-in type: problem %q", got.InstructionsProblem)
	}
	if plain.InstructionsProblem != "" {
		t.Errorf("built-in without text: problem %q", plain.InstructionsProblem)
	}
	// The longest text costs what a built-in review costs.
	good := e.preview(e.custom("\r\n" + strings.Repeat("\U0001F6A2", 2000)))
	if good.InstructionsProblem != "" || good.WorstCaseMonthUSD == nil || plain.WorstCaseMonthUSD == nil || *good.WorstCaseMonthUSD != *plain.WorstCaseMonthUSD {
		t.Errorf("good draft = %+v, want no problem and the worst case of %+v", good, plain)
	}
	// Like everything else that is off, the switch does not stop a preview.
	e.api.Cfg.AgentCustomReviews = false
	if got := e.preview(e.custom(customReviewText)); got.InstructionsProblem != "" {
		t.Errorf("preview with the switch off: %+v", got)
	}
	if e.agentCalls.Load() != 0 || len(e.stored()) != 0 || e.runCount() != 0 || e.auditCount() != audits || e.kicks.Load() != kicks {
		t.Errorf("a preview reached the agent (%d calls), stored, audited or kicked", e.agentCalls.Load())
	}
}

// ---- run now and test run ----

func (e *reviewsEnv) testRun(id int64) *http.Response {
	return e.admin.post(fmt.Sprintf("/api/agent/reviews/%d/run", id), map[string]any{"deliver": false})
}

func (e *reviewsEnv) startedRun(resp *http.Response) store.ReviewRun {
	e.t.Helper()
	if resp.StatusCode != http.StatusAccepted {
		e.t.Fatalf("run: status %d, want 202", resp.StatusCode)
	}
	var started ReviewRunStartedDTO
	decode(e.t, resp, &started)
	run, err := e.st.GetReviewRun(e.t.Context(), started.RunID)
	if err != nil {
		e.t.Fatal(err)
	}
	return run
}

func TestReviewRunNow_TestRunIsRecordedMarkedPlannedAndAudited(t *testing.T) {
	e := newCustomEnv(t)
	e.setReviewerModel()
	for i, review := range []ReviewDTO{e.create(e.custom(customReviewText)), e.create(e.input(with("enabled", false)))} {
		kicks := e.kicks.Load()
		run := e.startedRun(e.testRun(review.ID))
		if !run.Test || !run.Manual || run.State != "requested" || run.DeliveryPlannedAt == nil || !run.DeliveryPlannedAt.Equal(e.now) ||
			run.PeriodLabel != "Test on 25 Nov 2026, 10:00 UTC" || run.ReviewType != review.Type {
			t.Errorf("run = %+v, want a test run marked planned now", run)
		}
		if got := e.kicks.Load(); got != kicks+1 {
			t.Errorf("kicks = %d, want %d", got, kicks+1)
		}
		rows := settingsAuditRows(t, e.st, "agent_review_run")
		if len(rows) != i+1 || rows[0].Details["test"] != true || rows[0].Details["run_id"] != float64(run.ID) || rows[0].Target != review.Name {
			t.Fatalf("audit = %+v", rows)
		}
		if raw, _ := json.Marshal(rows[0]); strings.Contains(string(raw), "largest clusters") {
			t.Errorf("the audit of a run holds the text: %s", raw)
		}
	}
	if e.agentCalls.Load() != 0 {
		t.Error("a test run called the agent")
	}
}

func TestReviewRunNow_NoBodyStillDelivers(t *testing.T) {
	e := newCustomEnv(t)
	e.setReviewerModel()
	review := e.create(e.custom(customReviewText))
	path := fmt.Sprintf("/api/agent/reviews/%d/run", review.ID)
	unsized := func() *http.Response { // a chunked request whose body turns out empty
		req, err := http.NewRequest(http.MethodPost, e.srv.URL+path, io.MultiReader(strings.NewReader("")))
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = -1
		req.Header.Set("X-CSRF-Token", e.admin.csrf)
		resp, err := e.admin.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	for name, send := range map[string]func() *http.Response{
		"no body":      func() *http.Response { return e.admin.post(path, nil) },
		"unsized body": unsized,
		"empty object": func() *http.Response { return e.admin.post(path, map[string]any{}) },
		"true":         func() *http.Response { return e.admin.post(path, map[string]any{"deliver": true}) },
	} {
		run := e.startedRun(send())
		if run.Test || run.DeliveryPlannedAt != nil || !run.Manual || run.PeriodLabel != "Run on 25 Nov 2026, 10:00 UTC" {
			t.Errorf("%s: run = %+v, want one that is delivered", name, run)
		}
		if rows := settingsAuditRows(t, e.st, "agent_review_run"); rows[0].Details["test"] != false {
			t.Errorf("%s: audit = %v", name, rows[0].Details)
		}
		if _, err := e.st.Pool.Exec(t.Context(), `update agent_review_runs set state = 'done', finished_at = now()`); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReviewRunNow_UnknownMemberIsRefused(t *testing.T) {
	e := newCustomEnv(t)
	e.setReviewerModel()
	review := e.create(e.custom(customReviewText))
	audits, kicks := e.auditCount(), e.kicks.Load()
	for _, body := range []string{
		`{"deliver":false,"force":true}`, `{"test":true}`, `{"instructions":"Look here."}`, `{"deliver":"false"}`, `{"deliver":0}`,
		`{"deliver":null}`, `null`, `[]`, `false`,
	} {
		resp := e.admin.post(fmt.Sprintf("/api/agent/reviews/%d/run", review.ID), json.RawMessage(body))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", body, resp.StatusCode)
		}
		resp.Body.Close()
	}
	if e.runCount() != 0 || e.auditCount() != audits || e.kicks.Load() != kicks {
		t.Errorf("a refused body recorded %d runs, %d audits, %d kicks", e.runCount(), e.auditCount()-audits, e.kicks.Load()-kicks)
	}
}

func TestReviewRunNow_TestRunRefusedWhileBusyOffOrWithoutAModel(t *testing.T) {
	e := newCustomEnv(t)
	review := e.create(e.custom(customReviewText))
	kicks := e.kicks.Load()
	expect := func(label string, resp *http.Response, status int, code string) {
		t.Helper()
		if got, _ := refused(t, resp); resp.StatusCode != status || got != code {
			t.Errorf("%s: status %d code %q, want %d %s", label, resp.StatusCode, got, status, code)
		}
	}
	expect("no model", e.testRun(review.ID), http.StatusUnprocessableEntity, "no_model")
	e.setReviewerModel()
	e.api.Cfg.AgentReviewsPerDay = 0
	expect("reviews off", e.testRun(review.ID), http.StatusUnprocessableEntity, "reviews_off")
	e.api.Cfg.AgentReviewsPerDay = 24
	expect("unknown review", e.testRun(987654), http.StatusNotFound, "not_found")
	if e.runCount() != 0 || e.kicks.Load() != kicks || len(settingsAuditRows(t, e.st, "agent_review_run")) != 0 {
		t.Fatalf("a refused test run was recorded (%d), kicked or audited", e.runCount())
	}
	// A test run and a run now are busy to each other.
	e.startedRun(e.testRun(review.ID))
	expect("test run waiting, test run", e.testRun(review.ID), http.StatusConflict, "review_busy")
	expect("test run waiting, run now", e.runNow(review.ID), http.StatusConflict, "review_busy")
	if e.runCount() != 1 || e.kicks.Load() != kicks+1 || len(settingsAuditRows(t, e.st, "agent_review_run")) != 1 {
		t.Errorf("runs %d, kicks %d", e.runCount(), e.kicks.Load()-kicks)
	}
}

func TestAgentReviewRuns_ShowTheCopyAndTheTestMark(t *testing.T) {
	e := newCustomEnv(t)
	review := e.create(e.custom(customReviewText))
	run, err := e.st.RequestManualReviewRun(t.Context(), review.ID, "Test", true, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if got := fetchRuns(t, e, "").Items; len(got) != 1 || !got[0].Test || got[0].Instructions != "" {
		t.Fatalf("before the start = %+v, want the test mark and no copy", got)
	}
	_, claim, err := e.st.ClaimReviewRun(t.Context(), e.now, time.Minute, e.now)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.StartReviewRun(t.Context(), claim, store.ReviewStart{RunID: uuid.New(), Provider: "fake", Model: "fake-1", Instructions: customReviewText}, e.now); err != nil {
		t.Fatal(err)
	}
	e.finishReviewRun(run.ID, e.now, "Fine.", 1)
	e.update(review.ID, e.custom("Other text.", with("version", 1))).Body.Close()
	builtIn := e.create(e.input())
	if _, err := e.st.RequestManualReviewRun(t.Context(), builtIn.ID, "Run", false, e.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got := fetchRuns(t, e, "").Items
	if len(got) != 2 || got[0].Test || got[0].Instructions != "" || !got[1].Test || got[1].Instructions != customReviewText {
		t.Errorf("runs = %+v, want an unmarked run without a copy, then the test run with the text it used", got)
	}
}
