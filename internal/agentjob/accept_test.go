package agentjob

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/agentresult"
)

var (
	investigationOptions = AcceptOptions{AllowLogs: true, Replacement: agentresult.ReplacementSummary}
	reviewOptions        = AcceptOptions{Replacement: agentresult.ReplacementReviewSummary}
)

// A review keeps View evidence only, whoever may read the rest.
func TestAcceptReview_DropsLogsEvidence(t *testing.T) {
	lying := logsItem("connection refused")
	lying.Action = "view"
	res := resultWith(logsItem("connection refused"), viewItem("desired 2, running 0"), lying)
	for name, opts := range map[string]AcceptOptions{"a review": reviewOptions, "no options": {}} {
		got := Accept(res, readAll, opts)
		if len(got.Evidence) != 1 || got.Evidence[0].Action != "view" || got.Evidence[0].Ordinal != 1 || got.EvidenceDropped != 2 {
			t.Errorf("%s: kept %+v, dropped %d; want the View item alone and 2", name, got.Evidence, got.EvidenceDropped)
		}
	}
	// Dropped items do not use up the cap.
	var items []agentresult.Evidence
	for i := range 24 {
		items = append(items, logsItem("x"))
		if i%2 == 0 {
			items = append(items, viewItem(fmt.Sprint("row ", i)))
		}
	}
	if got := Accept(resultWith(items...), readAll, reviewOptions); len(got.Evidence) != 12 || got.EvidenceDropped != 24 {
		t.Errorf("kept %d, dropped %d; want 12 and 24", len(got.Evidence), got.EvidenceDropped)
	}
}

func TestAcceptReview_UsesTheReviewSentence(t *testing.T) {
	bad := agentresult.Result{Summary: "It uses " + fakeSecret + " to sign."}
	for name, c := range map[string]struct {
		opts AcceptOptions
		want string
	}{
		"a review": {reviewOptions, agentresult.ReplacementReviewSummary}, "an investigation": {investigationOptions, agentresult.ReplacementSummary},
	} {
		if got := Accept(bad, readAll, c.opts); got.Summary != c.want || !got.SummaryReplaced {
			t.Errorf("%s: summary = %q, replaced %v; want %q", name, got.Summary, got.SummaryReplaced, c.want)
		}
	}
	if got := Accept(agentresult.Result{Summary: "Spend rose by 12 percent."}, readAll, reviewOptions); got.Summary != "Spend rose by 12 percent." || got.SummaryReplaced {
		t.Errorf("a good summary: %q, replaced %v", got.Summary, got.SummaryReplaced)
	}
}

// The service a review's item names is stored as the service the id decodes to, never as it was spelled.
func TestAcceptReview_StoresTheCanonicalService(t *testing.T) {
	spelled := viewItem("x")
	spelled.ServiceID = checkoutKey.ID()[:8] + "\n" + checkoutKey.ID()[8:]
	got := Accept(resultWith(spelled), readAll, reviewOptions)
	if len(got.Evidence) != 1 || got.Evidence[0].AccountID != checkoutKey.AccountID || got.Evidence[0].Region != checkoutKey.Region ||
		got.Evidence[0].ClusterName != checkoutKey.Cluster || got.Evidence[0].ServiceName != checkoutKey.Service {
		t.Errorf("evidence = %+v", got.Evidence)
	}
}

const requestText = "Compare the running cost of every team this week with the week before, and name three services whose spend moved most."

