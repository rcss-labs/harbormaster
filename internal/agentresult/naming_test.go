package agentresult

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

const (
	// Names as CDK makes them: the scrubber takes them for random tokens.
	cdkService = "CheckoutStack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNvRt"
	cdkWorker  = "CheckoutStack-WorkerServiceA71B04D9-pL8sVw3TzKc5"
	cdkCluster = "Infra-ClusterEB0386A7-wD3kQ9sLmN2v"
)

func namesOfService(t *testing.T, service string) secretshape.Names {
	t.Helper()
	return secretshape.NamesOf(subjectID(t, cdkCluster, service))
}

const (
	namingSummary = "The service " + cdkService + " in " + cdkCluster + " stopped after a deploy."
	namingNote    = cdkService + " has no running task."
)

func TestRecheckSummaryNaming_KeepsASummaryThatNamesAKeptService(t *testing.T) {
	keep := namesOfService(t, cdkService)
	for _, replacement := range []string{ReplacementSummary, ReplacementReviewSummary} {
		if got, replaced := RecheckSummaryAs(namingSummary, replacement); !replaced || got != replacement {
			t.Errorf("without the set: got %q, %v", got, replaced)
		}
		if got, replaced := RecheckSummaryNaming(namingSummary, replacement, secretshape.Names{}); !replaced || got != replacement {
			t.Errorf("empty set: got %q, %v", got, replaced)
		}
		if got, replaced := RecheckSummaryNaming(namingSummary, replacement, keep); replaced || got != namingSummary {
			t.Errorf("with the set: got %q, %v", got, replaced)
		}
	}
	if got := RecheckNote(namingNote); got != ReplacementNote {
		t.Errorf("note without the set: %q", got)
	}
	if got := RecheckNoteNaming(namingNote, keep); got != namingNote {
		t.Errorf("note with the set: %q", got)
	}
	if got := RecheckNoteNaming("two\n"+namingNote, keep); got != "two "+namingNote {
		t.Errorf("note over two lines: %q", got)
	}
}

func TestRecheckSummaryNaming_ReplacesGluedForgedAndPatternNames(t *testing.T) {
	awsKey := "AKIA" + "IOSFODNN7EXAMPLE"
	keep := secretshape.NamesOf(subjectIDOf(cdkCluster, cdkService), subjectIDOf(cdkCluster, awsKey), subjectIDOf(awsKey, cdkWorker),
		subjectIDOf(cdkCluster, cdkWorker)+"=", cdkWorker)
	for label, summary := range map[string]string{
		"path":            "See /services/" + cdkService + "/logs.",
		"dash":            "It is " + cdkService + "-1.",
		"equals":          "It is " + cdkService + "= now.",
		"two names":       "It is " + cdkCluster + "/" + cdkService + ".",
		"part":            "It is " + cdkService[1:] + ".",
		"case":            "It is " + strings.Replace(cdkService, "Xy7", "xy7", 1) + ".",
		"another service": "It is " + cdkWorker + ".",
		"pattern":         "It is " + awsKey + ".",
		"secret pair":     "The password=" + cdkService + " is set.",
		"bearer":          "It sends Bearer " + cdkService + ".",
		"one of two":      "It is " + cdkService + " or " + cdkWorker + ".",
	} {
		if got, replaced := RecheckSummaryNaming(summary, ReplacementReviewSummary, keep); !replaced || got != ReplacementReviewSummary {
			t.Errorf("%s: kept %q", label, got)
		}
		if got := RecheckNoteNaming(summary, keep); got != ReplacementNote {
			t.Errorf("%s: note kept %q", label, got)
		}
		read := readOf([2]string{"get_service", `{"status":"down"}`})
		if got, replaced := CheckReadSummaryNaming(summary, read, ReplacementReviewSummary, keep); !replaced || got != ReplacementReviewSummary {
			t.Errorf("%s: check kept %q", label, got)
		}
		if got := CheckReadNoteNaming(summary, read, keep); got != ReplacementNote {
			t.Errorf("%s: check kept the note %q", label, got)
		}
	}
	for label, summary := range map[string]string{
		"over the cap": namingSummary + strings.Repeat("a", SummaryCap), "hidden": namingSummary + string(rune(0x200b)), "blank": " \n",
	} {
		if got, replaced := RecheckSummaryNaming(summary, "free text", keep); !replaced || got != ReplacementSummary {
			t.Errorf("%s: got %q, %v", label, got, replaced)
		}
		if got := RecheckNoteNaming(summary, keep); got != ReplacementNote {
			t.Errorf("%s: note kept %q", label, got)
		}
	}
}

func subjectIDOf(cluster, service string) string {
	return encodeID("482109375512|us-east-1|" + cluster + "|" + service)
}

func TestCheckReadSummaryNaming_CopyCheckStillRuns(t *testing.T) {
	keep := namesOfService(t, cdkService)
	run := letters(VerbatimRun)
	build := func() *ReadCorpus {
		return readOf([2]string{"get_service_logs", `{"lines":[{"message":"` + cdkService + ` ` + run + `"},{"message":"at ` + cdkCluster + `"}]}`})
	}
	if got, replaced := CheckReadSummaryNaming(namingSummary, build(), ReplacementSummary, keep); !replaced || got != ReplacementSummary {
		t.Errorf("a name the copy check may not pass: got %q", got)
	}
	read := build()
	read.AllowNames(cdkCluster, cdkService)
	if got, replaced := CheckReadSummaryNaming(namingSummary, read, ReplacementSummary, keep); replaced || got != namingSummary {
		t.Errorf("an allowed name: got %q, %v", got, replaced)
	}
	if got, replaced := CheckReadSummaryNaming(namingSummary, read, ReplacementSummary, secretshape.Names{}); !replaced {
		t.Errorf("an allowed name without the set: kept %q", got)
	}
	for _, summary := range []string{cdkService + " " + run, "It is " + cdkService + ". It logs " + run + "."} {
		if got, replaced := CheckReadSummaryNaming(summary, read, ReplacementReviewSummary, keep); !replaced || got != ReplacementReviewSummary {
			t.Errorf("a name beside copied text: kept %q", got)
		}
	}
	full := readOf([2]string{"get_service", strings.Repeat("x", CorpusCap+1)})
	for label, corpus := range map[string]*ReadCorpus{"none": nil, "full": full} {
		if got, replaced := CheckReadSummaryNaming(namingSummary, corpus, ReplacementReviewSummary, keep); !replaced || got != ReplacementReviewSummary {
			t.Errorf("%s: got %q", label, got)
		}
		if got := CheckReadNoteNaming(namingNote, corpus, keep); got != ReplacementNote {
			t.Errorf("%s: note %q", label, got)
		}
	}
}

