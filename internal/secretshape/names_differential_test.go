package secretshape

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// The differential tests compare the scrubber with what it did before names could be kept. They use few texts by
// default; HM_SCRUB_DIFF=full runs all of them, in subtests "seeds_NNN" that -run can select one by one.
const (
	baselineFile    = "testdata/scrub-baseline.sha256"
	baselineBatches = 200
	baselineTexts   = 10_000 // in each batch
	diffGroup       = 20     // batches in one subtest
)

func fullDiff() bool { return os.Getenv("HM_SCRUB_DIFF") == "full" }

// diffGroups runs check once for every batch number below batches, in parallel, in subtests of diffGroup batches; by
// default only for the first few.
func diffGroups(t *testing.T, batches, byDefault int, check func(t *testing.T, batch int)) {
	t.Helper()
	if !fullDiff() {
		batches = min(batches, byDefault)
	}
	for first := 0; first < batches; first += diffGroup {
		t.Run(fmt.Sprintf("seeds_%03d", first), func(t *testing.T) {
			var workers sync.WaitGroup
			for batch := first; batch < min(first+diffGroup, batches); batch++ {
				workers.Add(1)
				go func() {
					defer workers.Done()
					check(t, batch)
				}()
			}
			workers.Wait()
		})
	}
}

// diffTexts generates texts from a seed. It is frozen together with baselineFile: it uses nothing that other tests or
// the scrubber may change, and a different text for one seed makes the recorded hashes useless.
type diffTexts struct {
	rng *rand.Rand
	// kept are mixed into the texts; without any the same seed gives the texts of the baseline.
	kept []string
}

func newDiffTexts(seed int64, kept ...string) *diffTexts {
	return &diffTexts{rng: rand.New(rand.NewSource(seed)), kept: kept}
}

const (
	diffAlnum = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	diffHex   = "0123456789abcdef"
)

func (d *diffTexts) pick(of ...string) string { return of[d.rng.Intn(len(of))] }

func (d *diffTexts) chars(alphabet string, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = alphabet[d.rng.Intn(len(alphabet))]
	}
	return string(out)
}

// cdkName is a name as CDK and CloudFormation make them.
func (d *diffTexts) cdkName() string {
	return d.pick("CheckoutStack", "HarborStack", "Infra", "prod", "payments-prod") + "-" +
		d.pick("CheckoutService", "ApiServiceService", "Cluster", "WebService", "worker") + d.chars("0123456789ABCDEF", 8) +
		"-" + d.chars(diffAlnum, 12)
}

func (d *diffTexts) name() string {
	switch d.rng.Intn(3) {
	case 0:
		return d.pick("checkout-api", "prod-cluster", "ingest_worker_v2", "payments")
	case 1:
		return d.chars("abcdefghij-_XYZ0189", 1+d.rng.Intn(60))
	}
	return d.cdkName()
}

func (d *diffTexts) id() string {
	key := model.ClusterKey{AccountID: "123456789012", Region: d.pick("us-east-1", "eusc-de-east-1", "eu-west-1"), Cluster: d.name()}
	switch d.rng.Intn(3) {
	case 0:
		return key.ID()
	case 1:
		return model.ServiceKey{ClusterKey: key, Service: d.name()}.ID()
	}
	return model.TaskID(key.AccountID, key.Region, "arn:aws:ecs:"+key.Region+":"+key.AccountID+":task/"+key.Cluster+"/"+d.chars(diffHex, 32))
}

// keptName is one of the kept names, mostly whole.
func (d *diffTexts) keptName() string {
	name := d.pick(d.kept...)
	switch d.rng.Intn(8) {
	case 0:
		return name[:1+d.rng.Intn(len(name)-1)]
	case 1:
		return name[1+d.rng.Intn(len(name)-1):]
	case 2:
		return strings.ToLower(name)
	case 3:
		return name + d.chars(diffAlnum+"-_/+=", 1+d.rng.Intn(3))
	}
	return name
}

