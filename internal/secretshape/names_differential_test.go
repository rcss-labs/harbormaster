package secretshape

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
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
// default only the first subtest runs.
func diffGroups(t *testing.T, batches int, check func(t *testing.T, batch int)) {
	t.Helper()
	for first := 0; first < batches; first += diffGroup {
		if first > 0 && !fullDiff() {
			return
		}
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
	diffGroups(t, baselineBatches, func(t *testing.T, batch int) {
		if got := baselineHash(batch, ScrubText); got != want[batch] {
			t.Errorf("batch %d: the output changed", batch)
		}
	})
}