func TestCheckReadNoteNaming_KeepsANoteThatNamesAKeptService(t *testing.T) {
	keep := namesOfService(t, cdkService)
	read := readOf([2]string{"get_service_logs", `{"lines":[{"message":"the settlement ledger refused the connection"}]}`})
	if got := CheckReadNote(namingNote, read); got != ReplacementNote {
		t.Errorf("without the set: %q", got)
	}
	if got := CheckReadNoteNaming(namingNote, read, secretshape.Names{}); got != ReplacementNote {
		t.Errorf("empty set: %q", got)
	}
	if got := CheckReadNoteNaming("First.\n"+namingNote, read, keep); got != "First. "+namingNote {
		t.Errorf("with the set: %q", got)
	}
	for label, note := range map[string]string{
		"copy":     namingNote + " The settlement ledger refused the connection.",
		"markup":   namingNote + " ![x](https://example.com/a.png)",
		"over cap": namingNote + strings.Repeat("n", NoteCap),
		"hidden":   namingNote + string(rune(0x200b)),
	} {
		if got := CheckReadNoteNaming(note, read, keep); got != ReplacementNote {
			t.Errorf("%s: %q", label, got)
		}
	}
}

func TestNaming_ZeroSetEqualsTheOldFunctions(t *testing.T) {
	texts := []string{
		"", " \n\t ", "ok", "two\nlines", "a\rb", "The checkout service is healthy.", copiedLog, "It logged: " + copiedLog, namingSummary, namingNote,
		"key " + "AKIA" + "IOSFODNN7EXAMPLE", "password=hunter2", "See ![x](https://example.com/a.png) here.", "a" + string(rune(0x200b)) + "b", "a\xffb",
		strings.Repeat("n", NoteCap), strings.Repeat("n", NoteCap+1), strings.Repeat("a", SummaryCap), strings.Repeat("a", SummaryCap+1),
		letters(200), letters(200)[4:40], letters(200)[4:94], "migrations pending: three migrations have not been applied yet",
		"Deployment ecs-svc/2233445566778899001 of checkout-api in payments-prod is in progress.",
	}
	reads := map[string]*ReadCorpus{
		"realistic": realisticRun(), "empty": {}, "none": nil, "log": readOf([2]string{"get_service_logs", `{"lines":[{"message":"` + copiedLog + `"}]}`}),
		"letters": readOf([2]string{"get_cost_analytics", `{"by_tag":[{"label":"` + letters(200) + `"}]}`}),
		"full":    readOf([2]string{"get_service", strings.Repeat("x", CorpusCap+1)}),
	}
	zero := map[string]secretshape.Names{"zero value": {}, "no names": secretshape.NamesOf(subjectID(t, "payments-prod", "checkout-api"))}
	for set, keep := range zero {
		for _, text := range texts {
			for _, replacement := range []string{ReplacementSummary, ReplacementReviewSummary, "free text"} {
				want, wantReplaced := RecheckSummaryAs(text, replacement)
				if got, replaced := RecheckSummaryNaming(text, replacement, keep); got != want || replaced != wantReplaced {
					t.Errorf("%s: recheck of %.60q: got %.60q, %v", set, text, got, replaced)
				}
				for label, read := range reads {
					want, wantReplaced := CheckReadSummaryAs(text, read, replacement)
					if got, replaced := CheckReadSummaryNaming(text, read, replacement, keep); got != want || replaced != wantReplaced {
						t.Errorf("%s, %s: check of %.60q: got %.60q, %v", set, label, text, got, replaced)
					}
				}
			}
			if got, want := RecheckNoteNaming(text, keep), RecheckNote(text); got != want {
				t.Errorf("%s: recheck of the note %.60q: got %.60q", set, text, got)
			}
			for label, read := range reads {
				if got, want := CheckReadNoteNaming(text, read, keep), CheckReadNote(text, read); got != want {
					t.Errorf("%s, %s: check of the note %.60q: got %.60q", set, label, text, got)
				}
			}
		}
	}
}

func TestResult_NameIDsOmittedWhenEmpty(t *testing.T) {
	const want = `{"summary":"s","summary_replaced":false,"evidence":null,"evidence_dropped":0}`
	for label, result := range map[string]Result{"nil": {Summary: "s"}, "empty": {Summary: "s", NameIDs: []string{}}} {
		got, err := json.Marshal(result)
		if err != nil || string(got) != want {
			t.Errorf("%s: %s, %v", label, got, err)
		}
	}
	id := subjectID(t, cdkCluster, cdkService)
	got, err := json.Marshal(Result{Summary: "s", NameIDs: []string{id}})
	if err != nil || string(got) != strings.TrimSuffix(want, "}")+`,"name_ids":["`+id+`"]}` {
		t.Errorf("one id: %s, %v", got, err)
	}
}