// piece is one token.
func (d *diffTexts) piece() string {
	if len(d.kept) > 0 && d.rng.Intn(3) == 0 {
		return d.keptName()
	}
	switch d.rng.Intn(13) {
	case 0:
		return d.id()
	case 1:
		return d.cdkName()
	case 2:
		return d.chars(diffAlnum+"-_", 20+d.rng.Intn(50))
	case 3:
		return d.chars(diffAlnum+"+/", 32+d.rng.Intn(32)) + d.pick("", "=", "==")
	case 4:
		return d.chars(diffHex, 32+d.rng.Intn(40))
	case 5:
		return d.chars(diffHex, 8) + "-" + d.chars(diffHex, 4) + "-" + d.chars(diffHex, 4) + "-" + d.chars(diffHex, 4) + "-" + d.chars(diffHex, 12)
	case 6:
		return "arn:aws:ecs:eu-west-1:123456789012:service/" + d.cdkName() + "/" + d.cdkName()
	case 7:
		return d.pick("sha256:", "registry.example.test/team/app:", "registry.example.test/team/app@sha256:") + d.chars(diffHex, 64)
	case 8:
		return d.pair()
	case 9:
		return d.pick(baselineLeakRows...)
	case 10:
		return d.pick("AKIA"+d.chars("ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", 16), "ghp_"+d.chars(diffAlnum, 36), "sk-"+d.chars(diffAlnum, 24),
			"hmp_"+d.chars("abcdefghijklmnopqrstuvwxyz234567", 12)+"_"+d.chars(diffAlnum+"-_", 43))
	case 11:
		return d.pick("started", "task stopped", "exit code 137", "GET /healthz 200", "", "[redacted]")
	}
	return d.chars("abcdefgh-", 5+d.rng.Intn(30))
}

// value is a token that may stand as the value of a pair.
func (d *diffTexts) value() string {
	if len(d.kept) > 0 && d.rng.Intn(2) == 0 {
		return d.keptName()
	}
	switch d.rng.Intn(5) {
	case 0:
		return "hunter2"
	case 1:
		return d.chars(diffAlnum+"-_", 32+d.rng.Intn(20))
	case 2:
		return d.id()
	case 3:
		return d.cdkName()
	}
	return d.pick("12", "true", "checkout-api", "a b c")
}

func (d *diffTexts) memberName() string {
	return d.pick("name", "id", "service_id", "value", "message", "password", "DB_PASSWORD", "api_keys", "max_tokens", "auth", "cluster",
		"key", "secrets", "credentials", "token", "valueFrom", "nextToken", "service", "status")
}

func (d *diffTexts) pair() string {
	name, value := d.memberName(), d.value()
	return d.pick(
		name+"="+value,
		name+": "+value,
		`"`+name+`":"`+value+`"`,
		name+"=["+value+", "+d.value()+"]",
		name+"={"+value+"}",
		`{"name":"`+name+`","value":"`+value+`"}`,
		`{\"`+name+`\":\"`+value+`\"}`,
		"Bearer "+value,
		"Authorization: Bearer "+value,
		"postgres://app:"+value+"@db.example.test/app",
		"https://x.example.test/v1?"+name+"="+value+"&a=1",
		"user["+name+"]="+value,
	)
}

func (d *diffTexts) glued() string {
	var text strings.Builder
	for i, count := 0, 1+d.rng.Intn(5); i < count; i++ {
		text.WriteString(d.piece())
		text.WriteString(d.pick(" ", " ", "\n", "\n", `"`, "/", "-", "_", "=", ":", ", ", ".", "/services/", "/logs", `\n`, `\"`, ""))
	}
	return text.String()
}

// quote writes text as a JSON string. Now and then a character is written as \u00XX.
func (d *diffTexts) quote(text string) string {
	var out strings.Builder
	out.WriteByte('"')
	unicodeEscapes := d.rng.Intn(6) == 0
	for i := 0; i < len(text); i++ {
		switch c := text[i]; {
		case c == '"' || c == '\\':
			out.WriteByte('\\')
			out.WriteByte(c)
		case c == '\n':
			out.WriteString(`\n`)
		case unicodeEscapes && d.rng.Intn(16) == 0:
			fmt.Fprintf(&out, `\u%04x`, c)
		default:
			out.WriteByte(c)
		}
	}
	out.WriteByte('"')
	return out.String()
}

