package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

type selectorResp struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Value string `json:"value"`
	TagID int64  `json:"tag_id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type excludedResp struct {
	ServiceID   string `json:"service_id"`
	Service     string `json:"service"`
	Cluster     string `json:"cluster"`
	Region      string `json:"region"`
	AccountName string `json:"account_name"`
}

type selectorScheduleResp struct {
	scheduleResp
	Tags         []selectorResp `json:"tags"`
	Excluded     []excludedResp `json:"excluded"`
	ServiceCount int            `json:"service_count"`
}

type selectorPreviewResp struct {
	Services []serviceResp `json:"services"`
	Excluded []serviceResp `json:"excluded"`
	Overlaps []struct {
		ScheduleName string `json:"schedule_name"`
	} `json:"overlaps"`
}

func (e *offHoursEnv) tagNamed(t *testing.T, name string) store.Tag {
	t.Helper()
	tags, err := e.st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Name == name {
			return tag
		}
	}
	t.Fatalf("no Harbormaster tag %q", name)
	return store.Tag{}
}

func (e *offHoursEnv) preview(t *testing.T, c *apiClient, body map[string]any) selectorPreviewResp {
	t.Helper()
	resp := c.post("/api/offhours/preview", body)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("preview = %d %s %s", resp.StatusCode, code, msg)
	}
	var out selectorPreviewResp
	decode(t, resp, &out)
	return out
}

func TestOffHoursPreview_OrAcrossTagKindsListsEachServiceOnce(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	staging := env.tagNamed(t, "Staging")
	env.tagService(t, stagingWebKey, "team", "web")

	byHarbormaster := env.preview(t, c, scheduleBodyWithTags("x", hmTagBody(int64(staging.ID))))
	if len(byHarbormaster.Services) < 2 {
		t.Fatalf("Staging tag matched %d services, want at least 2", len(byHarbormaster.Services))
	}
	// staging-web/web matches by both tags and by the AWS tag alone: it must still appear once.
	both := env.preview(t, c, scheduleBodyWithTags("x", awsTagBody("team", "web"), hmTagBody(int64(staging.ID))))
	if len(both.Services) != len(byHarbormaster.Services) {
		t.Fatalf("services = %d, want %d (OR with deduplication)", len(both.Services), len(byHarbormaster.Services))
	}
	seen := map[string]bool{}
	for _, svc := range both.Services {
		if seen[svc.ServiceID] {
			t.Errorf("service %s listed twice", svc.ServiceID)
		}
		seen[svc.ServiceID] = true
	}
	prod := env.preview(t, c, scheduleBodyWithTags("x", awsTagBody("env", "prod"), hmTagBody(int64(staging.ID))))
	if want := len(byHarbormaster.Services) + 4; len(prod.Services) != want {
		t.Fatalf("services = %d, want %d: the 4 env=prod services plus the Staging ones", len(prod.Services), want)
	}
}

func TestOffHoursPreview_ExclusionMovesServiceToExcluded(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	staging := env.tagNamed(t, "Staging")
	all := env.preview(t, c, scheduleBodyWithTags("x", hmTagBody(int64(staging.ID))))

	body := scheduleBodyWithTags("x", hmTagBody(int64(staging.ID)))
	body["excluded_service_ids"] = []string{stagingWebKey.ID()}
	got := env.preview(t, c, body)
	if len(got.Services) != len(all.Services)-1 || len(got.Excluded) != 1 || got.Excluded[0].ServiceID != stagingWebKey.ID() {
		t.Fatalf("services = %d excluded = %+v, want %d services and staging-web/web excluded", len(got.Services), got.Excluded, len(all.Services)-1)
	}
	for _, svc := range got.Services {
		if svc.ServiceID == stagingWebKey.ID() {
			t.Errorf("excluded service %s is still in services", svc.ServiceID)
		}
	}
	if none := env.preview(t, c, scheduleBodyWithTags("x")); len(none.Services) != 0 || len(none.Excluded) != 0 {
		t.Errorf("preview without tags = %+v, want empty", none)
	}
}

func TestOffHoursSchedules_StoreTagsAndExclusionsAndHideExcludedFromDetail(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	staging := env.tagNamed(t, "Staging")
	all := env.preview(t, c, scheduleBodyWithTags("x", hmTagBody(int64(staging.ID))))

	body := scheduleBodyWithTags("staging nights", awsTagBody("env", "staging"), hmTagBody(int64(staging.ID)))
	body["excluded_service_ids"] = []string{stagingWebKey.ID(), stagingWebKey.ID()} // a repeated id is stored once
	resp := c.post("/api/offhours/schedules", body)
	if resp.StatusCode != http.StatusCreated {
		code, msg := errorCode(t, resp)
		t.Fatalf("create = %d %s %s", resp.StatusCode, code, msg)
	}
	var created selectorScheduleResp
	decode(t, resp, &created)
	wantTags := []selectorResp{
		{Kind: "aws", Key: "env", Value: "staging"},
		{Kind: "harbormaster", TagID: int64(staging.ID), Name: "Staging", Color: staging.Color},
	}
	if len(created.Tags) != 2 || created.Tags[0] != wantTags[0] || created.Tags[1] != wantTags[1] {
		t.Fatalf("tags = %+v, want %+v", created.Tags, wantTags)
	}
	if len(created.Excluded) != 1 || created.Excluded[0].ServiceID != stagingWebKey.ID() || created.Excluded[0].Service != "web" ||
		created.Excluded[0].Cluster != "staging-web" || created.Excluded[0].Region != "us-east-1" || created.Excluded[0].AccountName == "" {
		t.Fatalf("excluded = %+v", created.Excluded)
	}
	if created.ServiceCount != len(all.Services)-1 {
		t.Errorf("service_count = %d, want %d (the excluded service does not count)", created.ServiceCount, len(all.Services)-1)
	}
	for _, svc := range created.Services {
		if svc.ServiceID == stagingWebKey.ID() {
			t.Errorf("excluded service %s is listed in the detail rows", svc.ServiceID)
		}
	}

	// The list shows the same tags and exclusions.
	var list struct {
		Items []selectorScheduleResp `json:"items"`
	}
	decode(t, c.get("/api/offhours/schedules"), &list)
	if len(list.Items) != 1 || len(list.Items[0].Tags) != 2 || len(list.Items[0].Excluded) != 1 {
		t.Fatalf("list = %+v", list.Items)
	}

	// An update replaces both lists.
	update := scheduleBodyWithTags("staging nights", awsTagBody("env", "staging"))
	update["version"] = created.Version
	resp = c.do(http.MethodPut, "/api/offhours/schedules/"+itoa(created.ID), update, nil)
	if resp.StatusCode != http.StatusOK {
		code, msg := errorCode(t, resp)
		t.Fatalf("update = %d %s %s", resp.StatusCode, code, msg)
	}
	var updated selectorScheduleResp
	decode(t, resp, &updated)
	if len(updated.Tags) != 1 || len(updated.Excluded) != 0 {
		t.Fatalf("updated = tags %+v excluded %+v, want one tag and no exclusions", updated.Tags, updated.Excluded)
	}
}

func TestOffHoursSchedules_OverlapIgnoresExcludedServices(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	staging := env.tagNamed(t, "Staging")
	env.tagService(t, stagingWebKey, "team", "web")

	first := scheduleBodyWithTags("staging nights", hmTagBody(int64(staging.ID)))
	first["excluded_service_ids"] = []string{stagingWebKey.ID()}
	env.createSchedule(t, c, first)

	// staging-web/web is only in the second schedule because the first one excludes it.
	env.createSchedule(t, c, scheduleBody("web team", "team", "web"))

	// Taking the exclusion away from the first schedule now collides.
	var all struct {
		Items []scheduleResp `json:"items"`
	}
	decode(t, c.get("/api/offhours/schedules"), &all)
	var firstSaved scheduleResp
	for _, item := range all.Items {
		if item.Name == "staging nights" {
			firstSaved = item
		}
	}
	unexcluded := scheduleBodyWithTags("staging nights", hmTagBody(int64(staging.ID)))
	unexcluded["version"] = firstSaved.Version
	resp := c.do(http.MethodPut, "/api/offhours/schedules/"+itoa(firstSaved.ID), unexcluded, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("update without the exclusion = %d, want 409", resp.StatusCode)
	}
	if _, msg := errorCode(t, resp); !strings.Contains(msg, `Schedule "web team" already matches 1 of these services: staging-web/web`) {
		t.Errorf("message = %q", msg)
	}
}

func TestOffHoursSchedules_RejectsBadTagsAndExclusions(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	staging := env.tagNamed(t, "Staging")

	fiftyOne := []map[string]any{}
	for i := 0; i < 51; i++ {
		fiftyOne = append(fiftyOne, awsTagBody("k"+itoa(int64(i)), "v"))
	}
	thousandAndOne := make([]string, 1001)
	for i := range thousandAndOne {
		thousandAndOne[i] = model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: "1", Region: "r", Cluster: "c"}, Service: "s" + itoa(int64(i))}.ID()
	}
	withExcluded := func(ids ...string) map[string]any {
		body := scheduleBody("x", "env", "staging")
		body["excluded_service_ids"] = ids
		return body
	}
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"no tags", scheduleBodyWithTags("x"), "Pick at least one tag."},
		{"too many tags", scheduleBodyWithTags("x", fiftyOne...), "50"},
		{"duplicate aws tag", scheduleBodyWithTags("x", awsTagBody("env", "a"), awsTagBody("env", "a")), "twice"},
		{"duplicate harbormaster tag", scheduleBodyWithTags("x", hmTagBody(int64(staging.ID)), hmTagBody(int64(staging.ID))), "twice"},
		{"aws prefix", scheduleBodyWithTags("x", awsTagBody("aws:foo", "a")), "aws:"},
		{"empty key", scheduleBodyWithTags("x", awsTagBody("", "a")), "tag key"},
		{"unknown harbormaster tag", scheduleBodyWithTags("x", hmTagBody(999999)), "no longer exists"},
		{"unknown kind", scheduleBodyWithTags("x", map[string]any{"kind": "other"}), "kind"},
		{"bad excluded id", withExcluded("not-an-id"), "not a valid service"},
		{"too many excluded", withExcluded(thousandAndOne...), "1000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"/api/offhours/schedules", "/api/offhours/preview"} {
				if tc.name == "no tags" && strings.HasSuffix(path, "preview") {
					continue // an empty preview is not an error
				}
				resp := c.post(path, tc.body)
				if resp.StatusCode != http.StatusUnprocessableEntity {
					t.Fatalf("POST %s = %d, want 422", path, resp.StatusCode)
				}
				if code, msg := errorCode(t, resp); code != "invalid" || !strings.Contains(msg, tc.want) {
					t.Errorf("POST %s = %s %q, want invalid containing %q", path, code, msg, tc.want)
				}
			}
		})
	}
	// The old single-tag body is no longer accepted.
	old := map[string]any{"name": "x", "enabled": true, "tag_key": "env", "tag_value": "staging",
		"timezone": "Europe/Berlin", "awake": weekdayGrid()}
	if resp := c.post("/api/offhours/schedules", old); resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("legacy body = %d, want a rejection", resp.StatusCode)
	}
}

func TestOffHoursTags_ListsHarbormasterTagCounts(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	staging := env.tagNamed(t, "Staging")
	var out struct {
		Harbormaster []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			Color    string `json:"color"`
			Services int    `json:"services"`
		} `json:"harbormaster"`
	}
	decode(t, env.admin(t).get("/api/offhours/tags"), &out)
	counts, err := env.st.OffHoursHarbormasterTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Harbormaster) != len(counts) || len(counts) == 0 {
		t.Fatalf("harbormaster = %+v, want %d tags", out.Harbormaster, len(counts))
	}
	var found bool
	for _, tag := range out.Harbormaster {
		if authz.TagID(tag.ID) == staging.ID {
			found = tag.Name == "Staging" && tag.Color == staging.Color && tag.Services >= 2
		}
	}
	if !found {
		t.Errorf("harbormaster = %+v, want Staging with its colour and at least 2 services", out.Harbormaster)
	}
}

func TestServiceOffHours_ExcludedServiceHasNoSchedule(t *testing.T) {
	env := newOffHoursEnv(t, saturdayNight(t))
	c := env.admin(t)
	body := scheduleBody("staging nights", "env", "staging")
	body["excluded_service_ids"] = []string{stagingWebKey.ID()}
	env.createSchedule(t, c, body)

	var out struct {
		ScheduleName string `json:"schedule_name"`
	}
	decode(t, c.get(serviceOffHoursPath(stagingWebKey, "")), &out)
	if out.ScheduleName != "" {
		t.Errorf("schedule_name = %q, want none for an excluded service", out.ScheduleName)
	}
}

func TestScheduleAuditView_NamesTagsAndExclusions(t *testing.T) {
	sc := store.OffHoursSchedule{Name: "n", Enabled: true, Timezone: "UTC", Awake: "1",
		Selectors:  []store.OffHoursSelector{{Kind: "aws", Key: "env", Value: "staging"}, {Kind: "harbormaster", TagID: 4}},
		Exclusions: []model.ServiceKey{stagingWebKey}}
	view := scheduleAuditView(sc, map[authz.TagID]string{4: "QA"})
	tags, _ := view["tags"].([]string)
	excluded, _ := view["excluded"].([]string)
	if len(tags) != 2 || tags[0] != "env=staging" || tags[1] != "Harbormaster tag QA" || len(excluded) != 1 || excluded[0] != "staging-web/web" {
		t.Fatalf("view = %+v", view)
	}
}
