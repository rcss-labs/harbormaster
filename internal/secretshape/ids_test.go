package secretshape_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
)

const (
	idAccount = "123456789012"
	idRegion  = "eu-west-1"
)

var (
	// Fake secrets, assembled from pieces so scanners do not flag this file.
	idAWSKey   = "AKIA" + "IOSFODNN7EXAMPLE"
	idGitHub   = "ghp_" + "abcdefghijklmnopqrstuvwxyz0123456789"
	idRandom43 = "kQ9x_Tz3-Lm8vWb2Nc7Yd4Re1Uf6Hg0Js5Ap" + "Xo-Qi_Zr"
)

func serviceID(cluster, service string) string {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: idAccount, Region: idRegion, Cluster: cluster}, Service: service}.ID()
}

func clusterID(cluster string) string {
	return model.ClusterKey{AccountID: idAccount, Region: idRegion, Cluster: cluster}.ID()
}

func taskID(arn string) string { return model.TaskID(idAccount, idRegion, arn) }

func realIDs() map[string]string {
	long := strings.Repeat("payments-", 20) + "worker"
	return map[string]string{
		"service":                serviceID("prod-cluster", "checkout-api"),
		"cluster":                clusterID("prod-cluster"),
		"task, new ARN form":     taskID("arn:aws:ecs:" + idRegion + ":" + idAccount + ":task/prod-cluster/0123456789abcdef0123456789abcdef"),
		"task, old ARN form":     taskID("arn:aws:ecs:" + idRegion + ":" + idAccount + ":task/0123456789abcdef0123456789abcdef"),
		"task, ARN of a cluster": taskID("arn:aws:ecs:" + idRegion + ":" + idAccount + ":task/prod-cluster"),
		"CDK service":            serviceID("Infra-ClusterEB0386A7-wD3kQ9sLmN2v", "HarborStack-ApiServiceService7C1A2B3D-Xk3J9mLpQ2Rt"),
		"CloudFormation service": serviceID("prod-EcsCluster-AbCdEfGh1234", "prod-WebService-9ZyXwVuT8sRq"),
		"long names":             serviceID(long, long),
		"underscores":            serviceID("data_platform_cluster", "ingest_worker_v2"),
		"sovereign region":       model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: idAccount, Region: "eusc-de-east-1", Cluster: "c1"}, Service: "svc"}.ID(),
		"another region":         model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: idAccount, Region: "us-gov-west-1", Cluster: "c1"}, Service: "svc"}.ID(),
	}
}

func requireKept(t *testing.T, label, text, id string) {
	t.Helper()
	got := secretshape.ScrubText(text)
	if got != text {
		t.Errorf("%s: the id was changed:\n in  %.200s\n out %.200s", label, text, got)
	}
	if !strings.Contains(got, id) {
		t.Errorf("%s: the id is missing from %.200s", label, got)
	}
}

func requireRedacted(t *testing.T, label, text string, hidden ...string) {
	t.Helper()
	got := secretshape.ScrubText(text)
	if !strings.Contains(got, secretshape.Redacted) {
		t.Errorf("%s: nothing was redacted in %.200s", label, text)
	}
	for _, h := range hidden {
		if strings.Contains(got, h) {
			t.Errorf("%s: %.40q survived in %.200s", label, h, got)
		}
	}
}

func TestScrubText_HarbormasterIDsSurvive(t *testing.T) {
	for name, id := range realIDs() {
		t.Run(name, func(t *testing.T) {
			requireKept(t, "alone", id, id)
			requireKept(t, "sentence", "Use "+id+" next.", id)
			for _, field := range []string{"id", "service_id", "cluster_id", "task_id"} {
				requireKept(t, field, `{"`+field+`":"`+id+`","name":"x"}`, id)
			}
			requireKept(t, "list", `{"services":[{"id":"`+id+`"},{"id":"`+id+`"}]}`, id)
			requireKept(t, "plain list", id+"\n"+id+"\n", id)
			requireKept(t, "url path", "GET /api/services/"+id+"/logs", id)
			requireKept(t, "full url", "https://hm.example.test/services/"+id+"/logs?limit=5", id)
			inner, _ := json.Marshal(map[string]string{"service_id": id})
			outer, _ := json.Marshal(string(inner))
			requireKept(t, "escaped JSON string", string(outer), id)
			requireKept(t, "escaped fragment", `{\"service_id\":\"`+id+`\"}`, id)
		})
	}
}