func (d *diffTexts) jsonValue(depth int) string {
	kind := d.rng.Intn(8)
	switch {
	case kind == 0 && depth < 3:
		return d.object(depth + 1)
	case kind == 1 && depth < 3:
		return "[" + d.jsonValue(depth+1) + "," + d.jsonValue(depth+1) + "]"
	case kind == 2:
		return d.pick("12", "-1.5e3", "true", "null", d.chars("123456789", 20+d.rng.Intn(30)))
	case kind == 3 && depth < 3:
		return d.quote(d.object(depth + 1))
	case kind == 4:
		return d.quote(d.glued())
	case kind == 5:
		return d.quote(d.value())
	}
	return d.quote(d.piece())
}

func (d *diffTexts) object(depth int) string {
	members := make([]string, 1+d.rng.Intn(4))
	for i := range members {
		name := d.memberName()
		if len(d.kept) > 0 && d.rng.Intn(12) == 0 {
			name = d.keptName()
		}
		members[i] = d.quote(name) + d.pick(":", ": ") + d.jsonValue(depth)
	}
	return "{" + strings.Join(members, d.pick(",", ", ", ",\n  ")) + "}"
}

// text is the next text: glued tokens, JSON whole, one value per line or inside strings, and escaped or cut JSON.
func (d *diffTexts) text() string {
	switch d.rng.Intn(8) {
	case 0, 1, 2:
		return d.glued()
	case 3:
		return d.object(0)
	case 4:
		var lines strings.Builder
		for i, count := 0, 1+d.rng.Intn(3); i < count; i++ {
			lines.WriteString(d.pick(d.object(1), d.glued(), "ok") + "\n")
		}
		return lines.String()
	case 5:
		text := d.object(1)
		for i, levels := 0, 1+d.rng.Intn(3); i < levels; i++ {
			text = d.quote(text)
		}
		return text
	case 6:
		text := d.object(1)
		for i, levels := 0, 1+d.rng.Intn(3); i < levels; i++ {
			text = d.quote(text)
			text = text[1 : len(text)-1]
		}
		return text
	}
	text := d.object(0)
	if d.rng.Intn(2) == 0 {
		text = d.quote(text)
	}
	return text[:1+d.rng.Intn(len(text))]
}

