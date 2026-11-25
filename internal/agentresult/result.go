// Package agentresult holds the wire types and the checks shared by the agent service and Harbormaster for what an
// investigator run hands back: a summary, notes and evidence excerpts that people may read without the logs.
package agentresult

const (
	// SummaryCap is the most characters (runes) a summary may have.
	SummaryCap = 1200
	// NoteCap is the most characters (runes) a note may have.
	NoteCap = 300
	// EvidenceCap is the most evidence items a result keeps.
	EvidenceCap = 12
	// ExcerptCap is the most bytes an excerpt keeps.
	ExcerptCap = 1500
	// VerbatimRun is how many normalised characters a copied run needs to be refused.
	VerbatimRun = 32
	// CorpusCap is the most raw bytes of read text a run indexes; beyond it the copy check fails closed.
	CorpusCap = 2 << 20

	// ReplacementSummary stands in for a summary that failed a check.
	ReplacementSummary = "The investigation finished. Open it in Harbormaster to read the findings."
	// ReplacementReviewSummary stands in for a review summary that failed a check.
	ReplacementReviewSummary = "The review finished. Open it in Harbormaster to read the findings."
	// ReplacementNote stands in for a note that failed a check.
	ReplacementNote = "See the excerpt."
	// URLPlaceholder stands in for a removed link.
	URLPlaceholder = "[link removed]"
	// CutMarker ends an excerpt that was shortened.
	CutMarker = " [cut]"
)

// Evidence is one cited piece of what the run read. Tool, ServiceID and Action come from code, never from the model.
type Evidence struct {
	Tool      string `json:"tool"`
	ServiceID string `json:"service_id"`
	Action    string `json:"action"`
	Note      string `json:"note"`
	Excerpt   string `json:"excerpt"`
}

// Result is what an investigator run returns.
type Result struct {
	Summary         string     `json:"summary"`
	SummaryReplaced bool       `json:"summary_replaced"`
	Evidence        []Evidence `json:"evidence"`
	EvidenceDropped int        `json:"evidence_dropped"`
}
