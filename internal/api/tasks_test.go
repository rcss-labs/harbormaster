package api

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
)

func getTasks(t *testing.T, c *apiClient, path string) TasksListDTO {
	t.Helper()
	resp := c.get(path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, resp.StatusCode)
	}
	var out TasksListDTO
	decode(t, resp, &out)
	return out
}

func tasksByCluster(items []TaskDTO, cluster string) []TaskDTO {
	var out []TaskDTO
	for _, task := range items {
		if task.Cluster.Name == cluster {
			out = append(out, task)
		}
	}
	return out
}

func TestTasks_StandaloneVisibleByClusterTag(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "oncall")
	pollAll(t, st, fk)

	// db-migrate stopped 2 h ago; widen the window to a day so the test is about visibility only.
	if resp := putWindow(login(t, srv, "pavel", testPassword), map[string]any{"stopped_tasks_window_minutes": 1440}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT settings status %d", resp.StatusCode)
	}
	omer := getTasks(t, login(t, srv, "omer", testPassword), "/api/tasks?filter=all&limit=500")
	families := map[string]TaskDTO{}
	for _, task := range omer.Items {
		if task.Service == nil {
			families[task.Family] = task
		}
	}
	for _, family := range []string{"release-build", "db-migrate"} {
		task, ok := families[family]
		if !ok {
			t.Errorf("omer does not see standalone %s", family)
			continue
		}
		if task.Service != nil || task.Cluster.Name == "" {
			t.Errorf("standalone task = %+v", task)
		}
	}

	oncall := getTasks(t, login(t, srv, "oncall", testPassword), "/api/tasks?filter=all&limit=500")
	for _, task := range oncall.Items {
		if task.Service == nil {
			t.Errorf("oncall sees standalone task %s (%s)", task.ID, task.Family)
		}
	}
}

func TestTasks_ProblemsFilterAndCounts(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	c := login(t, srv, "pavel", testPassword)
	got := getTasks(t, c, "/api/tasks?limit=500") // default filter is problems

	var stopped137, unhealthyCatalog bool
	for _, task := range got.Items {
		if task.Service != nil && task.Service.Name == "image-resizer" && task.Status == "STOPPED" && task.ExitCode != nil && *task.ExitCode == 137 {
			stopped137 = true
		}
		if task.Service != nil && task.Service.Name == "catalog-api" && task.Health == "UNHEALTHY" {
			unhealthyCatalog = true
		}
		if task.Status == "STOPPED" && (task.ExitCode == nil || *task.ExitCode == 0) && task.StopCode != "TaskFailedToStart" {
			t.Errorf("clean stopped task in problems: %+v", task)
		}
	}
	if !stopped137 || !unhealthyCatalog {
		t.Errorf("problems missing image-resizer 137 (%v) or catalog-api unhealthy (%v)", stopped137, unhealthyCatalog)
	}
	if want := estateStoppedWithin(fk, 10*time.Minute); got.Counts.Stopped != want || want == 0 {
		t.Errorf("counts.stopped = %d, want %d (stops inside the 10 minute window)", got.Counts.Stopped, want)
	}
	if got.Counts.Running != 22 { // 21 service tasks + the standalone release-build
		t.Errorf("counts.running = %d, want 22", got.Counts.Running)
	}
	all := getTasks(t, c, "/api/tasks?filter=all&limit=500")
	if all.Counts.All != len(all.Items) || all.Counts.All != all.Counts.Running+all.Counts.Stopped {
		t.Errorf("counts = %+v items %d", all.Counts, len(all.Items))
	}
	if got.Counts != all.Counts {
		t.Errorf("counts differ by filter: %+v vs %+v", got.Counts, all.Counts)
	}
	if got.Counts.Problems != len(got.Items) {
		t.Errorf("counts.problems = %d, items %d", got.Counts.Problems, len(got.Items))
	}
	for _, task := range all.Items {
		if task.Status == "STOPPED" && (task.StoppedAt == nil || time.Since(*task.StoppedAt) > 10*time.Minute+time.Minute) {
			t.Errorf("stopped task outside the window listed: %s stopped %v", task.ShortID, task.StoppedAt)
		}
	}
	if got.Summary.ByStatus.StoppedError <= got.Counts.Stopped {
		t.Errorf("summary.by_status.stopped_error = %d must still cover all 72 h stops (> %d)", got.Summary.ByStatus.StoppedError, got.Counts.Stopped)
	}
	if got.Summary.ByStatus.StoppedError == 0 || got.Summary.ByStatus.RunningUnhealthy != 1 {
		t.Errorf("summary.by_status = %+v", got.Summary.ByStatus)
	}
	if len(got.Summary.ByAZ) == 0 || len(got.Summary.ByCluster) != 6 || len(got.Summary.StopReasons) == 0 {
		t.Errorf("summary = %+v", got.Summary)
	}
	for i := 1; i < len(got.Summary.ByAZ); i++ {
		if got.Summary.ByAZ[i-1].AZ > got.Summary.ByAZ[i].AZ {
			t.Errorf("by_az not sorted: %+v", got.Summary.ByAZ)
		}
	}
	if got.PolledAt.IsZero() {
		t.Error("polled_at missing")
	}
}

