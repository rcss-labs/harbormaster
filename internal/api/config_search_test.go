package api

import (
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const plantedEnvValue = "v4lue-must-not-leak-1b2c3d"

func getConfigSearch(t *testing.T, c *apiClient, q, extra string) ConfigSearchResponse {
	t.Helper()
	path := "/api/config-search?q=" + url.QueryEscape(q)
	if extra != "" {
		path += "&" + extra
	}
	resp := c.get(path)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d: %s", path, resp.StatusCode, body)
	}
	var out ConfigSearchResponse
	decode(t, resp, &out)
	return out
}

func configItemNames(r ConfigSearchResponse) []string {
	names := []string{}
	for _, item := range r.Items {
		names = append(names, item.Name)
	}
	sort.Strings(names)
	return names
}

func configItem(t *testing.T, r ConfigSearchResponse, name string) ConfigSearchItemDTO {
	t.Helper()
	for _, item := range r.Items {
		if item.Name == name {
			return item
		}
	}
	t.Fatalf("service %q not in %v", name, configItemNames(r))
	return ConfigSearchItemDTO{}
}

func sameNames(got []string, want ...string) bool {
	sort.Strings(want)
	return strings.Join(got, ",") == strings.Join(want, ",")
}

// plantValues puts a unique environment value on auth-service's definitions before the poller caches them.
func plantValues(f *fake.Fake) {
	f.Estate.Mu.Lock()
	defer f.Estate.Mu.Unlock()
	for i := range f.Estate.TaskDefinitions {
		def := &f.Estate.TaskDefinitions[i].Definition
		if aws.ToString(def.Family) != "auth-service" {
			continue
		}
		main := &def.ContainerDefinitions[0]
		main.Environment = append(main.Environment, ecstypes.KeyValuePair{Name: aws.String("PLANTED_NAME"), Value: aws.String(plantedEnvValue)})
	}
}

func newConfigSearchServer(t *testing.T) (srvClient func(user string) *apiClient, fk *fake.Fake, st *store.Store) {
	t.Helper()
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	plantValues(fk)
	pollAll(t, st, fk)
	return func(user string) *apiClient { return login(t, srv, user, testPassword) }, fk, st
}

func TestConfigSearch_FindsByEachField(t *testing.T) {
	as, _, _ := newConfigSearchServer(t)
	sam := as("sam")
	all := getServicesList(t, sam, "")

	cases := []struct {
		query         string
		want          []string // nil means every service
		field, valueP string   // a match entry that must exist on the first service
	}{
		{"image:ecr.eu-central-1 edge-nginx", []string{"edge-nginx"}, "image", "edge-nginx:"},
		{"tag:1.19.0-rc.2", []string{"api"}, "tag", "1.19.0-rc.2"},
		{"env:TOKEN_TTL", []string{"auth-service"}, "env", "TOKEN_TTL"},
		{"secret:JWT_SIGNING_KEY", []string{"auth-service"}, "secret", "JWT_SIGNING_KEY"},
		{"secret:DATABASE_URL", []string{"api", "auth-service"}, "secret", "DATABASE_URL"},
		{"port:9000", []string{"auth-service"}, "port", "9000"},
		{"taskrole:auth-service-task", []string{"auth-service"}, "taskrole", "arn:aws:iam::482109375512:role/auth-service-task"},
		{"role:auth-service-task", []string{"auth-service"}, "taskrole", "arn:aws:iam::482109375512:role/auth-service-task"},
		{"execrole:ecs-task-execution", nil, "execrole", ""},
		{"loggroup:/ecs/core-services/auth-service", []string{"auth-service"}, "loggroup", "/ecs/core-services/auth-service"},
		{"ISSUER", []string{"auth-service"}, "env", "ISSUER"},
	}
	for _, c := range cases {
		got := getConfigSearch(t, sam, c.query, "")
		want := c.want
		if want == nil {
			for _, item := range all.Items {
				want = append(want, item.Name)
			}
		}
		if !sameNames(configItemNames(got), want...) {
			t.Errorf("%q: services = %v, want %v", c.query, configItemNames(got), want)
			continue
		}
		found := false
		for _, m := range got.Items[0].Matches {
			if string(m.Field) == c.field && (c.valueP == "" || strings.Contains(m.Value, c.valueP)) {
				found = true
			}
		}
		if !found {
			t.Errorf("%q: first item matches = %+v, want field %s value %q", c.query, got.Items[0].Matches, c.field, c.valueP)
		}
	}
}

