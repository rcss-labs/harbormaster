package secretshape_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

const (
	// Names as CDK makes them: the scrubber takes them for random tokens.
	cdkService = "CheckoutStack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNvRt"
	cdkCluster = "Infra-ClusterEB0386A7-wD3kQ9sLmN2v"
	cdkWorker  = "CheckoutStack-WorkerServiceA71B04D9-pL8sVw3TzKc5"
)

// keptOf gives the names of a service and its cluster.
func keptOf(services ...string) secretshape.Names {
	var keep secretshape.Names
	for _, service := range services {
		keep.Add(serviceID(cdkCluster, service))
	}
	return keep
}

func requireNameKept(t *testing.T, label, text string, keep secretshape.Names) {
	t.Helper()
	if secretshape.ScrubText(text) == text {
		t.Fatalf("%s: test setup: nothing in %q is hidden without the names", label, text)
	}
	if got := secretshape.ScrubTextKeeping(text, keep); got != text {
		t.Errorf("%s: %q became %q", label, text, got)
	}
}

// requireNameHidden requires that the names change nothing: text holds one of them, hidden with and without the set.
func requireNameHidden(t *testing.T, label, text string, keep secretshape.Names, hidden string) {
	t.Helper()
	got := secretshape.ScrubTextKeeping(text, keep)
	if want := secretshape.ScrubText(text); got != want {
		t.Errorf("%s: %q became %q, without the names %q", label, text, got, want)
	}
	if !strings.Contains(text, hidden) {
		t.Fatalf("%s: test setup: %q is not in %q", label, hidden, text)
	}
	if strings.Contains(got, hidden) || !strings.Contains(got, secretshape.Redacted) {
		t.Errorf("%s: %q survived in %q", label, hidden, got)
	}
}

func TestNamesOf_KeepsOnlyRandomLookingNamesFromValidIDs(t *testing.T) {
	const random31 = "Xy7KpQ2mNvRtF3A9B2C1wD3kQ9sLmN2"
	const hex64 = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	wordy := "payments-settlement-reconciliation-background-worker-service"
	if len(random31) != 31 || len(wordy) != 60 {
		t.Fatalf("test setup: lengths %d and %d", len(random31), len(wordy))
	}
	keep := secretshape.NamesOf(serviceID(cdkCluster, cdkService))
	if keep.Empty() {
		t.Error("a CDK service gives no name")
	}
	requireNameKept(t, "service", cdkService, keep)
	requireNameKept(t, "cluster", cdkCluster, keep)
	requireNameKept(t, "cluster of a cluster id", cdkCluster, secretshape.NamesOf(clusterID(cdkCluster)))
	requireNameKept(t, "32 bytes", random31+"v", secretshape.NamesOf(serviceID("prod-cluster", random31+"v")))

	task := secretshape.NamesOf(taskID("arn:aws:ecs:" + idRegion + ":" + idAccount + ":task/" + cdkCluster + "/" + cdkWorker))
	requireNameKept(t, "cluster of a task", cdkCluster, task)
	requireNameHidden(t, "task of a task id", cdkWorker, task, cdkWorker)
	if !secretshape.NamesOf(taskID("arn:aws:ecs:" + idRegion + ":" + idAccount + ":task/" + cdkWorker)).Empty() {
		t.Error("a task id without a cluster gave a name")
	}

	none := map[string][]string{
		"nothing":        nil,
		"ordinary names": {serviceID("prod-cluster", "checkout-api"), clusterID("prod-cluster")},
		"31 bytes":       {serviceID("prod-cluster", random31)},
		"40 hex":         {serviceID("prod-cluster", hex64[:40])},
		"64 hex":         {serviceID("prod-cluster", hex64), clusterID(hex64)},
		"wordy":          {serviceID("prod-cluster", wordy)},
		"AWS key":        {serviceID(cdkCluster, idAWSKey), serviceID(idAWSKey, cdkService), clusterID(idAWSKey)},
		"GitHub token":   {serviceID(cdkCluster, idGitHub), clusterID(idGitHub)},
		"sk key":         {serviceID(cdkCluster, "sk-"+idRandom43)},
		"own token":      {serviceID(cdkCluster, "hmp_"+"abcdefghijkl"+"_"+idRandom43)},
		"a bare name":    {cdkService, cdkService + " ", "prod-cluster|" + cdkService, idAccount + "|" + idRegion + "|prod-cluster|" + cdkService},
		"a list":         {serviceID("prod-cluster", cdkService) + "," + serviceID("prod-cluster", cdkWorker)},
	}
	for name, id := range refusedIDs(t, cdkService) {
		none["refused: "+name] = []string{id}
	}
	for name, ids := range none {
		keep := secretshape.NamesOf(ids...)
		if !keep.Empty() {
			t.Errorf("%s: a name is kept", name)
		}
		if got := secretshape.ScrubTextKeeping(cdkService, keep); got != secretshape.Redacted {
			t.Errorf("%s: got %q", name, got)
		}
	}
	if !(secretshape.Names{}).Empty() {
		t.Error("the zero value is not empty")
	}
}