// baselineLeakRows are the texts of leakCorpus on the day of the baseline. They are copied because that corpus grows.
var baselineLeakRows = func() []string {
	rows := []string{
		`auth: API_KEYS=x,hunter2`, `auth: secret={abc hunter2}`, `secret: API_KEYS=[x hunter2]`, `secret: password=[x hunter2]`,
		`SERVICE_TOKENS: Basic aHVudGVyMjp4`, `SERVICE_TOKENS=Bearer x,hunter2`, `DATABASE_PASSWORD=hunter2 LOG_LEVEL=debug`,
		`dial postgres://app:hun/ter2@db/x failed`, `Authorization: Bearer hun}ter2`, `x Bearer hunter2`,
		`url=https://api.x.com/v1?api_key=hunter2&a=1`, `https://x/y?a=1&token=hunter2`, `line1\ntoken=hunter2`,
		`error:password=hunter2`, `password="ab\"hunter2" next=1`, `password=}hunter2`, `DB_PASSWORD=p[0]hunter2`,
		`secret={abc}hunter2`, `Server=db;Password=hu}nter2;X=1`, `auth: {user}:{hunter2}`, `api_key=${X}hunter2`,
		`API_KEYS=abc,hunter2`, `API_KEYS=[abc hunter2] region=eu`, `"api_keys":["a]b","hunter2"],"n":1`,
		`user[password]=hunter2 next`, `secret={abc hunter2} x`, `map[password:hunter2 user:bob]`, `{"password":"hunter2"}`,
		`{\"password\":\"hunter2\"}`, `{"message":"{\"db\":{\"secret\":\"hunter2\"}}"}`, `{"name":"DB_PASSWORD","value":"hunter2"}`,
		`{"name":"DB_PASSWORD","value":["hunter2",{"a":"b"}]}`, `{"password":123459876}`, `{"password":{"plain":"hunter2"}}`,
		`{"credentials":{"user":"hunter2"}}`, `{"secrets":{"db":"hunter2"}}`, `{"secret":[{"plain":"hunter2"}]}`,
		`{"headers":{"Authorization":["Bearer hunter2"]}}`,
		"got " + "kQ9x_Tz3-Lm8vWb2Nc7Yd4Re1Uf6Hg0Js5Ap" + "Xo-Qi_Zr" + " back",
		"env_" + "AKIA" + "IOSFODNN7EXAMPLE" + " set",
		`x\n` + "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789",
		"auth " + "eyJhbGciOiJIUzI1NiJ9" + "." + "eyJzdWIiOiJ0ZXN0In0" + "." + "c2lnbmF0dXJlLWZha2U" + " status=200",
	}
	for _, name := range []string{"password", "db.password", "api_key", "AUTH_TOKEN", "secret", "credentials", "API_KEYS", "SERVICE_TOKENS"} {
		forms := []string{"@", `"@"`, `'a @'`, "[@]", "[x, @]", `["x","@"]`, "[[x],[@]]", "[a[0],@]", "Bearer @", "Basic @", "a}@", "[x @", "[@"}
		switch name {
		case "API_KEYS", "SERVICE_TOKENS":
			forms = append(forms, "x,@", "Bearer x,@", "x,y,@")
		default:
			forms = append(forms, "{@}", "{x @}", "{x:{y @}}", "{x @", `{"a":"@"}`, `{"a":{"b":["x","@"]}}`)
		}
		for _, form := range forms {
			value := strings.Replace(form, "@", "hunter2x", 1)
			rows = append(rows, name+"="+value, name+": "+value, `"`+name+`":`+value)
		}
	}
	return rows
}()

// baselineHash is the hash over what scrubText makes of the texts of one batch.
func baselineHash(batch int, scrubText func(string) string) string {
	texts := newDiffTexts(int64(batch) + 1)
	hash := sha256.New()
	for i := 0; i < baselineTexts; i++ {
		hash.Write([]byte(scrubText(texts.text())))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// The hashes were recorded before the scrubber knew names. They stay as they are: a difference is a change of what
// ScrubText hides. HM_SCRUB_BASELINE_OUT=<new file> writes the hashes of the code at hand to compare from outside.
func TestScrubBaseline_Unchanged(t *testing.T) {
	if path := os.Getenv("HM_SCRUB_BASELINE_OUT"); path != "" {
		hashes := make([]string, baselineBatches)
		var workers sync.WaitGroup
		for batch := range hashes {
			workers.Add(1)
			go func() {
				defer workers.Done()
				hashes[batch] = baselineHash(batch, ScrubText)
			}()
		}
		workers.Wait()
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer out.Close()
		if _, err := out.WriteString(strings.Join(hashes, "\n") + "\n"); err != nil {
			t.Fatal(err)
		}
		return
	}
	recorded, err := os.ReadFile(baselineFile)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields(string(recorded))
	if len(want) != baselineBatches {
		t.Fatalf("%d hashes are recorded, want %d", len(want), baselineBatches)
	}
	foreign := foreignNames()
	diffGroups(t, baselineBatches, 20, func(t *testing.T, batch int) {
		if got := baselineHash(batch, ScrubText); got != want[batch] {
			t.Errorf("batch %d: the output changed", batch)
		}
		if got := baselineHash(batch, func(text string) string { return ScrubTextKeeping(text, foreign) }); got != want[batch] {
			t.Errorf("batch %d: the output changed with names that are in none of the texts", batch)
		}
	})
}

// foreignNames are 50 kept names that diffTexts never writes.
func foreignNames() Names {
	names := newDiffTexts(-1)
	var ids []string
	for i := 0; i < 50; i++ {
		ids = append(ids, diffServiceID("prod-cluster", "Outside"+names.cdkName()))
	}
	return NamesOf(ids...)
}

func diffServiceID(cluster, service string) string {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "123456789012", Region: "eu-west-1", Cluster: cluster}, Service: service}.ID()
}

