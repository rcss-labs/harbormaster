package main

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// customText is a request no canned answer of the fake model repeats and no log line or message should hold.
const customText = "Look at the quiet zebra-mango services\nand name the five that cost the most while idle."

// receivedReview is one `review` body at the in-process receiver.
type receivedReview struct {
	Path string
	Raw  string
	Body struct {
		Event      string `json:"event"`
		ReviewName string `json:"review_name"`
		ReviewType string `json:"review_type"`
		Title      string `json:"title"`
	}
}

func receivedReviews(t *testing.T, me *session, path string) []receivedReview {
	t.Helper()
	status, body := me.send(http.MethodGet, "/fake-notify/received", nil)
	if status != http.StatusOK {
		return nil
	}
	var all []struct {
		Path string          `json:"path"`
		Body json.RawMessage `json:"body"`
	}
	if json.Unmarshal([]byte(body), &all) != nil {
		return nil
	}
	var out []receivedReview
	for _, entry := range all {
		var r receivedReview
		if entry.Path != path || json.Unmarshal(entry.Body, &r.Body) != nil || r.Body.Event != "review" {
			continue
		}
		r.Path, r.Raw = entry.Path, string(entry.Body)
		out = append(out, r)
	}
	return out
}

// customServe starts one serve process with the agent on and the custom switch as given, and a database connection.
func customServe(t *testing.T, switchOn bool, agentURL, upstream, key string) (*session, *pgx.Conn, *syncBuffer) {
	t.Helper()
	schema := requireDB(t)
	logs := &syncBuffer{}
	env := map[string]string{
		"HM_DATABASE_URL": schema, "HM_SESSION_KEY": key, "HM_AGENT_URL": agentURL,
		"HM_POLL_SERVICES": "2s", "HM_POLL_RUNNING_TASKS": "2s", "HM_POLL_IDLE_AFTER": "0",
		"HM_NOTIFY_INTERVAL": "1s", "HM_NOTIFY_SETTLE": "0s", "HM_FAKE_NOTIFY_DEMO": "false",
	}
	if upstream != "" {
		env["HM_LISTEN"], env["HM_BASE_URL"] = upstream, "http://"+upstream
	}
	if switchOn {
		env["HM_AGENT_CUSTOM_REVIEWS"] = "true"
	}
	base := startServeLogged(t, env, logs)
	conn, err := pgx.Connect(context.Background(), schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return signIn(t, base), conn, logs
}

// createCustomReview saves a custom review that is off, so only a run by hand makes a result, and returns its id.
func createCustomReview(t *testing.T, me *session, name, text string, routeID int64) int64 {
	t.Helper()
	status, body := me.send(http.MethodPost, "/api/agent/reviews", map[string]any{
		"name": name, "type": "custom", "enabled": false, "instructions": text,
		"schedule": map[string]any{"cadence": "weekly", "at_minute": 480, "weekday": 1, "month_day": nil, "timezone": "UTC"},
		"scope":    []any{}, "route_ids": []int64{routeID},
	})
	var created struct {
		ID int64 `json:"id"`
	}
	if (status != http.StatusOK && status != http.StatusCreated) || json.Unmarshal([]byte(body), &created) != nil || created.ID == 0 {
		t.Fatalf("create custom review = %d %s", status, body)
	}
	return created.ID
}

func runReviewByHand(t *testing.T, me *session, reviewID int64, payload any) {
	t.Helper()
	status, body := me.send(http.MethodPost, "/api/agent/reviews/"+itoa(reviewID)+"/run", payload)
	if status != http.StatusAccepted && status != http.StatusOK {
		t.Fatalf("run review %d = %d %s", reviewID, status, body)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func waitRunDone(t *testing.T, conn *pgx.Conn, reviewID int64) reviewRunRow {
	t.Helper()
	var last []reviewRunRow
	within(t, 120*time.Second, "the run of review "+itoa(reviewID)+" is done", func() bool {
		last = reviewRunRows(t, conn, reviewID)
		return len(last) == 1 && last[0].state == "done"
	})
	return last[0]
}

var requestTag = regexp.MustCompile(`(?s)<admin_request_([A-Za-z0-9]+)>\n(.*?)\n</admin_request_([A-Za-z0-9]+)>`)

// A custom review is run by hand. The text the admin saved reaches the model once, inside the request tag of the
// first message and nowhere else, and one `review` body with the type custom reaches the route, without the text.
func TestCustomReview_RunsWithItsSavedTextAndIsDelivered(t *testing.T) {
	hm := freeAddr(t)
	agentURL, model := startRecordedFakeAgent(t, "http://"+hm)
	me, conn, logs := customServe(t, true, agentURL, hm, "custom-review-session-key")
	const path = "/fake-notify/webhook/customrun"
	routeID := createRoute(t, me, me.base, "custom run sink", path)
	reviewID := createCustomReview(t, me, "Idle, by hand", customText, routeID)

	runReviewByHand(t, me, reviewID, nil)
	waitRunDone(t, conn, reviewID)
	within(t, 60*time.Second, "the review message at the receiver", func() bool { return len(receivedReviews(t, me, path)) > 0 })

	messages := receivedReviews(t, me, path)
	if len(messages) != 1 || messages[0].Body.ReviewType != "custom" || messages[0].Body.ReviewName != "Idle, by hand" {
		t.Fatalf("messages at the receiver = %+v, want one custom review message", messages)
	}
	if strings.Contains(messages[0].Raw, "zebra-mango") {
		t.Errorf("the message holds the instructions: %s", messages[0].Raw)
	}
	if messages[0].Body.Title != "Custom review: Idle, by hand" {
		t.Errorf("title = %q, want the Custom review headline", messages[0].Body.Title)
	}

	requests := model.Requests()
	if len(requests) == 0 {
		t.Fatal("the model was sent no request")
	}
	for i, req := range requests {
		if strings.Contains(req.System, "zebra-mango") {
			t.Errorf("request %d: the system text holds the instructions", i)
		}
		count := 0
		for _, m := range req.Messages {
			count += strings.Count(m.Text, customText)
		}
		if count != 1 {
			t.Errorf("request %d holds the text %d times, want once", i, count)
		}
	}
	found := requestTag.FindStringSubmatch(requests[0].Messages[0].Text)
	if found == nil || found[1] != found[3] || found[2] != customText {
		t.Errorf("first message = %q, want the text alone inside one request tag", requests[0].Messages[0].Text)
	}
	if strings.Contains(logs.String(), "zebra-mango") {
		t.Errorf("the process log holds the instructions")
	}
}

// A test run is stored and sends nothing. The later message of a second review proves the dispatcher scanned after
// the test run was done, and it is the only one at the receiver.
func TestCustomReview_TestRunSendsNothing(t *testing.T) {
	hm := freeAddr(t)
	agentURL := startFakeAgent(t, "http://"+hm)
	me, conn, _ := customServe(t, true, agentURL, hm, "custom-test-session-key")
	const path = "/fake-notify/webhook/customtest"
	routeID := createRoute(t, me, me.base, "custom test sink", path)
	testID := createCustomReview(t, me, "Idle, test", customText, routeID)
	otherID := createCustomReview(t, me, "Idle, delivered", customText+" Again.", routeID)

	runReviewByHand(t, me, testID, map[string]any{"deliver": false})
	testRun := waitRunDone(t, conn, testID)
	var isTest bool
	var planned *time.Time
	if err := conn.QueryRow(context.Background(), `select test, delivery_planned_at from agent_review_runs where id = $1`, testRun.id).Scan(&isTest, &planned); err != nil || !isTest || planned == nil {
		t.Fatalf("test run row: test = %v, planned = %v, %v; want a test run that was marked planned when made", isTest, planned, err)
	}

	runReviewByHand(t, me, otherID, nil)
	waitRunDone(t, conn, otherID)
	within(t, 60*time.Second, "the other review's message", func() bool { return len(receivedReviews(t, me, path)) > 0 })
	// The other review's message was planned after the test run was done, so at least one scan has passed it by.
	messages := receivedReviews(t, me, path)
	if len(messages) != 1 || messages[0].Body.ReviewName != "Idle, delivered" {
		t.Fatalf("messages at the receiver = %+v, want only the delivered review's", messages)
	}
	var deliveries int
	if err := conn.QueryRow(context.Background(), `select count(*) from notification_deliveries where kind = 'review' and route_id = $1`, routeID).Scan(&deliveries); err != nil || deliveries != 1 {
		t.Fatalf("review deliveries = %d, %v; want 1", deliveries, err)
	}
}

// With the switch off the type is not offered, a custom review cannot be made, and one already stored and due is
// never run: the model is asked nothing and nothing is sent. A built-in review due beside it still runs, which also
// proves the job scanned.
func TestCustomReview_SwitchOffRunsAndOffersNothing(t *testing.T) {
	hm := freeAddr(t)
	agentURL, model := startRecordedFakeAgent(t, "http://"+hm)
	me, conn, _ := customServe(t, false, agentURL, hm, "custom-off-session-key")
	const path = "/fake-notify/webhook/customoff"
	routeID := createRoute(t, me, me.base, "custom off sink", path)

	status, body := me.send(http.MethodGet, "/api/agent/settings", nil)
	if status != http.StatusOK || strings.Contains(body, `"custom"`) {
		t.Fatalf("settings = %d %s, want no custom type offered", status, body)
	}
	status, body = me.send(http.MethodPost, "/api/agent/reviews", map[string]any{
		"name": "Idle, refused", "type": "custom", "enabled": false, "instructions": customText,
		"schedule": map[string]any{"cadence": "weekly", "at_minute": 480, "weekday": 1, "month_day": nil, "timezone": "UTC"},
		"scope":    []any{}, "route_ids": []int64{routeID},
	})
	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "custom_reviews_off") {
		t.Fatalf("create with the switch off = %d %s, want 422 custom_reviews_off", status, body)
	}

	fiveMinutesAgo := time.Now().UTC().Add(-5 * time.Minute)
	var customID int64
	if err := conn.QueryRow(context.Background(), `insert into agent_reviews (name, type, cadence, at_minute, timezone, schedule_changed_at, enabled, instructions)
		values ('Idle, stored', 'custom', 'daily', $1, 'UTC', now() - interval '2 days', true, $2) returning id`,
		fiveMinutesAgo.Hour()*60+fiveMinutesAgo.Minute(), customText).Scan(&customID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), `insert into agent_review_routes (review_id, route_id) values ($1, $2)`, customID, routeID); err != nil {
		t.Fatal(err)
	}
	builtInID := createReviewDueNow(t, me, conn, "Cost, beside it", routeID)

	within(t, 120*time.Second, "the built-in review's message", func() bool { return len(receivedReviews(t, me, path)) > 0 })
	waitRunDone(t, conn, builtInID)
	for _, row := range reviewRunRows(t, conn, customID) {
		if row.state == "done" || row.state == "running" {
			t.Errorf("custom run %+v with the switch off", row)
		}
	}
	messages := receivedReviews(t, me, path)
	if len(messages) != 1 || messages[0].Body.ReviewType != "cost" {
		t.Errorf("messages at the receiver = %+v, want only the cost review's", messages)
	}
	for i, req := range model.Requests() {
		for _, m := range req.Messages {
			if strings.Contains(m.Text, "zebra-mango") {
				t.Errorf("request %d holds the instructions of a review that is switched off", i)
			}
		}
	}
}

// The agent service is away, refusing at once or accepting and never answering, while a custom review is due. As for
// a built-in review, the review is claimed first, an alert on another route raised after that arrives within
// seconds, and nothing is sent for the review.
func TestCustomReview_AgentDownDelaysNoAlert(t *testing.T) {
	for name, agentAddr := range map[string]func(*testing.T) string{"port closed": closedAddr, "accepts and never answers": hangingAddr} {
		t.Run(name, func(t *testing.T) {
			me, conn, logs := customServe(t, true, "http://"+agentAddr(t), "", "custom-down-session-key")
			const reviewPath, alertPath = "/fake-notify/webhook/customdown", "/fake-notify/webhook/customalert"
			reviewRoute := degradedRoute(me.base)
			reviewRoute["name"], reviewRoute["url"], reviewRoute["kinds"], reviewRoute["min_severity"] = "custom down sink", me.base+reviewPath, []string{"account_unreachable"}, "critical"
			status, body := me.send(http.MethodPost, "/api/notifications/routes", reviewRoute)
			var created struct {
				ID int64 `json:"id"`
			}
			if (status != http.StatusOK && status != http.StatusCreated) || json.Unmarshal([]byte(body), &created) != nil || created.ID == 0 {
				t.Fatalf("create route = %d %s", status, body)
			}
			// The API refuses a review while the agent cannot say which types it offers, so the row is written directly.
			var reviewID int64
			fiveMinutesAgo := time.Now().UTC().Add(-5 * time.Minute)
			if err := conn.QueryRow(context.Background(), `insert into agent_reviews (name, type, cadence, at_minute, timezone, schedule_changed_at, enabled, instructions)
				values ('Idle, agent down', 'custom', 'daily', $1, 'UTC', now() - interval '2 days', true, $2) returning id`,
				fiveMinutesAgo.Hour()*60+fiveMinutesAgo.Minute(), customText).Scan(&reviewID); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(context.Background(), `insert into agent_review_routes (review_id, route_id) values ($1, $2)`, reviewID, created.ID); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if t.Failed() {
					t.Logf("runs: %+v\nlog:\n%s", reviewRunRows(t, conn, reviewID), logs.String())
				}
			}()

			within(t, 90*time.Second, "the review was claimed", func() bool {
				rows := reviewRunRows(t, conn, reviewID)
				return len(rows) == 1 && (rows[0].state != "requested" || rows[0].retryAt != nil)
			})
			alertRoute := degradedRoute(me.base)
			alertRoute["name"], alertRoute["url"] = "custom alert sink", me.base+alertPath
			if status, body := me.send(http.MethodPost, "/api/notifications/routes", alertRoute); status != http.StatusOK && status != http.StatusCreated {
				t.Fatalf("create alert route = %d %s", status, body)
			}
			within(t, 5*time.Second, "alert.raised on the alert route, with the custom review stuck or failing", func() bool {
				return countEvents(receivedEvents(t, me, alertPath), "alert.raised") > 0
			})

			within(t, 90*time.Second, "the review waits for a retry or is skipped", func() bool {
				rows := reviewRunRows(t, conn, reviewID)
				return len(rows) == 1 && ((rows[0].state == "requested" && rows[0].retryAt != nil) || rows[0].state == "skipped")
			})
			for _, row := range reviewRunRows(t, conn, reviewID) {
				if row.state == "done" || row.state == "running" {
					t.Errorf("run %+v with no agent", row)
				}
			}
			if got := len(receivedReviews(t, me, reviewPath)); got != 0 {
				t.Errorf("review messages at the receiver = %d, want none", got)
			}
			if out := logs.String(); strings.Contains(out, "panic") || strings.Contains(out, "zebra-mango") {
				t.Errorf("the process logged a panic or the instructions:\n%s", out)
			}
		})
	}
}