func TestTasks_InvalidFilterIs422(t *testing.T) {
	srv, fk, st := newTestServer(t)
	pollAll(t, st, fk)
	resp := login(t, srv, "pavel", testPassword).get("/api/tasks?filter=bogus")
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "invalid" {
		t.Errorf("code = %q", code)
	}
}

func TestTasks_PaginationCursor(t *testing.T) {
	srv, fk, st := newTestServer(t)
	pollAll(t, st, fk)
	c := login(t, srv, "pavel", testPassword)
	seen := map[string]bool{}
	path := "/api/tasks?filter=all&limit=5"
	pages := 0
	var last TasksListDTO
	for path != "" {
		last = getTasks(t, c, path)
		pages++
		if pages > 30 {
			t.Fatal("pagination does not terminate")
		}
		for _, task := range last.Items {
			if seen[task.ARN] {
				t.Errorf("task %s repeated across pages", task.ARN)
			}
			seen[task.ARN] = true
		}
		if pages == 1 && last.Next == nil {
			t.Fatal("first page next = null, want a cursor")
		}
		path = ""
		if last.Next != nil {
			path = "/api/tasks?filter=all&limit=5&cursor=" + url.QueryEscape(*last.Next)
		}
	}
	if pages < 2 || len(seen) != last.Counts.All {
		t.Errorf("pages %d, seen %d, counts.all %d", pages, len(seen), last.Counts.All)
	}
}