func TestConfigSearch_BaseImageFromRepositorySetting(t *testing.T) {
	as, _, st := newConfigSearchServer(t)
	sam := as("sam")
	key := store.RepoKey{AccountID: homelabAccountID, Region: "us-east-1", Repository: "payments-api"}
	if err := st.SetRepoBase(t.Context(), key, "node:22-alpine", 0, time.Now()); err != nil {
		t.Fatal(err)
	}

	got := getConfigSearch(t, sam, "base:node:22", "")
	if !sameNames(configItemNames(got), "payments-api") {
		t.Fatalf("base:node:22 services = %v", configItemNames(got))
	}
	byName := map[string]ConfigContainerDTO{}
	for _, c := range got.Items[0].Containers {
		byName[c.Name] = c
	}
	if byName["payments-api"].BaseImage != "node:22-alpine" {
		t.Errorf("main base_image = %q", byName["payments-api"].BaseImage)
	}
	if byName["otel-collector"].BaseImage != "" {
		t.Errorf("non-ECR sidecar base_image = %q, want empty", byName["otel-collector"].BaseImage)
	}
	// the seeded bases are found too, and a service without one has an empty base
	seeded := getConfigSearch(t, sam, "base:node:18", "")
	if !sameNames(configItemNames(seeded), "web-frontend", "web") {
		t.Errorf("base:node:18 services = %v", configItemNames(seeded))
	}
	worker := getConfigSearch(t, sam, "image:report-worker", "")
	if len(worker.Items) != 1 || worker.Items[0].Containers[0].BaseImage != "" {
		t.Errorf("report-worker = %+v", worker.Items)
	}
}

func TestConfigSearch_HiddenServicesNeverCounted(t *testing.T) {
	as, _, st := newConfigSearchServer(t)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateUser(t.Context(), "nobody", hash, false, false); err != nil {
		t.Fatal(err)
	}
	sam, omer := as("sam"), as("omer")

	samAll := getConfigSearch(t, sam, "execrole:ecs-task-execution", "")
	omerAll := getConfigSearch(t, omer, "execrole:ecs-task-execution", "")
	omerServices := getServicesList(t, omer, "")
	if len(omerAll.Items) == 0 || len(omerAll.Items) >= len(samAll.Items) {
		t.Fatalf("omer rows = %d, sam rows = %d", len(omerAll.Items), len(samAll.Items))
	}
	if omerAll.Total != omerServices.Total || omerAll.Searched != omerServices.Total {
		t.Errorf("omer total %d searched %d, services list total %d", omerAll.Total, omerAll.Searched, omerServices.Total)
	}
	for _, item := range omerAll.Items {
		if item.Tag.Name == "Prod" {
			t.Errorf("omer sees Prod service %s", item.Name)
		}
	}
	// a service omer cannot see is never found, even by a query only it matches
	if got := getConfigSearch(t, omer, "secret:JWT_SIGNING_KEY", ""); len(got.Items) != 0 || got.Total != 0 {
		t.Errorf("omer found a Prod service: %+v", configItemNames(got))
	}

	nobody := getConfigSearch(t, as("nobody"), "execrole:ecs-task-execution", "")
	if len(nobody.Items) != 0 || nobody.Total != 0 || nobody.Searched != 0 || nobody.Unindexed != 0 {
		t.Errorf("a user with no permissions got %+v", nobody)
	}
}

func TestConfigSearch_ShellFiltersNarrow(t *testing.T) {
	as, _, st := newConfigSearchServer(t)
	sam, omer := as("sam"), as("omer")
	ids := lookupTagIDs(t, st)
	q := "execrole:ecs-task-execution"
	for _, extra := range []string{"account=" + stagingAccountID, "region=eu-central-1", "account=" + homelabAccountID + "&region=us-east-1", ids.Prod, ids.Staging} {
		got := getConfigSearch(t, sam, q, extra)
		list := getServicesList(t, sam, "?"+extra)
		if got.Searched != list.Total || len(got.Items) != list.Total {
			t.Errorf("%s: searched %d rows %d, services list %d", extra, got.Searched, len(got.Items), list.Total)
		}
	}
	if full := getConfigSearch(t, sam, q, ""); full.Searched <= getConfigSearch(t, sam, q, "account="+stagingAccountID).Searched {
		t.Errorf("the account filter did not narrow: %d", full.Searched)
	}
	// a tag the user cannot see returns nothing rather than widening
	if got := getConfigSearch(t, omer, q, ids.Prod); len(got.Items) != 0 || got.Searched != 0 {
		t.Errorf("omer with tag Prod: %+v", got)
	}
}

