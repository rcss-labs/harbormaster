package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// Names as CDK makes them: the scrubber takes them for random tokens.
const (
	cdkService = "CheckoutStack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNvRt"
	cdkWorker  = "CheckoutStack-WorkerServiceA71B04D9-pL8sVw3TzKc5"
	cdkForeign = "LedgerStack-LedgerServiceC40D77E2-qZ4nTb8WxYk6"
	// A random token that is no name, assembled so that secret scanners do not flag it.
	randomToken = "Qm4Zx81LtVb7" + "RkPw29HsJd6Ny3Fc5Ge0UaTi"
)

var (
	cdkCluster   = model.ClusterKey{AccountID: "123456789012", Region: "eu-west-1", Cluster: "Infra-ClusterEB0386A7-wD3kQ9sLmN2v"}
	cdkServiceID = model.ServiceKey{ClusterKey: cdkCluster, Service: cdkService}.ID()
	cdkWorkerID  = model.ServiceKey{ClusterKey: cdkCluster, Service: cdkWorker}.ID()
	cdkForeignID = model.ServiceKey{ClusterKey: cdkCluster, Service: cdkForeign}.ID()
)

// What runs sent before names could be kept, as one hash for each request and result, and for each batch of generated
// texts. The file was made by this test on the code of that time; nothing regenerates it.
// HM_NAMES_BASE_OUT=<new file> writes another one to compare, HM_NAMES_DIFF=full compares every batch.
const (
	namesBaseFile    = "testdata/names-base.sha256"
	namesBaseBatches = 50
	namesBaseDefault = 3
	namesBaseTexts   = 300 // in each batch
)

type baseRecord struct{ lines []string }

func (b *baseRecord) add(label, text string) {
	sum := sha256.Sum256([]byte(text))
	b.lines = append(b.lines, label+" "+hex.EncodeToString(sum[:]))
}

func (b *baseRecord) addRun(t *testing.T, label string, in RunInput, runner *recordingRunner, steps ...fake.Step) {
	t.Helper()
	p := fake.New(steps...)
	events, err := run(t, fixedNonces(newLoop(p, runner, func(l *Limits) { l.MaxResultBytes = 20_000 })), in)
	if err != nil {
		t.Fatal(err)
	}
	requests := p.Requests()
	if len(requests) != len(steps) {
		t.Errorf("%s: %d requests for %d steps", label, len(requests), len(steps))
	}
	for i, req := range requests {
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		b.add(fmt.Sprintf("%s.request%d", label, i), string(raw))
	}
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	b.add(label+".events", string(raw))
}

// baseBodies are answers with ids of ordinary names only, beside names that look random and other secrets.
func baseBodies() map[string]string {
	row := func(id string) string {
		return `{"service_id":"` + id + `","service":"payments-worker","cluster":"prod-main","note":"` + cdkForeign + `","reason":"` + randomToken + `"}`
	}
	return map[string]string{
		"list_services": `{"items":[{"id":"` + checkoutID + `","name":"checkout-api","tag":{"name":"` + cdkForeign + `"}},{"id":"` + paymentsID + `","name":"` + cdkWorker + `"},` +
			`{"name":"` + cdkForeign + `","arn":"arn:aws:ecs:eu-west-1:123456789012:service/prod-main/` + cdkForeign + `"}],"polled_at":"2026-11-25T09:30:00Z"}`,
		"get_service":   `{"name":"` + cdkForeign + `","running":0,"tags":{"owner":"` + cdkForeignID + `"},"env":[{"name":"DB_PASSWORD","value":"hunter2 ` + randomToken + `"}]}`,
		"list_security": `{"images":[{"service_id":"` + cdkServiceID + `","service":"` + cdkService + `"}]}`,
		"list_alerts":   `{"items":[` + row(checkoutID) + `,` + row(paymentsID) + `]}`,
		"get_service_logs": `{"lines":[{"message":"started ` + cdkService + ` with ` + randomToken + `"},{"message":"password=` + cdkWorker + `"},` +
			`{"message":"{\"service\":\"` + cdkService + `\"}"}]}`,
		"search_logs":               `{"rows":[` + row(checkoutID) + `],"services":[{"id":"` + cdkServiceID + `","name":"` + cdkService + `"}]}`,
		"get_task_definition":       `{"environment":[{"name":"SVC","value":"` + cdkServiceID + `"},{"name":"N","value":"` + cdkService + `"}]}`,
		"get_scaling":               "The service " + cdkService + " could not be read: token " + randomToken,
		"get_service_timeline":      `{"events":[{"title":"` + cdkService + `"},{"title":"` + randomToken + answerTooLargeNote,
		"get_cost_analytics":        `{"rightsizing":{"services":[` + row(paymentsID) + `,` + row(longServiceID) + `]},"spot":{"services":[]},"by_tag":[{"label":"` + cdkForeign + `"}]}`,
		"get_reliability_analytics": `{"least_available":{"rows":[` + row(checkoutID) + `]},"total":"` + randomToken + `"}`,
		"get_capacity_analytics":    `{"at_scaling_max":[` + row(checkoutID) + `],"over_reserved":{"data":{"rows":[` + row(paymentsID) + `]}}}`,
		"get_service_security":      `{"images":[{"repository":"` + cdkForeign + `","failure_detail":"` + randomToken + `"}]}`,
		"get_overview":              `{"clusters":2,"events":[{"text":"` + cdkService + `","service":{"id":"` + cdkServiceID + `"}}]}`,
	}
}