func TestServiceTasks_ScopedAndInvisible(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	imageResizer := serviceID(homelabAccountID, "us-east-1", "background-jobs", "image-resizer")

	pavel := login(t, srv, "pavel", testPassword)
	// The six OOM stops are 4-64 min old; widen the window so all of them are listed.
	if resp := putWindow(pavel, map[string]any{"stopped_tasks_window_minutes": 90}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT settings status %d", resp.StatusCode)
	}
	got := getTasks(t, pavel, "/api/services/"+imageResizer+"/tasks?filter=all&limit=100")
	if len(got.Items) != 6 {
		t.Fatalf("image-resizer tasks = %d, want 6", len(got.Items))
	}
	for _, task := range got.Items {
		if task.Service == nil || task.Service.Name != "image-resizer" || task.Service.Status != "failing" {
			t.Errorf("task = %+v", task)
		}
	}
	if got.Counts.All != 6 || got.Summary.ByCluster == nil {
		t.Errorf("counts %+v by_cluster %v", got.Counts, got.Summary.ByCluster)
	}

	resp := login(t, srv, "omer", testPassword).get("/api/services/" + paymentsID() + "/tasks")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("omer payments tasks status %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestTasks_CanAndExecFlags(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	setPassword(t, st, "oncall")
	pollAll(t, st, fk)
	got := getTasks(t, login(t, srv, "oncall", testPassword),
		"/api/services/"+serviceID(stagingAccountID, "us-east-1", "staging-web", "web")+"/tasks?filter=running")
	if len(got.Items) != 1 {
		t.Fatalf("web running tasks = %d, want 1", len(got.Items))
	}
	task := got.Items[0]
	if !task.Can.Operate || !task.Can.Terminal || !task.ExecEnabled || task.Status != "RUNNING" || task.Capacity == "" {
		t.Errorf("task = %+v", task)
	}
}

func TestTasks_CarryAccountNameAndRegion(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	got := getTasks(t, login(t, srv, "pavel", testPassword), "/api/tasks?filter=running&limit=500")
	if len(got.Items) == 0 {
		t.Fatal("no running tasks")
	}
	for _, task := range got.Items {
		key, err := model.ParseClusterID(task.Cluster.ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Region != key.Region || task.AccountName == "" {
			t.Errorf("task %s: account_name %q region %q, want a name and %q", task.ShortID, task.AccountName, task.Region, key.Region)
		}
	}
	if core := tasksByCluster(got.Items, "core-services"); len(core) == 0 || core[0].AccountName != "homelab-admin" || core[0].Region != "us-east-1" {
		t.Errorf("core-services tasks = %+v, want account homelab-admin in us-east-1", core)
	}
}

func TestTasks_StoppedErrorCountFollowsWindowAndScope(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	c := login(t, srv, "pavel", testPassword)
	resizer := serviceID(homelabAccountID, "us-east-1", "background-jobs", "image-resizer")

	// errorStops counts the listed stopped tasks that failed, the definition the count must match.
	errorStops := func(items []TaskDTO) int {
		n := 0
		for _, task := range items {
			if task.Status == "STOPPED" && ((task.ExitCode != nil && *task.ExitCode != 0) || task.StopCode == "TaskFailedToStart") {
				n++
			}
		}
		return n
	}

	counts := map[int]int{}
	for _, minutes := range []int{10, 90} {
		if resp := putWindow(c, map[string]any{"stopped_tasks_window_minutes": minutes}); resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT status %d", resp.StatusCode)
		}
		got := getTasks(t, c, "/api/tasks?filter=running&limit=500")
		stopped := getTasks(t, c, "/api/tasks?filter=stopped&limit=500")
		if want := errorStops(stopped.Items); got.Counts.StoppedError != want {
			t.Errorf("window %d min: stopped_error = %d, want %d", minutes, got.Counts.StoppedError, want)
		}
		counts[minutes] = got.Counts.StoppedError

		scoped := getTasks(t, c, "/api/services/"+resizer+"/tasks?filter=running&limit=500")
		if minutes == 90 && scoped.Counts.StoppedError != 6 {
			t.Errorf("image-resizer stopped_error = %d, want its 6 OOM stops", scoped.Counts.StoppedError)
		}
		if scoped.Counts.StoppedError > got.Counts.StoppedError {
			t.Errorf("scoped count %d exceeds the estate count %d", scoped.Counts.StoppedError, got.Counts.StoppedError)
		}
	}
	if counts[10] < 1 || counts[90] <= counts[10] {
		t.Errorf("stopped_error by window = %v, want at least one inside 10 min and more inside 90", counts)
	}
}

func TestServiceTasks_SummaryValues(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	c := login(t, srv, "pavel", testPassword)

	resizer := getTasks(t, c, "/api/services/"+serviceID(homelabAccountID, "us-east-1", "background-jobs", "image-resizer")+"/tasks?filter=all")
	by := resizer.Summary.ByStatus
	if by.StoppedError != 6 || by.RunningHealthy != 0 || by.RunningUnhealthy != 0 || by.StoppedNormal != 0 {
		t.Errorf("image-resizer by_status = %+v", by)
	}
	if len(resizer.Summary.StopReasons) != 1 || resizer.Summary.StopReasons[0] != (StopReasonDTO{Reason: "Out of memory (exit 137)", Count: 6}) {
		t.Errorf("image-resizer stop_reasons = %+v", resizer.Summary.StopReasons)
	}
	if len(resizer.Summary.ByAZ) != 0 || len(resizer.Summary.ByCluster) != 0 {
		t.Errorf("image-resizer runs nothing: by_az %+v by_cluster %+v", resizer.Summary.ByAZ, resizer.Summary.ByCluster)
	}

	catalog := getTasks(t, c, "/api/services/"+serviceID(homelabAccountID, "eu-west-1", "eu-apps", "catalog-api")+"/tasks?filter=all")
	if catalog.Summary.ByStatus.RunningUnhealthy != 1 || catalog.Summary.ByStatus.RunningHealthy != 1 {
		t.Errorf("catalog-api by_status = %+v", catalog.Summary.ByStatus)
	}
	if len(catalog.Summary.ByCluster) != 1 || catalog.Summary.ByCluster[0].Name != "eu-apps" {
		t.Errorf("catalog-api by_cluster = %+v", catalog.Summary.ByCluster)
	}
}

// estateStoppedWithin counts the mockup estate's STOPPED tasks stopped no more than window ago.
func estateStoppedWithin(fk *fake.Fake, window time.Duration) int {
	fk.Estate.Mu.Lock()
	defer fk.Estate.Mu.Unlock()
	n := 0
	for _, task := range fk.Estate.Tasks {
		if aws.ToString(task.Task.LastStatus) == "STOPPED" && task.Task.StoppedAt != nil && time.Since(*task.Task.StoppedAt) <= window {
			n++
		}
	}
	return n
}

func TestTasks_WindowFollowsSetting(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	c := login(t, srv, "pavel", testPassword)
	if resp := putWindow(c, map[string]any{"stopped_tasks_window_minutes": 90}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status %d", resp.StatusCode)
	}

	want := estateStoppedWithin(fk, 90*time.Minute)
	if want < 7 {
		t.Fatalf("estate has %d stops in 90 min, expected at least the 6 image-resizer ones plus catalog-api", want)
	}
	stopped := getTasks(t, c, "/api/tasks?filter=stopped&limit=500")
	if len(stopped.Items) != want || stopped.Counts.Stopped != want {
		t.Errorf("stopped items/count = %d/%d, want %d", len(stopped.Items), stopped.Counts.Stopped, want)
	}

	resizer := serviceID(homelabAccountID, "us-east-1", "background-jobs", "image-resizer")
	svc := getTasks(t, c, "/api/services/"+resizer+"/tasks?filter=all&limit=500")
	var wantSvc int
	for _, task := range stopped.Items {
		if task.Service != nil && task.Service.Name == "image-resizer" {
			wantSvc++
		}
	}
	var gotSvc int
	var oldest TaskDTO
	for _, task := range svc.Items {
		if task.Status == "STOPPED" {
			gotSvc++
			if oldest.StoppedAt == nil || task.StoppedAt.Before(*oldest.StoppedAt) {
				oldest = task
			}
		}
	}
	if gotSvc != wantSvc || gotSvc != 6 {
		t.Fatalf("service tasks stopped = %d, want %d (6 image-resizer OOM stops)", gotSvc, wantSvc)
	}

	// A task stopped 64 min ago is hidden from the 10 minute list but its by-id reads still work.
	if resp := putWindow(c, map[string]any{"stopped_tasks_window_minutes": 10}); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT status %d", resp.StatusCode)
	}
	hidden := getTasks(t, c, "/api/services/"+resizer+"/tasks?filter=all&limit=500")
	for _, task := range hidden.Items {
		if task.ID == oldest.ID {
			t.Fatal("64 minute old stop still listed under the 10 minute window")
		}
	}
	// No HTTP route reads a stopped task by id (exec-targets 404s for any non-RUNNING task), so
	// check the store read behind the by-id endpoints.
	account, region, arn, err := model.ParseTaskID(oldest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetTask(t.Context(), account, region, arn); err != nil {
		t.Errorf("by-id read of a task outside the window failed: %v", err)
	}
}