func TestAcceptReview_SummaryEchoingTheRequestIsReplaced(t *testing.T) {
	// 79 and 80 normalised characters of the request.
	short, long := requestText[1:81], requestText[:81]
	if a, b := len(agentresult.Normalize(short)), len(agentresult.Normalize(long)); a != 79 || b != 80 {
		t.Fatalf("the parts have %d and %d normalised characters", a, b)
	}
	for name, c := range map[string]struct {
		summary     string
		replacement string
		want        string
	}{
		"whole":             {requestText, agentresult.ReplacementReviewSummary, agentresult.ReplacementReviewSummary},
		"within a summary":  {"You asked me: " + strings.ToUpper(long) + "! Spend rose.", agentresult.ReplacementReviewSummary, agentresult.ReplacementReviewSummary},
		"other white space": {strings.ReplaceAll(long, " ", "\n"), agentresult.ReplacementReviewSummary, agentresult.ReplacementReviewSummary},
		"another sentence":  {requestText, agentresult.ReplacementSummary, agentresult.ReplacementSummary},
		"a made-up one":     {requestText, requestText, agentresult.ReplacementSummary},
		"one short":         {short, agentresult.ReplacementReviewSummary, short},
		"its own words":     {"Spend moved most in three services.", agentresult.ReplacementReviewSummary, "Spend moved most in three services."},
	} {
		got := Accept(agentresult.Result{Summary: c.summary}, readAll, AcceptOptions{Replacement: c.replacement, Echo: requestText})
		if got.Summary != c.want || got.SummaryReplaced != (c.want != c.summary) {
			t.Errorf("%s: summary %q, replaced %v; want %q", name, got.Summary, got.SummaryReplaced, c.want)
		}
	}
	// What the agent replaced stays counted.
	if got := Accept(agentresult.Result{Summary: "Fine.", SummaryReplaced: true}, readAll, AcceptOptions{Echo: requestText}); !got.SummaryReplaced {
		t.Error("the agent's own replacement was forgotten")
	}
}

func TestAcceptReview_NoteEchoingTheRequestIsEmptied(t *testing.T) {
	opts := reviewOptions
	opts.Echo = requestText
	fine, first, second := viewItem("desired 2"), viewItem("desired 3"), viewItem("desired 4")
	first.Note, second.Note = requestText, "Asked: "+strings.ReplaceAll(requestText, " ", "\n")
	got := Accept(resultWith(first, fine, second), readAll, opts)
	if len(got.Evidence) != 3 || got.EvidenceDropped != 0 || got.SummaryReplaced {
		t.Fatalf("kept %+v, dropped %d, replaced %v", got.Evidence, got.EvidenceDropped, got.SummaryReplaced)
	}
	for i, want := range []string{agentresult.ReplacementNote, fine.Note, agentresult.ReplacementNote} {
		if got.Evidence[i].Note != want {
			t.Errorf("note %d = %q, want %q", i+1, got.Evidence[i].Note, want)
		}
	}
	if got.Evidence[0].Excerpt != "desired 3" {
		t.Errorf("excerpt = %q", got.Evidence[0].Excerpt)
	}
}

func TestAcceptReview_EmptyEchoChangesNothing(t *testing.T) {
	lying := logsItem("connection refused")
	lying.Action = "view"
	long := viewItem("desired 2")
	long.Note = requestText
	cases := map[string]agentresult.Result{
		"a long summary and note":  {Summary: requestText, Evidence: []agentresult.Evidence{long}},
		"an empty summary":         {},
		"a secret":                 {Summary: "It uses " + fakeSecret + " to sign."},
		"items of every kind":      resultWith(logsItem("connection refused"), viewItem("desired 2, running 0"), lying),
		"replaced by the agent":    {Summary: agentresult.ReplacementReviewSummary, SummaryReplaced: true},
		"a count that is not sane": {Summary: "Fine.", EvidenceDropped: -3},
	}
	for name, res := range cases {
		without := Accept(res, readAll, reviewOptions)
		if name == "a long summary and note" && (without.Summary != requestText || without.SummaryReplaced || without.Evidence[0].Note != requestText) {
			t.Errorf("%s: %+v", name, without)
		}
		// A text that nothing repeats changes nothing either.
		opts := reviewOptions
		opts.Echo = "List every service that runs fewer tasks than it should, oldest problem first, and say since when each one has been so."
		if with := Accept(res, readAll, opts); !reflect.DeepEqual(with, without) {
			t.Errorf("%s: %+v with a text and %+v without", name, with, without)
		}
	}
}
