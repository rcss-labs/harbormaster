package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/cost"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func costFacts(t *testing.T, fx reliabilityFixture, vis store.Visibility) map[string]store.CostServiceFact {
	t.Helper()
	rows, err := fx.s.AnalyticsCostServices(context.Background(), store.TargetFilter{}, vis, readDay.AddDate(0, 0, -90))
	if err != nil {
		t.Fatal(err)
	}
	byService := map[string]store.CostServiceFact{}
	for _, row := range rows {
		byService[row.Key.Service] = row
	}
	return byService
}

func TestAnalyticsCostServices_UsageIsAbsoluteAcrossAResize(t *testing.T) {
	fx := newReliabilityFixture(t)
	// 20 days at 40% of 2048 units, then (the latest 10 days) 90% of 512: percentages are not comparable.
	for i := 0; i < 30; i++ {
		day := readDay.AddDate(0, 0, -i)
		if i < 10 {
			fx.utilDay(t, fx.payments, day, ptrF(10), ptrF(90), ptrF(10), ptrF(90), ptrI(512), ptrI(1024))
		} else {
			fx.utilDay(t, fx.payments, day, ptrF(10), ptrF(40), ptrF(10), ptrF(40), ptrI(2048), ptrI(4096))
		}
	}
	got := costFacts(t, fx, fx.adminVis)["payments"]
	if got.UsedCPUUnits == nil || !near(*got.UsedCPUUnits, 819.2) {
		t.Errorf("UsedCPUUnits = %v, want 819.2 (40%% of 2048, not 90%% of 512)", got.UsedCPUUnits)
	}
	if got.UsedMemMiB == nil || !near(*got.UsedMemMiB, 1638.4) {
		t.Errorf("UsedMemMiB = %v, want 1638.4", got.UsedMemMiB)
	}
	if got.HistoryDays != 30 || got.LatestDay == nil || !got.LatestDay.Equal(readDay) {
		t.Errorf("HistoryDays/LatestDay = %d/%v, want 30/%v", got.HistoryDays, got.LatestDay, readDay)
	}
}

func TestAnalyticsCostServices_HighestDailyP95IsKept(t *testing.T) {
	fx := newReliabilityFixture(t)
	for i := 0; i < 30; i++ {
		p95 := 10.0
		if i == 12 {
			p95 = 80
		}
		fx.utilDay(t, fx.payments, readDay.AddDate(0, 0, -i), ptrF(5), ptrF(p95), ptrF(5), ptrF(p95), ptrI(1024), ptrI(2048))
	}
	got := costFacts(t, fx, fx.adminVis)["payments"]
	if got.UsedCPUUnits == nil || !near(*got.UsedCPUUnits, 819.2) || got.UsedMemMiB == nil || !near(*got.UsedMemMiB, 1638.4) {
		t.Errorf("used = %v/%v, want 80%% of the reservation (819.2 units, 1638.4 MiB)", got.UsedCPUUnits, got.UsedMemMiB)
	}
}

func TestAnalyticsCostServices_IncompleteDaysDoNotCount(t *testing.T) {
	fx := newReliabilityFixture(t)
	fx.utilDay(t, fx.payments, readDay.AddDate(0, 0, -1), ptrF(5), ptrF(20), ptrF(5), ptrF(20), ptrI(1000), ptrI(1000))
	// Highest CPU, but no memory p95: the whole day is ignored.
	fx.utilDay(t, fx.payments, readDay, ptrF(5), ptrF(100), ptrF(5), nil, ptrI(1000), ptrI(1000))
	got := costFacts(t, fx, fx.adminVis)["payments"]
	if got.HistoryDays != 1 || got.UsedCPUUnits == nil || !near(*got.UsedCPUUnits, 200) {
		t.Errorf("history %d, cpu %v, want 1 day and 200 units", got.HistoryDays, got.UsedCPUUnits)
	}
	if got.LatestDay == nil || !got.LatestDay.Equal(readDay.AddDate(0, 0, -1)) {
		t.Errorf("LatestDay = %v, want the last complete day", got.LatestDay)
	}
}

func TestAnalyticsCostServices_NoRowsStillListsTheService(t *testing.T) {
	fx := newReliabilityFixture(t)
	got, ok := costFacts(t, fx, fx.adminVis)["ledger"]
	if !ok {
		t.Fatal("ledger missing, want it listed without utilisation")
	}
	if got.UsedCPUUnits != nil || got.UsedMemMiB != nil || got.HistoryDays != 0 || got.LatestDay != nil {
		t.Errorf("fact = %+v, want nil used values and no history", got)
	}
}