func manyNames(n int) (names, ids []string) {
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("CheckoutStack-CheckoutServiceF3A9B2C1-Xy7KpQ2mNvR%05d", i)
		names, ids = append(names, name), append(ids, serviceID("prod-cluster", name))
	}
	return names, ids
}

func TestNames_AtMostTenThousand(t *testing.T) {
	names, ids := manyNames(10_001)
	// A name that is already kept takes no place.
	keep := secretshape.NamesOf(append([]string{ids[0], ids[0]}, ids...)...)
	requireNameKept(t, "name 10,000", names[9_999], keep)
	requireNameHidden(t, "name 10,001", names[10_000], keep, names[10_000])
	keep.Add(serviceID(cdkCluster, cdkService))
	requireNameHidden(t, "later name", cdkService, keep, cdkService)
	keep.Add(ids[0])
	requireNameKept(t, "first name", names[0], keep)
}

// A copy handed on does not learn what is added to the original later.
func TestNames_ACopyDoesNotGrow(t *testing.T) {
	keep := keptOf(cdkService)
	handed := keep
	keep.Add(serviceID(cdkCluster, cdkWorker))
	requireNameKept(t, "original", cdkWorker, keep)
	requireNameHidden(t, "copy", cdkWorker, handed, cdkWorker)
}

func TestScrubTextKeeping_WholeRunsOnly(t *testing.T) {
	keep := keptOf(cdkService, cdkWorker)
	inner, _ := json.Marshal(map[string]string{"service": cdkService})
	nested, _ := json.Marshal(map[string]string{"message": string(inner)})
	twice, _ := json.Marshal(string(nested))
	for label, text := range map[string]string{
		"alone":             cdkService,
		"cluster":           cdkCluster,
		"prose":             "The service " + cdkService + " is down.",
		"quoted":            `The service "` + cdkService + `" is down`,
		"single quotes":     "service '" + cdkService + "' is down",
		"parentheses":       "(service " + cdkService + ")",
		"JSON value":        `{"name":"` + cdkService + `","desired":2}`,
		"JSON in a list":    `{"services":["` + cdkService + `","` + cdkWorker + `"]}`,
		"JSON member name":  `{"` + cdkService + `":{"status":"down"}}`,
		"JSON lines":        "{\"name\":\"" + cdkService + "\"}\n{\"name\":\"" + cdkWorker + "\"}\n",
		"nested JSON":       string(nested),
		"nested twice":      string(twice),
		"line break inside": `{"message":"service\n` + cdkService + `\nis down"}`,
		"unicode escape":    `{"name":"\u0043` + cdkService[1:] + `"}`,
		"escaped quotes":    `{\"name\":\"` + cdkService + `\"}`,
		"cut JSON":          `{"services":[{"name":"` + cdkService + `","des`,
		"after a colon":     "service:" + cdkService,
		"before a stop":     "It is " + cdkService + ".",
		"before a comma":    cdkService + ", " + cdkWorker,
		"before a port":     "http to " + cdkService + ":8080 failed",
		"two lines":         cdkService + "\n" + cdkCluster + "\n",
	} {
		requireNameKept(t, label, text, keep)
	}

	otherCase := strings.Replace(cdkService, "Xy7", "xy7", 1)
	glued := map[string]string{
		"path":               "/services/" + cdkService + "/logs",
		"path before":        "cluster/" + cdkService,
		"path after":         cdkService + "/logs",
		"dash before":        "a-" + cdkService,
		"dash after":         cdkService + "-1",
		"trailing dash":      cdkService + "-",
		"leading dash":       "-" + cdkService,
		"underscore before":  "x_" + cdkService,
		"underscore after":   cdkService + "_",
		"plus before":        "+" + cdkService,
		"plus after":         cdkService + "+1",
		"equals":             cdkService + "=",
		"two equals":         cdkService + "==",
		"assignment":         cdkService + "=up",
		"letter after":       cdkService + "a",
		"letter before":      "a" + cdkService,
		"digit after":        cdkService + "0",
		"digit before":       "0" + cdkService,
		"escape in cut text": `{"message":"service\n` + cdkService + `\nis`,
		"two names, dash":    cdkService + "-" + cdkWorker,
		"two names, slash":   cdkCluster + "/" + cdkService,
		"two names, glued":   cdkService + cdkWorker,
	}
	for _, glue := range []string{"", "/", "-", "_", "+"} {
		glued["token after "+glue] = cdkService + glue + idRandom43
		glued["token before "+glue] = idRandom43 + glue + cdkService
		glued["between two names "+glue] = cdkService + glue + idRandom43 + glue + cdkWorker
	}
	for label, text := range glued {
		for wrapped, form := range map[string]string{"": text, " in prose": "see " + text + " here", " in JSON": `{"name":` + jsonText(t, text) + `}`} {
			if label == "escape in cut text" && wrapped == " in JSON" {
				continue // the string of valid JSON is read decoded, where nothing is glued
			}
			requireNameHidden(t, label+wrapped, form, keep, cdkService)
		}
	}
	for label, text := range map[string]string{
		"other case":  otherCase,
		"prefix":      cdkService[:len(cdkService)-1],
		"suffix":      cdkService[1:],
		"middle part": cdkService[2 : len(cdkService)-2],
	} {
		requireNameHidden(t, label, text, keep, text)
		requireNameHidden(t, label+" in JSON", `{"name":"`+text+`"}`, keep, text)
	}
	// A token that only stands beside a name goes, the name stays.
	for _, between := range []string{" ", ", ", ":", "\n", `" "`} {
		for _, text := range []string{cdkService + between + idRandom43, idRandom43 + between + cdkService} {
			want := strings.Replace(text, idRandom43, secretshape.Redacted, 1)
			if got := secretshape.ScrubTextKeeping(text, keep); got != want {
				t.Errorf("%q became %q", text, got)
			}
		}
	}
}