// diffKeptNames are names a set keeps. Some are the start or the end of another, or differ in one letter.
var diffKeptNames = []string{
	"CheckoutStack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNvRt",
	"CheckoutStack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNvRtA",
	"CheckoutStack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNvRu",
	"Stack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNvRt",
	"Infra-ClusterEB0386A7-wD3kQ9sLmN2v",
	"kQ9x_Tz3-Lm8vWb2Nc7Yd4Re1Uf6Hg0Js5ApXo-Qi",
	"HarborStack-ApiServiceService7C1A2B3D-Xk3J9mLpQ2Rt",
	"-Xk3J9mLpQ2Rt7C1A2B3DXk3J9mLpQ2Rt_",
	"Zm9vYmFyQmF6QXV0aDB4OTlYWXo1MjabcdEFGH",
}

func diffKept(t *testing.T) (Names, map[string]bool) {
	t.Helper()
	ids, oracle := make([]string, 0, len(diffKeptNames)), map[string]bool{}
	for _, name := range diffKeptNames {
		ids = append(ids, diffServiceID("prod-cluster", name))
		oracle[name] = true
	}
	return NamesOf(ids...), oracle
}

const (
	diffBatches = 100 // of baselineTexts texts that hold kept names
	// diffSeeds keeps these seeds apart from those of the baseline.
	diffSeeds = 1_000_000
)

// eachKeptText calls check with the texts of a batch. Each holds a kept name somewhere.
func eachKeptText(batch int, check func(text string)) {
	texts := newDiffTexts(diffSeeds+int64(batch), diffKeptNames...)
	for done := 0; done < baselineTexts; {
		text := texts.text()
		holds := false
		for _, name := range diffKeptNames {
			holds = holds || strings.Contains(text, name)
		}
		if holds {
			check(text)
			done++
		}
	}
}

// longRunSpansWithoutNames is longRunSpans as it was before names were known.
func longRunSpansWithoutNames(text string) [][2]int {
	arns := arnSpan.FindAllStringIndex(text, -1)
	var spans [][2]int
	for _, m := range longRun.FindAllStringIndex(text, -1) {
		run := text[m[0]:m[1]]
		if isHarbormasterID(run) || !looksLikeRandomRun(run) {
			continue
		}
		if insideAny(arns, m) {
			continue
		}
		if hexOnly.MatchString(run) && hexRunIsDiagnostic(text, m[0]) {
			continue
		}
		spans = append(spans, [2]int{m[0], m[1]})
	}
	return spans
}

// The long-run rule finds what it found before, without the runs that are kept names, and nothing else changes.
func TestLongRunSpans_OnlyWholeKeptNamesBecomeVisible(t *testing.T) {
	keep, oracle := diffKept(t)
	var texts, shown atomic.Int64
	diffGroups(t, diffBatches, 4, func(t *testing.T, batch int) {
		eachKeptText(batch, func(text string) {
			old := longRunSpansWithoutNames(text)
			if now := longRunSpans(text, Names{}); !slices.Equal(old, now) {
				t.Errorf("batch %d: the empty set changes the spans of %q", batch, text)
			}
			want := old[:0:0]
			for _, span := range old {
				if !oracle[text[span[0]:span[1]]] {
					want = append(want, span)
				}
			}
			if now := longRunSpans(text, keep); !slices.Equal(now, want) {
				t.Errorf("batch %d: got %v, want %v in %q", batch, now, want, text)
			}
			texts.Add(1)
			shown.Add(int64(len(old) - len(want)))
		})
	})
	t.Logf("%d texts, %d runs became visible", texts.Load(), shown.Load())
	if shown.Load() == 0 {
		t.Error("no run became visible")
	}
}