func mustDecode(t *testing.T, id string) []byte {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// rawID builds the id text for fields that the model package would never produce.
func rawID(fields ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(fields, "|")))
}

func TestScrubText_IDShapedTokensThatAreNotIDsStayRedacted(t *testing.T) {
	good := serviceID("prod-cluster", "checkout-api")
	// The unused low bits of the last character set; the decoded bytes stay the same.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	oddLength := serviceID("prod-cluster", "checkout-ap") // a length that leaves trailing bits
	flipped := func(id string) string {
		mask := map[int]int{2: 15, 3: 3}[len(id)%4]
		if mask == 0 {
			t.Fatal("test setup: the id has no trailing bits")
		}
		return id[:len(id)-1] + string(alphabet[strings.IndexByte(alphabet, id[len(id)-1])|mask])
	}
	if loose, err := base64.RawURLEncoding.DecodeString(flipped(oddLength)); err != nil || string(loose) != string(mustDecode(t, oddLength)) {
		t.Fatalf("test setup: the flipped id must decode loosely to the same bytes: %v", err)
	}
	wide := strings.Repeat("a", 256)
	tests := map[string]string{
		"11 digit account":       rawID("12345678901", idRegion, "prod-cluster", "checkout-api"),
		"13 digit account":       rawID("1234567890123", idRegion, "prod-cluster", "checkout-api"),
		"letters in account":     rawID("12345678901x", idRegion, "prod-cluster", "checkout-api"),
		"region with a capital":  rawID(idAccount, "eu-West-1", "prod-cluster", "checkout-api"),
		"region with underscore": rawID(idAccount, "eu_west_1", "prod-cluster", "checkout-api"),
		"region of 31":           rawID(idAccount, strings.Repeat("a", 31), "prod-cluster", "checkout-api"),
		"empty region":           rawID(idAccount, "", "prod-cluster", "checkout-api"),
		"region in capitals":     rawID(idAccount, "EU-WEST-1", "prod-cluster", "checkout-api"),
		"five fields":            rawID(idAccount, idRegion, "prod-cluster", "checkout-api", "extra-field"),
		"two fields":             rawID(idAccount, idRegion+"-padding-padding-padding"),
		"empty cluster":          rawID(idAccount, idRegion, "", "checkout-api"),
		"empty service":          rawID(idAccount, idRegion, "prod-cluster", ""),
		"NUL in a name":          rawID(idAccount, idRegion, "prod-cluster", "checkout\x00api"),
		"non-ASCII name":         rawID(idAccount, idRegion, "prod-cluster", "checkout-api-é-é"),
		"space in a name":        rawID(idAccount, idRegion, "prod-cluster", "checkout api service"),
		"slash in a name":        rawID(idAccount, idRegion, "prod-cluster", "checkout/api/service"),
		"cluster too long":       rawID(idAccount, idRegion, wide+"a", "checkout-api"),
		"service too long":       rawID(idAccount, idRegion, "prod-cluster", wide+"a"),
		"ARN of another region":  rawID(idAccount, idRegion, "arn:aws:ecs:us-east-1:"+idAccount+":task/prod-cluster/0123456789abcdef0123456789abcdef"),
		"ARN of another acct":    rawID(idAccount, idRegion, "arn:aws:ecs:"+idRegion+":999999999999:task/prod-cluster/0123456789abcdef0123456789abcdef"),
		"ARN of a service":       rawID(idAccount, idRegion, "arn:aws:ecs:"+idRegion+":"+idAccount+":service/prod-cluster/checkout-api-0123456789"),
		"ARN with a bad task":    rawID(idAccount, idRegion, "arn:aws:ecs:"+idRegion+":"+idAccount+":task/prod-cluster/NOTHEX0123456789abcdef0123456"),
		"ARN with a long tail":   rawID(idAccount, idRegion, "arn:aws:ecs:"+idRegion+":"+idAccount+":task/prod-cluster/0123456789abcdef/extra/segments"),
		"cluster id, bad name":   rawID(idAccount, idRegion, "prod cluster name"),
		"cluster id, slash":      rawID(idAccount, idRegion, "prod/cluster/name"),
		"cluster id, colon":      rawID(idAccount, idRegion, "prod:cluster:name:x"),
		"cluster id, too long":   rawID(idAccount, idRegion, wide+"a"),
		"task longer than 2048":  rawID(idAccount, idRegion, "arn:aws:ecs:"+idRegion+":"+idAccount+":task/"+strings.Repeat("payments-", 300)+"worker"),
		"padding":                good + "=",
		"padding on odd length":  oddLength + "=",
		"trailing bits set":      flipped(oddLength),
		"not base64url":          strings.Replace(good, good[10:11], "+", 1),
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			requireRedacted(t, "alone", token, token)
			requireRedacted(t, "json", `{"id":"`+token+`"}`, token)
			requireRedacted(t, "path", "/services/"+token+"/logs", token)
		})
	}
}

