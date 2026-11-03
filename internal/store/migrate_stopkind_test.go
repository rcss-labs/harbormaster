package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// The 0100 migration backfills stop_kind, stop_cause and the spot stop-reason group for rows written before
// the columns existed. The file is idempotent, so the test replays it over legacy-shaped rows.
func TestMigrate_StopKindBackfill(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	body, err := os.ReadFile("migrations/0100_stop_kind.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	type legacy struct {
		arn, status, stopCode, reason, group string
		exit                                 int
		containers                           string
		wantKind, wantCause, wantGroup       string
	}
	const dep = "Replaced by a deployment"
	rows := []legacy{
		{arn: "deploywording", status: "STOPPED", stopCode: "ServiceSchedulerInitiated", reason: "Task stopped due to deployment", group: dep, exit: 1, containers: `[{"ExitCode":1}]`, wantKind: "expected", wantCause: "deploy", wantGroup: dep},
		{arn: "spot1", status: "STOPPED", stopCode: "SpotInterruption", reason: "Your Spot Task was interrupted.", group: dep, exit: 1, containers: `[{"ExitCode":1}]`, wantKind: "expected", wantCause: "spot", wantGroup: "Spot interruption"},
		{arn: "spot0", status: "STOPPED", stopCode: "SpotInterruption", group: dep, exit: 0, containers: `[{"ExitCode":0}]`, wantCause: "spot", wantGroup: "Spot interruption"},
		{arn: "maint", status: "STOPPED", stopCode: "TerminationNotice", group: dep, exit: 1, containers: `[{"ExitCode":1}]`, wantKind: "expected", wantCause: "maintenance", wantGroup: "Spot interruption"},
		{arn: "deploy", status: "STOPPED", stopCode: "ServiceSchedulerInitiated", reason: "Scaling activity initiated by (deployment ecs-svc/1)", group: dep, exit: 1, containers: `[{"ExitCode":1}]`, wantKind: "expected", wantCause: "scaling", wantGroup: dep},
		{arn: "scalein", status: "STOPPED", stopCode: "ServiceSchedulerInitiated", reason: "Scaling activity initiated by (desired count change)", group: "Scaled in", exit: 143, containers: `[{"ExitCode":143}]`, wantKind: "expected", wantCause: "scale-in", wantGroup: "Scaled in"},
		{arn: "policy", status: "STOPPED", stopCode: "ServiceSchedulerInitiated", reason: "Scaling activity initiated by (policy cpu-target-tracking)", group: dep, exit: 1, containers: `[{"ExitCode":1}]`, wantKind: "error", wantGroup: dep},
		{arn: "realdeploy", status: "STOPPED", stopCode: "ServiceSchedulerInitiated", reason: "Scaling activity initiated by (deployment ecs-svc/0911899770729461037)", group: dep, exit: 1, containers: `[{"ExitCode":1}]`, wantKind: "expected", wantCause: "scaling", wantGroup: dep},
		{arn: "realdeploy2", status: "STOPPED", stopCode: "ServiceSchedulerInitiated", reason: "Scaling activity initiated by (deployment ecs-svc/4412300098817265531)", group: dep, exit: 1, containers: `[{"ExitCode":1}]`, wantKind: "expected", wantCause: "scaling", wantGroup: dep},
		{arn: "user", status: "STOPPED", stopCode: "UserInitiated", reason: "Task stopped by pavel via Harbormaster", group: "Stopped by a user", exit: 143, containers: `[{"ExitCode":143}]`, wantKind: "expected", wantCause: "user", wantGroup: "Stopped by a user"},
		{arn: "oomspot", status: "STOPPED", stopCode: "SpotInterruption", group: "Out of memory (exit 137)", exit: 137, containers: `[{"ExitCode":0},{"ExitCode":137}]`, wantKind: "error", wantGroup: "Out of memory (exit 137)"},
		{arn: "health", status: "STOPPED", stopCode: "ServiceSchedulerInitiated", reason: "Task failed ELB health checks in (target-group tg)", group: "Failed load balancer health checks", exit: 0, containers: `[{"ExitCode":0}]`, wantKind: "error", wantGroup: "Failed load balancer health checks"},
		{arn: "healthdeploy", status: "STOPPED", stopCode: "ServiceSchedulerInitiated", reason: "deployment: Task failed ELB health checks", group: "Failed load balancer health checks", exit: 143, containers: `[{"ExitCode":143}]`, wantKind: "error", wantGroup: "Failed load balancer health checks"},
		{arn: "notstart", status: "STOPPED", stopCode: "TaskFailedToStart", group: "Failed to start", containers: `[]`, wantKind: "error", wantGroup: "Failed to start"},
		{arn: "crash", status: "STOPPED", stopCode: "EssentialContainerExited", group: "Container exited with an error", exit: 1, containers: `[{"ExitCode":1}]`, wantKind: "error", wantGroup: "Container exited with an error"},
		{arn: "plain1", status: "STOPPED", group: dep, exit: 2, containers: `[{"ExitCode":2}]`, wantKind: "error", wantGroup: dep},
		{arn: "clean", status: "STOPPED", group: dep, exit: 0, containers: `[{"ExitCode":0}]`, wantGroup: dep},
		{arn: "running", status: "RUNNING", stopCode: "SpotInterruption", exit: 1, containers: `[{"ExitCode":1}]`},
	}
	for _, r := range rows {
		_, err := s.Pool.Exec(ctx, `insert into tasks (account_id, region, task_arn, cluster_name, last_status, stop_code, stopped_reason,
		    stop_reason_group, exit_code, containers, content_hash, raw, polled_at)
		  values ('1', 'r', $1, 'c', $2, nullif($3, ''), nullif($4, ''), nullif($5, ''), $6, $7::jsonb, 'h', '{}', now())`,
			r.arn, r.status, r.stopCode, r.reason, r.group, r.exit, r.containers)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("replay migration: %v", err)
	}
	for _, r := range rows {
		var kind, cause, group string
		if err := s.Pool.QueryRow(ctx, `select coalesce(stop_kind, ''), coalesce(stop_cause, ''), coalesce(stop_reason_group, '') from tasks where task_arn = $1`, r.arn).Scan(&kind, &cause, &group); err != nil {
			t.Fatal(err)
		}
		if kind != r.wantKind || cause != r.wantCause || group != r.wantGroup {
			t.Errorf("%s: kind/cause/group = %q/%q/%q, want %q/%q/%q", r.arn, kind, cause, group, r.wantKind, r.wantCause, r.wantGroup)
		}
	}
}
