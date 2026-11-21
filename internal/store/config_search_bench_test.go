package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// BenchmarkConfigSearchRows_2000 times the read-time projection against 2,000 services whose task definitions
// have 3 containers and 30 environment variables each. The plan's stop line is 300 ms per op. Skipped unless
// HM_BENCH=1:
//
//	HM_BENCH=1 go test ./internal/store -run '^$' -bench ConfigSearchRows_2000 -benchtime 5x
func BenchmarkConfigSearchRows_2000(b *testing.B) {
	if os.Getenv("HM_BENCH") != "1" {
		b.Skip("set HM_BENCH=1 to run")
	}
	s := storetest.Open(b)
	ctx := context.Background()
	statements := []string{
		`insert into services (account_id, region, cluster_name, service_name, desired, running, task_definition_arn, family, revision,
		     deployments, subnets, content_hash, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'c' || (1 + g % 50), 's' || g, 3, 3, 'td' || g, 'fam' || g, 1,
		     jsonb_build_array(jsonb_build_object('Status', 'PRIMARY', 'TaskDefinitionARN', 'td' || g, 'Revision', 1, 'Running', 3, 'Pending', 0)),
		     array[]::text[], 'h', '{}', now() from generate_series(1, 2000) g`,
		`insert into task_definitions (account_id, region, arn, family, revision, task_role_arn, execution_role_arn, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'td' || g, 'fam' || g, 1, 'arn:aws:iam::111111111111:role/task-' || g, 'arn:aws:iam::111111111111:role/exec',
		     jsonb_build_object('ContainerDefinitions', (
		       select jsonb_agg(jsonb_build_object(
		         'Name', 'c' || c, 'Image', '111111111111.dkr.ecr.eu-west-1.amazonaws.com/app' || g || ':1.' || c,
		         'PortMappings', jsonb_build_array(jsonb_build_object('ContainerPort', 8000 + c)),
		         'Environment', (select jsonb_agg(jsonb_build_object('Name', 'ENV_' || e, 'Value', repeat('v', 40))) from generate_series(1, 30) e),
		         'Secrets', jsonb_build_array(jsonb_build_object('Name', 'SECRET_' || c, 'ValueFrom', 'arn:aws:secretsmanager:eu-west-1:111111111111:secret:s' || c)),
		         'LogConfiguration', jsonb_build_object('LogDriver', 'awslogs', 'Options', jsonb_build_object('awslogs-group', '/ecs/app' || g))))
		       from generate_series(1, 3) c)),
		     now() from generate_series(1, 2000) g`,
		`analyze`,
	}
	for _, sql := range statements {
		if _, err := s.Pool.Exec(ctx, sql); err != nil {
			b.Fatalf("seed: %v\n%s", err, sql)
		}
	}
	admin := store.Visibility{Admin: true}
	b.ResetTimer()
	for range b.N {
		rows, err := s.ConfigSearchRows(ctx, store.TargetFilter{}, admin)
		if err != nil {
			b.Fatal(err)
		}
		if len(rows) != 2000 {
			b.Fatalf("rows = %d, want 2000", len(rows))
		}
	}
}