func TestScrubText_IDHoldingASecretStaysRedacted(t *testing.T) {
	tests := map[string]string{
		"AWS key as service": serviceID("prod-cluster", idAWSKey),
		"AWS key as cluster": serviceID(idAWSKey, "checkout-api"),
		"GitHub token":       serviceID("prod-cluster", idGitHub),
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			requireRedacted(t, "alone", token, token)
			requireRedacted(t, "json", `{"service_id":"`+token+`"}`, token)
			requireRedacted(t, "path", "/services/"+token+"/logs", token)
		})
	}
}

// Names are judged by the value patterns only: CDK and CloudFormation give services long random-looking names.
func TestScrubText_RandomLookingNamesKeepTheirIDs(t *testing.T) {
	for name, id := range map[string]string{
		"43 character service": serviceID("prod-cluster", idRandom43),
		"43 character cluster": clusterID(idRandom43),
		"random task name":     taskID("arn:aws:ecs:" + idRegion + ":" + idAccount + ":task/" + idRandom43),
	} {
		t.Run(name, func(t *testing.T) { requireKept(t, name, `{"id":"`+id+`"}`, id) })
	}
}

func TestValueLooksSecret_IDs(t *testing.T) {
	id := serviceID("prod-cluster", "checkout-api")
	if secretshape.ValueLooksSecret(id) {
		t.Errorf("an id alone is not secret")
	}
	if !secretshape.ValueLooksSecret(serviceID("prod-cluster", idAWSKey)) {
		t.Errorf("an id holding a key is secret")
	}
	env := `{"environment":[{"name":"SERVICE_ID","value":"` + id + `"},{"name":"DB_PASSWORD","value":"` + id + `"}]}`
	got := secretshape.ScrubText(env)
	if !strings.Contains(got, `"SERVICE_ID","value":"`+id) || strings.Count(got, id) != 1 || !strings.Contains(got, `"DB_PASSWORD","value":"[redacted]"`) {
		t.Errorf("env list: %s", got)
	}
}

func TestScrubText_IDNamedAsASecretStaysRedacted(t *testing.T) {
	id := serviceID("prod-cluster", "checkout-api")
	for name, text := range map[string]string{
		"json password": `{"password":"` + id + `"}`,
		"json token":    `{"api_token":"` + id + `"}`,
		"assignment":    "token=" + id,
		"authorization": "Authorization: " + id,
		"env pair":      `{"name":"DB_PASSWORD","value":"` + id + `"}`,
		"escaped":       `{\"password\":\"` + id + `\"}`,
		"userinfo":      "postgres://user:" + id + "@db.example.test/app",
	} {
		t.Run(name, func(t *testing.T) { requireRedacted(t, name, text, id) })
	}
}

func TestScrubText_RandomTokenGluedToAnIDStaysRedacted(t *testing.T) {
	id := serviceID("prod-cluster", "checkout-api")
	for name, text := range map[string]string{
		"slash after":  id + "/" + idRandom43,
		"slash before": idRandom43 + "/" + id,
		"dash after":   id + "-" + idRandom43,
		"dash before":  idRandom43 + "-" + id,
		"underscore":   id + "_" + idRandom43,
		"underscore 2": idRandom43 + "_" + id,
		"in a path":    "/services/" + id + "/" + idRandom43 + "/logs",
		"two ids":      id + "/" + serviceID("prod-cluster", "worker") + "/" + idRandom43,
		"id stuck on":  id + "A",
		"id with tail": id + idRandom43,
	} {
		t.Run(name, func(t *testing.T) { requireRedacted(t, name, text, idRandom43) })
	}
	if got := secretshape.ScrubText(id + "/" + idRandom43); got != secretshape.Redacted {
		t.Errorf("the whole run should go, got %q", got)
	}
}