func jsonText(t *testing.T, text string) string {
	t.Helper()
	raw, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestScrubTextKeeping_OtherRulesStillWin(t *testing.T) {
	keep := keptOf(cdkService)
	for label, text := range map[string]string{
		"assignment":        "password=" + cdkService,
		"token":             "token: " + cdkService,
		"json token":        `{"token":"` + cdkService + `"}`,
		"json password":     `{"password":"` + cdkService + `"}`,
		"json object":       `{"credentials":{"user":"` + cdkService + `"}}`,
		"json secrets":      `{"secrets":{"db":"` + cdkService + `"}}`,
		"json list":         `{"api_keys":["` + cdkService + `"]}`,
		"plain list":        "API_KEYS=[" + cdkService + "]",
		"env pair":          `{"name":"DB_PASSWORD","value":"` + cdkService + `"}`,
		"env pair, reverse": `{"value":"` + cdkService + `","name":"DB_PASSWORD"}`,
		"env pair, cut":     `[{"name":"DB_PASSWORD","value":"` + cdkService + `"`,
		"escaped":           `{\"password\":\"` + cdkService + `\"}`,
		"nested":            `{"message":"{\"password\":\"` + cdkService + `\"}"}`,
		"bracketed":         "user[password]=" + cdkService,
		"query":             "https://x.example.test/v1?token=" + cdkService + "&a=1",
		"bearer":            "sent Bearer " + cdkService,
		"authorization":     "Authorization: Bearer " + cdkService,
		"json bearer":       `{"header":"Bearer ` + cdkService + `"}`,
		"url password":      "postgres://app:" + cdkService + "@db.example.test/app",
		"json url password": `{"url":"postgres://app:` + cdkService + `@db.example.test/app"}`,
	} {
		requireNameHidden(t, label, text, keep, cdkService)
	}

	for _, tc := range []struct{ service, verdict string }{
		{cdkService, "x"},
		{"CheckoutStack-CheckoutF3A9B2C1-Xy7KpQ2mNvRt-password", secretshape.Redacted},
	} {
		keep := keptOf(tc.service)
		plain := `{"name":"` + tc.service + `","value":"x"}`
		if got, want := secretshape.ScrubTextKeeping(plain, keep), `{"name":"`+tc.service+`","value":"`+tc.verdict+`"}`; got != want {
			t.Errorf("label %s: got %s", tc.service, got)
		}
		if got, want := secretshape.ScrubText(plain), `{"name":"`+secretshape.Redacted+`","value":"`+tc.verdict+`"}`; got != want {
			t.Errorf("label %s without the set: got %s", tc.service, got)
		}
	}
	if !secretshape.ValueLooksSecret(cdkService) {
		t.Error("ValueLooksSecret knows no names")
	}
}

func TestScrubTextKeeping_NamesOfOtherIDsStayRedacted(t *testing.T) {
	keep := keptOf(cdkService)
	for label, text := range map[string]string{
		"another service":     cdkWorker,
		"its id beside it":    serviceID(cdkCluster, cdkWorker) + " " + cdkWorker,
		"a forged id's name":  `{"tags":[{"key":"id","value":"` + serviceID("c", cdkWorker) + `"}],"name":"` + cdkWorker + `"}`,
		"an unattributed row": `{"items":[{"name":"` + cdkWorker + `","cluster":"x"}]}`,
	} {
		requireNameHidden(t, label, text, keep, cdkWorker)
	}
	mixed := `{"items":[{"name":"` + cdkService + `"},{"name":"` + cdkWorker + `"}]}`
	want := `{"items":[{"name":"` + cdkService + `"},{"name":"` + secretshape.Redacted + `"}]}`
	if got := secretshape.ScrubTextKeeping(mixed, keep); got != want {
		t.Errorf("got %s", got)
	}
}

func TestNamedIn_WholeRunsOnly(t *testing.T) {
	service, worker, cluster := serviceID("prod-cluster", cdkService), serviceID("prod-cluster", cdkWorker), clusterID(cdkCluster)
	inCluster := serviceID(cdkCluster, "checkout-api")
	ordinary := serviceID("prod-cluster", "checkout-api")
	keep := secretshape.NamesOf(service, worker, cluster)
	ids := []string{worker, service, ordinary, worker, cluster, "no id", cdkService}
	both := "checkout-api: " + cdkService + " and " + cdkWorker + "."
	for _, tc := range []struct {
		label, text string
		keep        secretshape.Names
		ids, want   []string
	}{
		{"in the order given, once", both, keep, ids, []string{worker, service}},
		{"one", "It is " + cdkService, keep, ids, []string{service}},
		{"cluster of a service id", "in " + cdkCluster, keep, append(ids, inCluster), []string{cluster, inCluster}},
		{"nothing named", "checkout-api is down", keep, ids, nil},
		{"no text", "", keep, ids, nil},
		{"no ids", both, keep, nil, nil},
		{"not in the set", both, secretshape.NamesOf(service), ids, []string{service}},
		{"empty set", both, secretshape.Names{}, ids, nil},
		{"glued", "/services/" + cdkService + "/logs " + cdkWorker + "-1 x" + cdkWorker, keep, ids, nil},
		{"one whole, one glued", cdkService + " and " + cdkWorker + "-1 " + cdkCluster + "/a", keep, ids, []string{service}},
		{"equals", cdkService + "=", keep, ids, nil},
		{"part", cdkService[:len(cdkService)-1] + " " + cdkService[1:], keep, ids, nil},
		{"case", strings.Replace(cdkService, "Xy7", "xy7", 1), keep, ids, nil},
		{"forged id", cdkService, keep, []string{serviceID("prod-cluster", idAWSKey), service + "="}, nil},
	} {
		if got := tc.keep.NamedIn(tc.text, tc.ids...); !slices.Equal(got, tc.want) {
			t.Errorf("%s: got %d ids, want %d: %q", tc.label, len(got), len(tc.want), got)
		}
	}
}

// 200 KB of name-like and id-like text may not take quadratic time, whatever the size of the set.
func TestScrubTextKeeping_Linear(t *testing.T) {
	names, ids := manyNames(10_000)
	keep := secretshape.NamesOf(ids...)
	requireNameKept(t, "set", names[1], keep)
	repeat := func(piece string) func(int) string {
		return func(n int) string { return strings.Repeat(piece, n/len(piece)) }
	}
	build := map[string]func(int) string{
		"kept names":            repeat(names[0] + " "),
		"glued names":           repeat(names[0] + "/"),
		"names that are near":   repeat(names[0][1:] + " "),
		"names and ids":         repeat(names[0] + " " + ids[0] + "\n"),
		"names stuck together":  repeat(names[0]),
		"JSON of names":         func(n int) string { return `["` + repeat(names[0]+`","`)(n) + `"]` },
		"escaped JSON of names": repeat(`\"name\":\"` + names[0] + `\",`),
		"names and equals":      repeat(names[0] + "="),
	}
	timeOf := func(work func()) time.Duration {
		start := time.Now()
		work()
		return time.Since(start)
	}
	for name, generate := range build {
		t.Run(name, func(t *testing.T) {
			small, large := generate(50_000), generate(200_000)
			for label, work := range map[string]func(string){
				"scrub": func(text string) { secretshape.ScrubTextKeeping(text, keep) },
				"named": func(text string) { keep.NamedIn(text, ids[:100]...) },
			} {
				var ratio float64
				for attempt := 0; attempt < 6; attempt++ {
					ratio = float64(timeOf(func() { work(large) })) / float64(max(timeOf(func() { work(small) }), 100*time.Microsecond))
					if ratio < 9 {
						break
					}
				}
				if ratio >= 9 {
					t.Errorf("%s: 200 KB took %.1f times as long as 50 KB", label, ratio)
				}
			}
		})
	}
}