func TestAnalyticsCostServices_VisibilityExcludesOtherTags(t *testing.T) {
	fx := newReliabilityFixture(t)
	got := costFacts(t, fx, fx.prodOnlyVi)
	if len(got) != 2 || got["batch"].Key.Service != "" {
		t.Errorf("prod-only services = %v, want payments and ledger", got)
	}
	if f := got["payments"]; int64(f.TagID) != fx.prod || !f.TagInherited {
		t.Errorf("payments tag = %d inherited %v, want the inherited Prod tag", f.TagID, f.TagInherited)
	}
	if all := costFacts(t, fx, fx.adminVis); len(all) != 3 {
		t.Errorf("admin services = %d, want 3", len(all))
	}
}

func TestAnalyticsCostServices_ReadsArchitectureAndFlags(t *testing.T) {
	fx := newReliabilityFixture(t)
	ctx := context.Background()
	const tdARN = "arn:aws:ecs:eu-west-1:123456789012:task-definition/payments:7"
	raw, _ := json.Marshal(map[string]any{
		"RuntimePlatform":      map[string]string{"CpuArchitecture": "ARM64", "OperatingSystemFamily": "LINUX"},
		"ContainerDefinitions": []map[string]any{{"Name": "app", "Memory": 512}},
	})
	if err := fx.s.UpsertTaskDefinition(ctx, model.TaskDefinitionRef{
		AccountID: fx.payments.AccountID, Region: fx.payments.Region, ARN: tdARN, Family: "payments", Revision: 7,
		CPU: "1024", Memory: "2048", RequiresCompatibilities: []string{"FARGATE"}, Raw: raw,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.s.UpsertServices(ctx, []model.Service{
		{Key: fx.payments, Desired: 2, Running: 2, TaskDefinitionARN: tdARN, PolledAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	if err := fx.s.UpsertScaling(ctx, []model.Scaling{{Key: fx.payments, HasTarget: true, PolledAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	stoppedAt := time.Now().UTC().Add(-time.Hour)
	if _, err := fx.s.UpsertTasks(ctx, []model.Task{{
		Cluster: clusterKey("core"), ARN: readTaskARN("core", "oom"), ServiceName: "payments", LastStatus: "STOPPED",
		StoppedAt: &stoppedAt, StoppedReason: "OutOfMemoryError: container killed", PolledAt: stoppedAt, CreatedAt: stoppedAt.Add(-time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}

	facts := costFacts(t, fx, fx.adminVis)
	got := facts["payments"]
	if got.Architecture != "ARM64" || got.OSFamily != "LINUX" || !got.RequiresFargate || !got.AutoScaled || !got.RecentOOM {
		t.Errorf("payments = %+v, want ARM64 LINUX fargate autoscaled with a recent OOM", got)
	}
	if got.CPUUnits == nil || *got.CPUUnits != 1024 || got.MemoryMiB == nil || *got.MemoryMiB != 2048 {
		t.Errorf("size = %v/%v, want 1024/2048", got.CPUUnits, got.MemoryMiB)
	}
	if len(got.ContainerDefinitions) == 0 {
		t.Error("ContainerDefinitions empty, want the raw container definitions")
	}
	if l := facts["ledger"]; l.AutoScaled || l.RecentOOM || l.RequiresFargate || l.CPUUnits != nil {
		t.Errorf("ledger = %+v, want no flags and no size", l)
	}
}

func (fx reliabilityFixture) useTaskDefinition(t *testing.T, key model.ServiceKey, family string, td model.TaskDefinitionRef) {
	t.Helper()
	ctx := context.Background()
	td.AccountID, td.Region, td.Family, td.Revision = key.AccountID, key.Region, family, 1
	td.ARN = "arn:aws:ecs:" + key.Region + ":" + key.AccountID + ":task-definition/" + family + ":1"
	if err := fx.s.UpsertTaskDefinition(ctx, td); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.s.UpsertServices(ctx, []model.Service{
		{Key: key, Desired: 1, Running: 1, TaskDefinitionARN: td.ARN, PolledAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyticsCostServices_ContainerDefinitionsSurviveJSONEscapes(t *testing.T) {
	fx := newReliabilityFixture(t)
	raw, err := json.Marshal(map[string]any{"ContainerDefinitions": []map[string]any{{
		"Name": "app", "Cpu": 256, "Memory": 512,
		"Environment": []map[string]string{{"Name": "X", "Value": "quote \" newline \n backslash C:\\x"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	fx.useTaskDefinition(t, fx.payments, "payments", model.TaskDefinitionRef{CPU: "1024", Memory: "2048", RequiresCompatibilities: []string{"FARGATE"}, Raw: raw})

	got := costFacts(t, fx, fx.adminVis)["payments"]
	var parsed []struct {
		Environment []struct{ Value string }
	}
	if err := json.Unmarshal(got.ContainerDefinitions, &parsed); err != nil {
		t.Fatalf("ContainerDefinitions is not valid JSON: %v (%q)", err, got.ContainerDefinitions)
	}
	if v := parsed[0].Environment[0].Value; v != "quote \" newline \n backslash C:\\x" {
		t.Errorf("environment value = %q, want it unchanged", v)
	}
	floors, err := cost.ContainerFloors(got.ContainerDefinitions)
	if err != nil || floors.CPUUnits != 256 || floors.MemoryMiB != 512 {
		t.Errorf("floors = %+v (%v), want 256 units / 512 MiB", floors, err)
	}
}

func TestAnalyticsCostServices_MissingContainerDefinitionsAreNil(t *testing.T) {
	fx := newReliabilityFixture(t)
	fx.useTaskDefinition(t, fx.payments, "payments", model.TaskDefinitionRef{CPU: "1024", Memory: "2048", Raw: json.RawMessage(`{"ContainerDefinitions": null}`)})
	fx.useTaskDefinition(t, fx.ledger, "ledger", model.TaskDefinitionRef{CPU: "1024", Memory: "2048", Raw: json.RawMessage(`{}`)})
	facts := costFacts(t, fx, fx.adminVis)
	for _, name := range []string{"payments", "ledger", "batch"} { // batch has no task definition at all
		if facts[name].ContainerDefinitions != nil {
			t.Errorf("%s ContainerDefinitions = %q, want nil (floors unknown)", name, facts[name].ContainerDefinitions)
		}
	}
}

func TestAnalyticsCostServices_NonFargateAndNonNumericSizesAreNil(t *testing.T) {
	fx := newReliabilityFixture(t)
	fx.useTaskDefinition(t, fx.payments, "payments", model.TaskDefinitionRef{RequiresCompatibilities: []string{"EC2"}, Raw: json.RawMessage(`{}`)})
	fx.useTaskDefinition(t, fx.ledger, "ledger", model.TaskDefinitionRef{CPU: "1 vCPU", Memory: "2 GB", RequiresCompatibilities: []string{"FARGATE"}, Raw: json.RawMessage(`{}`)})
	facts := costFacts(t, fx, fx.adminVis)
	if p := facts["payments"]; p.CPUUnits != nil || p.MemoryMiB != nil || p.RequiresFargate {
		t.Errorf("EC2 task definition = %+v, want nil sizes and RequiresFargate false", p)
	}
	if l := facts["ledger"]; l.CPUUnits != nil || l.MemoryMiB != nil || !l.RequiresFargate {
		t.Errorf("non-numeric sizes = %+v, want nil sizes and RequiresFargate true", l)
	}
}

func TestAnalyticsCostServices_RecentOOMFromContainerExit137(t *testing.T) {
	fx := newReliabilityFixture(t)
	stoppedAt := time.Now().UTC().Add(-time.Hour)
	exit := 137
	if _, err := fx.s.UpsertTasks(context.Background(), []model.Task{{
		Cluster: clusterKey("core"), ARN: readTaskARN("core", "oom137"), ServiceName: "payments", LastStatus: "STOPPED",
		StopCode: "EssentialContainerExited", StoppedReason: "Essential container in task exited",
		Containers: []model.Container{{Name: "app", ExitCode: &exit}},
		StoppedAt:  &stoppedAt, PolledAt: stoppedAt, CreatedAt: stoppedAt.Add(-time.Hour),
	}}); err != nil {
		t.Fatal(err)
	}
	facts := costFacts(t, fx, fx.adminVis)
	if !facts["payments"].RecentOOM || facts["ledger"].RecentOOM {
		t.Errorf("RecentOOM payments/ledger = %v/%v, want true/false", facts["payments"].RecentOOM, facts["ledger"].RecentOOM)
	}
}