// A run with an id is kept only when every other piece is plain; otherwise the whole run is judged as it always was.
func TestScrubText_RunsBesideAnIDKeepOnlyPlainPieces(t *testing.T) {
	id := serviceID("prod-cluster", "checkout-api")
	other := serviceID("prod-cluster", "worker")
	hex64 := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	hex40 := hex64[:40]
	upper40 := strings.ToUpper(hex40)
	random := func(n int) string { return (idRandom43 + idRandom43)[:n] }
	for name, text := range map[string]string{
		"64 hex in a path":   "/services/" + id + "/" + hex64,
		"64 hex after":       id + "/" + hex64 + "/",
		"64 hex before":      hex64 + "/" + id,
		"40 capital hex":     "/" + id + "/" + upper40,
		"40 hex after":       id + "/" + hex40,
		"40 hex before":      hex40 + "/" + id,
		"31 random after":    id + "/" + random(31),
		"31 random before":   random(31) + "/" + id,
		"25 random after":    id + "/" + random(25),
		"25 random before":   random(25) + "/" + id,
		"20 random after":    id + "/" + random(20),
		"20 random before":   random(20) + "/" + id,
		"random between ids": id + "/" + random(25) + "/" + other,
		"long words":         id + "/" + strings.Repeat("payments-", 8) + "worker",
		"hex of 33":          id + "/" + hex64[:33],
		"hex of 31":          id + "/" + hex64[:31],
		"43 random after":    id + "/" + idRandom43,
	} {
		t.Run(name, func(t *testing.T) { requireRedacted(t, name, text, id) })
	}
	uuid := "123e4567-e89b-12d3-a456-426614174000"
	for name, text := range map[string]string{
		"logs path":           "/services/" + id + "/logs",
		"task path":           "/services/" + id + "/tasks/" + hex64[:32],
		"cluster and service": clusterID("prod-cluster") + "/" + id,
		"uuid beside":         id + "/" + uuid,
		"uuid before":         uuid + "/" + id,
		"short piece":         id + "/" + random(15),
		"words":               "/api/services/" + id + "/task-definition/revisions",
		"trailing slash":      "/services/" + id + "/",
	} {
		t.Run(name, func(t *testing.T) { requireKept(t, name, text, id) })
	}
}

func TestScrubText_SeveralIDsInOneRun(t *testing.T) {
	a, b := serviceID("prod-cluster", "checkout-api"), serviceID("prod-cluster", "worker")
	requireKept(t, "two ids", "/services/"+a+"/compare/"+b+"/logs", a)
	requireKept(t, "two ids in a list", `["`+a+`","`+b+`"]`, b)
}

// 200 KB of id-like text may not take quadratic time.
func TestScrubText_IDLikeTextIsLinear(t *testing.T) {
	good := serviceID("prod-cluster", "checkout-api")
	build := map[string]func(int) string{
		"ids joined by slashes": func(n int) string { return strings.Repeat(good+"/", n/(len(good)+1)) },
		"ids in a path":         func(n int) string { return "/services/" + strings.Repeat(good+"/", n/(len(good)+1)) + "logs" },
		"one long id-like run":  func(n int) string { return strings.Repeat("MTIzNDU2Nzg5MDEy", n/16) },
		"short pieces":          func(n int) string { return strings.Repeat("MTIzNDU2Nzg5MDEy/", n/17) },
		"2048 character pieces": func(n int) string { return strings.Repeat(strings.Repeat("MTIz", 512)+"/", n/2049) },
		"2049 character pieces": func(n int) string { return strings.Repeat(strings.Repeat("MTIz", 512)+"M/", n/2050) },
		"slashes":               func(n int) string { return strings.Repeat("/", n) },
		"id then junk":          func(n int) string { return strings.Repeat(good+"/Zz9-", n/(len(good)+5)) },
	}
	timeOf := func(text string) time.Duration {
		start := time.Now()
		secretshape.ScrubText(text)
		return time.Since(start)
	}
	for name, generate := range build {
		t.Run(name, func(t *testing.T) {
			small, large := generate(50_000), generate(200_000)
			var ratio float64
			for attempt := 0; attempt < 6; attempt++ {
				ratio = float64(timeOf(large)) / float64(max(timeOf(small), time.Microsecond))
				if ratio < 9 {
					return
				}
			}
			t.Errorf("200 KB took %.1f times as long as 50 KB", ratio)
		})
	}
}