func baseRunner() *recordingRunner {
	bodies := baseBodies()
	return &recordingRunner{reply: func(name string) (string, bool) { return bodies[name], name == "get_scaling" }}
}

// baseRuns records runs whose own ids give no name that looks random, and a chat, which never uses one.
func baseRuns(t *testing.T) *baseRecord {
	b := &baseRecord{}
	searched := provider.ToolCall{ID: "c9", Name: "search_logs", Arguments: map[string]any{"service_ids": []any{checkoutID}, "query": "error"}}
	plain := func(id, name string) provider.ToolCall {
		return provider.ToolCall{ID: id, Name: name, Arguments: map[string]any{}}
	}
	gather := []fake.Step{
		calls(serviceCall("c1", "get_service"), plain("c2", "list_services"), serviceCall("c3", "get_task_definition"), plain("c4", "list_security")),
		calls(plain("c5", "list_alerts"), serviceCall("c6", "get_scaling"), serviceCall("c7", "get_service_timeline"), plain("c8", "get_overview")),
	}
	logs := calls(searched, serviceCall("c10", "get_service_logs"))
	for label, summary := range map[string]string{"plain": plainSummary, "named": "The service " + cdkForeign + " fails.", "listed": "It is " + cdkService + "."} {
		in := investigatorInput()
		in.Task.Alert.Title, in.Task.Alert.Text = cdkForeign+" is down", "reason "+randomToken+" "+cdkServiceID
		b.addRun(t, "investigator-"+label, in, baseRunner(), append(slices.Clone(gather), logs,
			calls(submitCall(summary, cite("e1"), cite("e2"), cite("e7"), cite("e9"), map[string]any{"ref": "e11", "note": "See " + cdkService})))...)
		for _, id := range []string{"cost", "security", "reliability", "capacity", "custom"} {
			in := reviewerInput()
			task := reviewTask(id)
			if id == "custom" {
				task.Review.Instructions = growthRequest + " Include " + cdkForeign + " and " + randomToken + "."
			}
			task.Scope.Entries[1].Name = cdkForeign
			in.Task = &task
			read := calls(costCall("d1"), plain("d2", "get_reliability_analytics"), plain("d3", "get_capacity_analytics"), serviceCall("d4", "get_service_security"))
			b.addRun(t, "review-"+id+"-"+label, in, baseRunner(), append(slices.Clone(gather), read,
				calls(submitCall(summary, cite("e1"), cite("e2"), cite("e7"), map[string]any{"ref": "e9", "note": "See " + cdkForeign})))...)
		}
	}
	for label, page := range map[string]PageContext{"chat": {}, "chat-page": {Path: "/services/" + cdkServiceID, ServiceID: cdkServiceID}} {
		in := baseInput()
		in.Page = page
		in.Messages[0].Text = "What is " + cdkService + " doing?"
		b.addRun(t, label, in, baseRunner(), append(slices.Clone(gather), logs, fake.Text("It runs.", smallUsage))...)
	}
	slices.Sort(b.lines) // the order of a map
	return b
}

