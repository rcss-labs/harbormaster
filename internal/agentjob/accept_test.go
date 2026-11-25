package agentjob

import (
	"fmt"
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