// hiddenBytes marks the bytes of text that the edits replace.
func hiddenBytes(text string, edits []edit) []bool {
	hidden := make([]bool, len(text))
	for _, e := range edits {
		for i := e.start; i < e.end; i++ {
			hidden[i] = true
		}
	}
	return hidden
}

// nameSpans returns where a whole run of text is one of names, read with any number of levels of JSON escapes removed.
func nameSpans(text string, names map[string]bool) [][2]int {
	var spans [][2]int
	view, offsets := text, make([]int, len(text)+1)
	for i := range offsets {
		offsets[i] = i
	}
	for level := 0; level <= maxJSONDepth; level++ {
		for _, run := range longRun.FindAllStringIndex(view, -1) {
			if names[view[run[0]:run[1]]] {
				spans = append(spans, [2]int{offsets[run[0]], offsets[run[1]]})
			}
		}
		next, at := unescape(view)
		for i := range at {
			at[i] = offsets[at[i]]
		}
		view, offsets = next, at
	}
	return spans
}

// Keeping names never hides more, and each stretch that it shows is exactly one kept name. In text that is no JSON a
// name with \uXXXX inside can be shown in part: what another reading of the escapes takes for a longer run stays hidden.
func TestScrubTextKeeping_RevealsOnlyKeptNames(t *testing.T) {
	keep, oracle := diffKept(t)
	var texts, shown, inPart atomic.Int64
	diffGroups(t, diffBatches, 4, func(t *testing.T, batch int) {
		eachKeptText(batch, func(text string) {
			old, now := hiddenBytes(text, scrub(text, Names{}, 0)), hiddenBytes(text, scrub(text, keep, 0))
			names := nameSpans(text, oracle)
			for i := 0; i < len(text); {
				if now[i] && !old[i] {
					t.Errorf("batch %d: byte %d of %q is hidden only with the set", batch, i, text)
					return
				}
				if now[i] || !old[i] {
					i++
					continue
				}
				start := i
				for i < len(text) && old[i] && !now[i] {
					i++
				}
				// The stretch is a whole name, or a part: it lies inside one name that is written with escapes and was
				// hidden to its last byte before, and a byte beside the stretch is hidden still. Another reading of the
				// escapes took that byte for a longer run.
				whole := slices.Contains(names, [2]int{start, i})
				part := (start > 0 && now[start-1] || i < len(text) && now[i]) && slices.ContainsFunc(names, func(name [2]int) bool {
					return name[0] <= start && i <= name[1] && strings.IndexByte(text[name[0]:name[1]], '\\') >= 0 && !slices.Contains(old[name[0]:name[1]], false)
				})
				if !whole && !part {
					t.Errorf("batch %d: %q became visible in %q", batch, text[start:i], text)
				}
				shown.Add(1)
				if !whole {
					inPart.Add(1)
				}
			}
			if isJSON(text, `{["`) && !json.Valid([]byte(ScrubTextKeeping(text, keep))) {
				t.Errorf("batch %d: no longer JSON: %q", batch, text)
			}
			texts.Add(1)
		})
	})
	t.Logf("%d texts, %d stretches became visible, %d of them part of a name", texts.Load(), shown.Load(), inPart.Load())
	if shown.Load() == 0 {
		t.Error("nothing became visible")
	}
}

// A later pass only hides more, and it hides more only where it does so without names too.
func TestScrubTextKeeping_Idempotent(t *testing.T) {
	keep, _ := diffKept(t)
	var texts, compared atomic.Int64
	diffGroups(t, 20, 2, func(t *testing.T, batch int) {
		eachKeptText(diffBatches+batch, func(text string) {
			texts.Add(1)
			once := ScrubTextKeeping(text, keep)
			if ScrubTextKeeping(once, keep) == once {
				compared.Add(1)
				return
			}
			if plain := ScrubText(text); ScrubText(plain) == plain {
				t.Errorf("batch %d: scrubbing %q\ngave %q\nand then %q", batch, text, once, ScrubTextKeeping(once, keep))
			}
		})
	})
	t.Logf("%d texts, %d unchanged by a second pass", texts.Load(), compared.Load())
}