// baseTexts generates the texts of one batch. It is frozen together with namesBaseFile.
func baseTexts(seed int64) []string {
	rng := rand.New(rand.NewSource(seed))
	pick := func(list ...string) string { return list[rng.Intn(len(list))] }
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	token := func() string {
		raw := make([]byte, 30+rng.Intn(30))
		for i := range raw {
			raw[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(raw)
	}
	piece := func() string {
		name := pick(cdkService, cdkWorker, cdkForeign, cdkCluster.Cluster)
		switch rng.Intn(12) {
		case 0:
			return token()
		case 1:
			return pick(cdkServiceID, cdkWorkerID, cdkForeignID, cdkCluster.ID(), checkoutID)
		case 2:
			return "password=" + name
		case 3:
			return `"token":"` + name + `"`
		case 4:
			return name[rng.Intn(5):]
		case 5:
			return strings.ToLower(name)
		case 6:
			return fmt.Sprintf(`\u%04x`, name[0]) + name[1:]
		case 7:
			return `{"service":"` + name + `","n":1}`
		case 8:
			return pick("started", "a task failed", "error: refused", "0 of 3", "checkout-api", "prod-main")
		}
		return name
	}
	texts := make([]string, namesBaseTexts)
	for i := range texts {
		var text strings.Builder
		for range 1 + rng.Intn(6) {
			text.WriteString(piece())
			text.WriteString(pick(" ", " ", ", ", `\n`, `"`, "/", "-", "_", "=", ":", "", "\n", "/services/", "."))
		}
		texts[i] = text.String()
		switch rng.Intn(4) {
		case 0: // as a string of a row
			raw, _ := json.Marshal(map[string]any{"rows": []any{map[string]any{"service_id": cdkServiceID, "message": texts[i]}}})
			texts[i] = string(raw)
		case 1: // as a string in a string
			inner, _ := json.Marshal(map[string]string{"message": texts[i]})
			raw, _ := json.Marshal(map[string]any{"lines": []any{map[string]string{"message": string(inner)}}})
			texts[i] = string(raw)
		}
	}
	return texts
}

// cdkTask is an investigation of two services whose names look random.
func cdkTask() Task {
	task := investigation
	task.Subject = TaskSubject{Kind: "service", ServiceIDs: []string{cdkServiceID, cdkWorkerID}, ClusterID: cdkCluster.ID(), AccountID: "123456789012", Region: "eu-west-1"}
	return task
}

// cdkReview is a review of one cluster whose name looks random.
func cdkReview(reviewType string) Task {
	task := reviewTask(reviewType)
	task.Scope.Entries = []ScopeEntry{{Kind: "cluster", ClusterID: cdkCluster.ID(), Name: "shop"}}
	return task
}

func withTask(in RunInput, task Task) RunInput {
	in.Task = &task
	return in
}

// answersAsSent runs texts as answers of calls to tool, forty in a run, and returns the tool messages the model was sent.
func answersAsSent(t *testing.T, in RunInput, tool string, failed bool, texts []string) string {
	t.Helper()
	var sent strings.Builder
	for chunk := range slices.Chunk(texts, 40) {
		var steps []fake.Step
		for first := 0; first < len(chunk); first += 8 {
			var list []provider.ToolCall
			for i := first; i < min(first+8, len(chunk)); i++ {
				call := callOf(fmt.Sprintf("c%d", i), tool)
				list = append(list, call)
			}
			steps = append(steps, calls(list...))
		}
		end := calls(submitCall(plainSummary))
		if in.Role == roleChat {
			end = fake.Text("Done.", smallUsage)
		}
		p := fake.New(append(steps, end)...)
		next := 0
		runner := &recordingRunner{reply: func(string) (string, bool) { next++; return chunk[next-1], failed }}
		if _, err := run(t, fixedNonces(newLoop(p, runner)), in); err != nil {
			t.Fatal(err)
		}
		if next != len(chunk) {
			t.Fatalf("%d of %d answers were read", next, len(chunk))
		}
		for _, m := range lastRequest(p).Messages {
			if m.Role == provider.RoleTool {
				sent.WriteString(m.Text + "\x00")
			}
		}
	}
	return sent.String()
}

// firstAsSent is the first message of a run of in.
func firstAsSent(t *testing.T, in RunInput) string {
	t.Helper()
	p := fake.New(calls(submitCall(plainSummary)))
	if _, err := run(t, fixedNonces(newLoop(p, &recordingRunner{})), in); err != nil {
		t.Fatal(err)
	}
	return requestAt(t, p, 0).Messages[0].Text + "\x00"
}

// baseKinds are the texts of a run that stay as they were whatever its ids name.
var baseKinds = map[string]func(t *testing.T, texts []string) string{
	"chat": func(t *testing.T, texts []string) string {
		return answersAsSent(t, baseInput(), "get_service", false, texts)
	},
	"chat-list": func(t *testing.T, texts []string) string {
		return answersAsSent(t, baseInput(), "list_services", false, texts)
	},
	"logs": func(t *testing.T, texts []string) string {
		return answersAsSent(t, withTask(investigatorInput(), cdkTask()), "get_service_logs", false, texts)
	},
	"search-logs": func(t *testing.T, texts []string) string {
		return answersAsSent(t, withTask(investigatorInput(), cdkTask()), "search_logs", false, texts)
	},
	"failed": func(t *testing.T, texts []string) string {
		return answersAsSent(t, withTask(investigatorInput(), cdkTask()), "get_service", true, texts)
	},
	"failed-review": func(t *testing.T, texts []string) string {
		return answersAsSent(t, withTask(reviewerInput(), cdkReview("cost")), "get_service", true, texts)
	},
	"alert": func(t *testing.T, texts []string) string {
		var sent strings.Builder
		for i := 0; i+2 < len(texts); i += 3 {
			task := cdkTask()
			task.Alert = TaskAlert{Kind: texts[i], Title: texts[i+1], Text: texts[i+2], OpenedAt: time.Date(2026, 11, 25, 9, 30, 0, 0, time.UTC)}
			sent.WriteString(firstAsSent(t, withTask(investigatorInput(), task)))
		}
		return sent.String()
	},
	"scope": func(t *testing.T, texts []string) string {
		var sent strings.Builder
		for i := 0; i+2 < len(texts); i += 3 {
			task := cdkReview("custom")
			task.Review.Instructions = "List what " + texts[i] + " costs."
			if instructionsRefusal(*task.Review) != "" {
				task.Review.Instructions = growthRequest + " " + cdkService
			}
			task.Scope.Entries[0].Name = texts[i+1]
			task.Scope.Entries = append(task.Scope.Entries, ScopeEntry{Kind: "tag", TagID: "12", Name: texts[i+2]})
			sent.WriteString(firstAsSent(t, withTask(reviewerInput(), task)))
		}
		return sent.String()
	},
}

func TestRuns_WithoutKeptNamesSendTheSameBytes(t *testing.T) {
	out := os.Getenv("HM_NAMES_BASE_OUT")
	batches := namesBaseDefault
	if out != "" || os.Getenv("HM_NAMES_DIFF") == "full" {
		batches = namesBaseBatches
	}
	got := baseRuns(t)
	var lock sync.Mutex
	t.Run("batches", func(t *testing.T) {
		for batch := range batches {
			t.Run(fmt.Sprintf("%02d", batch), func(t *testing.T) {
				t.Parallel()
				texts := baseTexts(int64(batch))
				for kind, sent := range baseKinds {
					all := sent(t, texts)
					lock.Lock()
					got.add(fmt.Sprintf("%s.batch%02d", kind, batch), all)
					lock.Unlock()
				}
			})
		}
	})
	slices.Sort(got.lines)
	if out != "" {
		f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(strings.Join(got.lines, "\n") + "\n"); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(namesBaseFile)
	if err != nil {
		t.Fatal(err)
	}
	recorded := strings.Split(strings.TrimSpace(string(raw)), "\n")
	// The whole record, but for the batches that were not run.
	want := slices.DeleteFunc(slices.Clone(recorded), func(line string) bool {
		label, _, _ := strings.Cut(line, " ")
		_, number, isBatch := strings.Cut(label, ".batch")
		n, err := strconv.Atoi(number)
		return isBatch && (err != nil || n >= batches)
	})
	if len(recorded) < namesBaseBatches*len(baseKinds) || len(want) < namesBaseDefault*len(baseKinds) {
		t.Fatalf("%d hashes recorded, %d of them to compare", len(recorded), len(want))
	}
	if !slices.Equal(got.lines, want) {
		for _, line := range got.lines {
			if !slices.Contains(want, line) {
				t.Errorf("not what was sent before: %s", line)
			}
		}
		for _, line := range want {
			if !slices.Contains(got.lines, line) {
				t.Errorf("sent before and not now: %s", line)
			}
		}
		t.Errorf("%d hashes made, %d recorded", len(got.lines), len(want))
	}
}
