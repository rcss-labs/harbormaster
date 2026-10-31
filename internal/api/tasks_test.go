package api

import (
	"net/http"
	"net/url"
	"testing"
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
	if got.Counts.Stopped < 10 {
		t.Errorf("counts.stopped = %d, want >= 10", got.Counts.Stopped)
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

	got := getTasks(t, login(t, srv, "pavel", testPassword), "/api/services/"+imageResizer+"/tasks?filter=all&limit=100")
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