// The wrapped and encoded secrets stay hidden when the set holds names, also names that begin or end as they do.
func TestScrubTextKeeping_NeverUncoversCorpusSecrets(t *testing.T) {
	keep, _ := diffKept(t)
	for _, secret := range []string{fakeBase64URL, fakeMixed, fakeHM, "hunter2x"} {
		for _, name := range []string{
			secret[:len(secret)-1], secret[1:], secret + "-Xy7KpQ2mNvRtF3A9B2C1wD3k", "Xy7KpQ2mNvRtF3A9B2C1wD3k-" + secret, secret + secret + secret,
		} {
			keep.Add(diffServiceID("prod-cluster", name), diffServiceID(name, "checkout-api"))
		}
	}
	if got := ScrubTextKeeping(fakeBase64URL[1:], keep); got != fakeBase64URL[1:] {
		t.Fatalf("test setup: the end of a corpus secret is not kept: %q", got)
	}
	scrubText := func(text string) string { return ScrubTextKeeping(text, keep) }
	requireWrappingUncoversNothing(t, scrubText)
	requireEncodedSecretsHidden(t, scrubText)
}

// A set that ids give no name for is the empty set.
func TestScrubTextKeeping_EmptySetIsScrubText(t *testing.T) {
	empty := map[string]Names{
		"zero value": {}, "no ids": NamesOf(), "ordinary ids": NamesOf(diffServiceID("prod-cluster", "checkout-api")), "no id": NamesOf(diffKeptNames...),
	}
	texts := append([]string{}, baselineLeakRows...)
	for _, row := range leakCorpus() {
		texts = append(texts, row.text)
	}
	texts = append(texts, fakeAWSKey, fakeAWSTemp, fakeJWT, fakeSK, fakeAnthropic, fakeGitHub, fakeGitHubOA, fakeSlackBot, fakeSlackUser,
		fakeGoogle, fakeHM, fakeMixed, fakeHex40, fakeBase64URL, fakeBase64Slash)
	texts = append(texts, diffKeptNames...)
	for _, text := range texts {
		for label, keep := range empty {
			if !keep.Empty() {
				t.Errorf("%s: not empty", label)
			}
			for _, form := range []string{text, encode(t, text, 1), `{"name":` + encode(t, text, 2) + `}`} {
				if got, want := ScrubTextKeeping(form, keep), ScrubText(form); got != want {
					t.Errorf("%s: %q became %q, want %q", label, form, got, want)
				}
			}
		}
	}
}

// IDNames gives no name with a value pattern, so the condition of the set can be seen only here.
func TestMayBeKept_EachCondition(t *testing.T) {
	for name, want := range map[string]bool{
		diffKeptNames[0]:                  true,
		"Xy7KpQ2mNvRtF3A9B2C1wD3kQ9sLmN2": false, // 31 bytes
		"payments-settlement-reconciliation-background-worker-service": false,
		fakeHex40:                                     false,
		fakeGitHub + "A":                              false,
		"sk-" + fakeBase64URL:                         false,
		"x-" + fakeAWSKey + "-Xy7KpQ2mNvRt":           false,
		"hmp_" + "abcdefghijkl" + "_" + fakeBase64URL: false,
	} {
		if got := mayBeKept(name); got != want {
			t.Errorf("%q: got %v", name, got)
		}
	}
}

// Below the depth that JSON in JSON strings is walked to, the plain-text rules get the set too.
func TestScrubTextKeeping_BelowTheDepthLimit(t *testing.T) {
	name := diffKeptNames[0]
	keep := NamesOf(diffServiceID("prod-cluster", name))
	for depth := range maxJSONDepth + 3 {
		text := encode(t, name, depth)
		if got := ScrubTextKeeping(text, keep); got != text {
			t.Errorf("depth %d: %s became %s", depth, text, got)
		}
		if got := ScrubText(text); strings.Contains(got, name) {
			t.Errorf("depth %d, without the name: %s", depth, got)
		}
	}
}
