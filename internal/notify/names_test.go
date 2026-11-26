package notify

import (
	"encoding/json"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

// Names as CDK makes them: the scrubber takes them for random tokens.
var (
	cdkCluster = model.ClusterKey{AccountID: "482109375512", Region: "us-east-1", Cluster: "Infra-ClusterEB0386A7-wD3kQ9sLmN2v"}
	cdkService = model.ServiceKey{ClusterKey: cdkCluster, Service: "CheckoutStack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNvRt"}
	cdkWorker  = model.ServiceKey{ClusterKey: cdkCluster, Service: "CheckoutStack-WorkerServiceA71B04D9-pL8sVw3TzKc5"}

	cdkSummary = "The service " + cdkService.Service + " in " + cdkCluster.Cluster + " cannot place its tasks."
)

const namesBase = "https://hm.example.com"

// cleaners are the two checks at send time, each with its sentence.
var cleaners = map[string]func(summary, baseURL string, keep secretshape.Names) string{
	agentresult.ReplacementSummary: CleanSummary, agentresult.ReplacementReviewSummary: CleanReviewSummary,
}

func TestCleanSummary_KeepsStoredNames(t *testing.T) {
	keep := secretshape.NamesOf(cdkService.ID())
	for sentence, clean := range cleaners {
		for label, c := range map[string]struct{ in, want string }{
			"whole":               {cdkSummary, cdkSummary},
			"quoted":              {`"` + cdkService.Service + `" (` + cdkCluster.Cluster + `)`, `"` + cdkService.Service + `" (` + cdkCluster.Cluster + `)`},
			"after a JSON escape": {`{"finding":"one\n` + cdkService.Service + ` is down"}`, `{"finding":"one\n` + cdkService.Service + ` is down"}`},
			"glued":               {"It is x" + cdkService.Service + ".", sentence},
			"in a path":           {"Open /services/" + cdkService.Service + "/logs.", sentence},
			"a part":              {"It is " + strings.TrimPrefix(cdkService.Service, "CheckoutStack-") + ".", sentence},
			"another case":        {"It is " + strings.Replace(cdkService.Service, "Xy7", "xY7", 1) + ".", sentence},
			"a password":          {"Set password=" + cdkService.Service + ".", sentence},
			"a secret beside":     {cdkSummary + " It uses AKIA" + "IOSFODNN7EXAMPLE.", sentence},
			"another's name":      {"It is " + cdkWorker.Service + ".", sentence},
			"over the cap":        {cdkSummary + strings.Repeat(" a", 600), sentence},
			"a hidden character":  {cdkSummary + "\u202e", sentence},
		} {
			if got := clean(c.in, namesBase, keep); got != c.want {
				t.Errorf("%s: %q, want %q", label, got, c.want)
			}
		}
	}
}

func TestCleanSummary_WithoutIDsReplacesAsBefore(t *testing.T) {
	for sentence, clean := range cleaners {
		for label, keep := range map[string]secretshape.Names{
			"none": {}, "ordinary ids": secretshape.NamesOf(svcX.ID(), svcX.ClusterKey.ID()), "another service": secretshape.NamesOf(model.ServiceKey{ClusterKey: svcX.ClusterKey, Service: cdkWorker.Service}.ID()),
			"names, not ids": secretshape.NamesOf(cdkService.Service, cdkCluster.Cluster),
		} {
			if got := clean(cdkSummary, namesBase, keep); got != sentence {
				t.Errorf("%s: %q", label, got)
			}
			if got := clean("Plain text, see https://elsewhere.example/x.", namesBase, keep); got != "Plain text, see "+agentresult.URLPlaceholder {
				t.Errorf("%s: %q", label, got)
			}
		}
	}
}

// Scrub, strip, scrub: a link is removed beside a name that stays, and a name can neither hide nor be made by a link.
func TestCleanSummary_ForeignLinkBesideAName(t *testing.T) {
	keep := secretshape.NamesOf(cdkService.ID())
	name, gone := cdkService.Service, agentresult.URLPlaceholder
	for sentence, clean := range cleaners {
		for label, c := range map[string]struct{ in, want string }{
			"beside":               {name + " is down, see https://evil.example/x now", name + " is down, see " + gone + " now"},
			"own link beside":      {name + " is down, see https://hm.example.com/services/x now", name + " is down, see https://hm.example.com/services/x now"},
			"the name in a link":   {"see https://evil.example/" + name + " now", sentence},
			"the name as a label":  {"see [" + name + "](https://evil.example/x) now", "see " + gone + " now"},
			"a secret in a link":   {name + " at https://evil.example/?token=Zx81kq02mmLw93hhTT", sentence},
			"a secret after it":    {name + " //x-api-key: Zx81kq02mmLw93hhTT", sentence},
			"a secret before it":   {"//Authorization: Bearer abcdefghijklmnop1234567890ABCDEF " + name, sentence},
			"past the cap by link": {name + " " + strings.Repeat("http://a ", 100), sentence},
		} {
			got := clean(c.in, namesBase, keep)
			if got != c.want {
				t.Errorf("%s: %q, want %q", label, got, c.want)
			}
			if again := clean(got, namesBase, keep); again != got {
				t.Errorf("%s: again %q, first %q", label, again, got)
			}
		}
	}
}

func TestMessages_EveryDestinationRendersAKeptName(t *testing.T) {
	ids := []string{cdkService.ID()}
	for _, c := range []struct {
		m        Message
		sentence string
	}{{diagnosisMessage(cdkSummary), agentresult.ReplacementSummary}, {reviewMessage(cdkSummary), agentresult.ReplacementReviewSummary}} {
		for _, kept := range []bool{true, false} {
			m, want, not := c.m, c.sentence, cdkService.Service
			if kept {
				want, not = cdkSummary, c.sentence
				if m.Diagnosis != nil {
					m.Diagnosis = &Diagnosis{InvestigationID: 77, Summary: cdkSummary, NameIDs: ids}
				} else {
					review := *m.Review
					review.NameIDs = ids
					m.Review = &review
				}
			}
			slack, err := SlackBody(m)
			if err != nil {
				t.Fatal(err)
			}
			hook, err := WebhookBody(m)
			if err != nil {
				t.Fatal(err)
			}
			_, mail := Email(m, "hm@example.com", []string{"ops@example.com"})
			_, mailBody := decodedEmailBody(t, mail)
			for name, out := range map[string]string{"slack": string(slack), "telegram": TelegramText(m), "email": mailBody, "webhook": string(hook)} {
				if !strings.Contains(out, want) || strings.Contains(out, not) {
					t.Errorf("%s, %s, kept %v: %s", m.Event, name, kept, out)
				}
			}
			for _, out := range []string{string(slack), string(hook)} {
				if strings.Contains(out, cdkService.ID()) || !json.Valid([]byte(out)) {
					t.Errorf("%s: the stored id is in the message: %s", m.Event, out)
				}
			}
		}
	}
}

// The ids come from the stored result at each send, so what is stored decides what is sent.
func TestDispatcher_SendsWithTheStoredIDs(t *testing.T) {
	t.Run("diagnosis", func(t *testing.T) {
		for label, ids := range map[string][]string{"stored": {cdkService.ID()}, "none": {}} {
			e := newInvEnv(t)
			msgs := e.recordMessages()
			e.switched("one")
			e.setAlerts(alert(svcX, "service_failing", model.AlertDanger))
			e.cycle()
			e.finishInvestigations(cdkSummary)
			e.exec(`update agent_investigations set summary_name_ids = $1`, ids)
			e.advance(1)
			e.cycle()
			got := msgs.of(EventDiagnosis)
			if len(got) != 1 || !slices.Equal(got[0].Diagnosis.NameIDs, ids) || got[0].Diagnosis.Summary != cdkSummary || (got[0].summary() == cdkSummary) != (len(ids) > 0) {
				t.Fatalf("%s: messages = %+v", label, got)
			}
		}
	})
	t.Run("review", func(t *testing.T) {
		for label, ids := range map[string][]string{"stored": {cdkService.ID()}, "none": {}} {
			e := newReviewEnv(t)
			msgs := e.recordMessages()
			e.finishReview(e.review("Monthly spend", e.route("one")))
			e.exec(`update agent_review_runs set summary = $1, summary_name_ids = $2`, cdkSummary, ids)
			e.cycle()
			got := msgs.of(EventReview)
			if len(got) != 1 || got[0].Review == nil || !slices.Equal(got[0].Review.NameIDs, ids) || (got[0].summary() == cdkSummary) != (len(ids) > 0) {
				t.Fatalf("%s: messages = %+v", label, got)
			}
		}
	})
}

// With kept names the result is still capped and a fixed point, and shows no other name that looks random.
func TestCleanSummary_WithNamesCapSafeAndIdempotent(t *testing.T) {
	keep := secretshape.NamesOf(cdkService.ID())
	fragments := []string{
		cdkService.Service, cdkService.Service, cdkCluster.Cluster, cdkWorker.Service, cdkService.ID(), "Qm4Zx81LtVb7" + "RkPw29HsJd6Ny3Fc5Ge0UaTi",
		" ", " ", " ", ", ", "\n", "(", ")", "-", "_", "/", "=", "\"", "{\"a\":\"", "\"}", "\\n", "x", "word ", "password=", "Bearer ",
		"http://a ", "https://hm.example.com/services/x ", "https://evil.example/p?q=1 ", "www.evil.io ", "[link removed]", "](", "//", "😀", "AKIA" + "IOSFODNN7EXAMPLE",
	}
	rng := rand.New(rand.NewSource(11))
	kept := 0
	for range 20_000 {
		var b strings.Builder
		for n := rng.Intn(9); n >= 0; n-- {
			b.WriteString(fragments[rng.Intn(len(fragments))])
		}
		in := b.String()
		for sentence, clean := range cleaners {
			once := clean(in, namesBase, keep)
			if utf8.RuneCountInString(once) > agentresult.SummaryCap || clean(once, namesBase, keep) != once {
				t.Fatalf("not capped and idempotent: %q gives %q, then %q", in, once, clean(once, namesBase, keep))
			}
			if strings.Contains(once, cdkWorker.Service) || strings.Contains(once, "Qm4Zx81LtVb7") {
				t.Fatalf("%q gives %q", in, once)
			}
			if before := clean(in, namesBase, secretshape.Names{}); before != once {
				if before != sentence {
					t.Fatalf("%q gives %q, without names %q", in, once, before)
				}
				kept++
			}
		}
	}
	if kept < 1000 {
		t.Errorf("only %d summaries were kept for their names", kept)
	}
}