func TestConfigSearch_ValuesAreNotSearchableOrReturned(t *testing.T) {
	as, _, _ := newConfigSearchServer(t)
	sam := as("sam")
	for _, q := range []string{plantedEnvValue, "env:" + plantedEnvValue, "prod/auth/database-url", "secretsmanager", "AbCdEf"} {
		if got := getConfigSearch(t, sam, q, ""); len(got.Items) != 0 {
			t.Errorf("%q found %v", q, configItemNames(got))
		}
	}
	resp := sam.get("/api/config-search?q=" + url.QueryEscape("env:PLANTED_NAME secret:DATABASE_URL"))
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "auth-service") || !strings.Contains(text, "PLANTED_NAME") {
		t.Fatalf("the planted service was not found: %s", text)
	}
	for _, leaked := range []string{plantedEnvValue, "prod/auth/database-url", "secretsmanager", "AbCdEf"} {
		if strings.Contains(text, leaked) {
			t.Errorf("response contains %q", leaked)
		}
	}
}

func TestConfigSearch_PreviousRevisionIsLabelled(t *testing.T) {
	as, _, _ := newConfigSearchServer(t)
	sam := as("sam")

	old := configItem(t, getConfigSearch(t, sam, "tag:2.2.0", ""), "notifications")
	if !old.Previous || old.MatchedRevision != old.Revision-1 || old.MatchedRevision != 9 || old.Revision != 10 {
		t.Errorf("old tag: previous %v matched %d revision %d", old.Previous, old.MatchedRevision, old.Revision)
	}
	current := configItem(t, getConfigSearch(t, sam, "tag:2.3.0", ""), "notifications")
	if current.Previous || current.MatchedRevision != current.Revision {
		t.Errorf("new tag: previous %v matched %d revision %d", current.Previous, current.MatchedRevision, current.Revision)
	}
}

func TestConfigSearch_Validation(t *testing.T) {
	as, _, _ := newConfigSearchServer(t)
	sam := as("sam")
	cases := []struct{ q, message string }{
		{strings.Repeat("a", 501), "Searches can be up to 500 characters."},
		{"aa bb cc dd ee ff gg hh ii jj kk", "Use at most 10 search terms."},
		{"a", "Search terms need at least 2 characters."},
		{"port:http", "port: needs a number from 1 to 65535."},
		{"env:", "Add a value after env:"},
		{`env:"PORT`, `Close the quote in env:"PORT.`},
	}
	for _, c := range cases {
		resp := sam.get("/api/config-search?q=" + url.QueryEscape(c.q))
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%q: status %d, want 422", c.q, resp.StatusCode)
			continue
		}
		if code, message := errorCode(t, resp); code != "invalid" || message != c.message {
			t.Errorf("%q: %s %q, want invalid %q", c.q, code, message, c.message)
		}
	}
	for _, q := range []string{"", "   "} {
		got := getConfigSearch(t, sam, q, "")
		if got.Items == nil || len(got.Items) != 0 || got.Total != 0 || got.Searched != 0 || got.PolledAt.IsZero() {
			t.Errorf("q=%q: %+v", q, got)
		}
	}
}

func TestConfigSearch_CapsAndReportsTotal(t *testing.T) {
	as, _, _ := newConfigSearchServer(t)
	sam := as("sam")
	old := configSearchCap
	configSearchCap = 3
	t.Cleanup(func() { configSearchCap = old })

	got := getConfigSearch(t, sam, "execrole:ecs-task-execution", "")
	if len(got.Items) != 3 || got.Total != 15 || !got.Truncated {
		t.Errorf("items %d total %d truncated %v, want 3, 15, true", len(got.Items), got.Total, got.Truncated)
	}
}

func TestConfigSearch_UnindexedCounted(t *testing.T) {
	as, _, st := newConfigSearchServer(t)
	sam := as("sam")
	if _, err := st.Pool.Exec(t.Context(), `delete from task_definitions where family = 'auth-service'`); err != nil {
		t.Fatal(err)
	}
	got := getConfigSearch(t, sam, "execrole:ecs-task-execution", "")
	if got.Unindexed != 1 || got.Searched != 15 || len(got.Items) != 14 {
		t.Errorf("unindexed %d searched %d items %d, want 1, 15, 14", got.Unindexed, got.Searched, len(got.Items))
	}
	for _, item := range got.Items {
		if item.Name == "auth-service" {
			t.Error("a service with no loaded definition appeared as a row")
		}
	}
}

func TestConfigSearch_RequiresSession(t *testing.T) {
	srv, _, _ := newTestServer(t)
	resp := newAPIClient(t, srv).get("/api/config-search?q=nginx")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", resp.StatusCode)
	}
}
