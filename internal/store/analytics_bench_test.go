package store_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// seedAnalyticsScale fills a schema to the plan's target size: 100 clusters, 600 services, 1.2M counts-history
// rows (7 days at 5 minutes), 1,000 running and 20,000 stopped tasks, 150,000 service events.
func seedAnalyticsScale(b *testing.B, s *store.Store) (prod int64) {
	b.Helper()
	ctx := context.Background()
	tag, err := s.CreateTag(ctx, "Prod", "red")
	if err != nil {
		b.Fatal(err)
	}
	statements := []string{
		`insert into clusters (account_id, region, cluster_name, content_hash, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'c' || g, 'h', '{}', now() from generate_series(1, 100) g`,
		`insert into cluster_tags (account_id, region, cluster_name, tag_id)
		   select '111111111111', 'eu-west-1', 'c' || g, ` + strconv.FormatInt(int64(tag.ID), 10) + ` from generate_series(1, 100, 2) g`,
		`insert into services (account_id, region, cluster_name, service_name, desired, running, task_definition_arn, subnets, content_hash, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'c' || (1 + g % 100), 's' || g, 3, 3, 'td' || g, array['subnet-' || (g % 50)], 'h', '{}', now() from generate_series(1, 600) g`,
		`insert into task_definitions (account_id, region, arn, cpu, memory, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'td' || g, '512', '1024', '{}', now() from generate_series(1, 600) g`,
		`insert into scaling (account_id, region, cluster_name, service_name, has_target, min_capacity, max_capacity, policies, scheduled_actions, activities, polled_at)
		   select '111111111111', 'eu-west-1', 'c' || (1 + g % 100), 's' || g, true, 1, case when g <= 20 then 3 else 10 end, '[]', '[]', '[]', now() from generate_series(1, 600) g`,
		`insert into subnets (account_id, region, subnet_id, vpc_id, name, cidr, az, available_ips, public, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'subnet-' || g, 'vpc-1', 'sn' || g, '10.0.' || g || '.0/24', 'eu-west-1a', g * 5, false, '{}', now() from generate_series(0, 49) g`,
		`insert into service_counts_history (account_id, region, cluster_name, service_name, ts, running, desired, pending)
		   select '111111111111', 'eu-west-1', 'c' || (1 + g % 100), 's' || g, date_trunc('minute', now()) - make_interval(mins => i * 5),
		     case when (g + i) % 50 = 0 then 2 else 3 end, case when g <= 20 and i > 500 then 2 else 3 end, 0
		   from generate_series(1, 600) g, generate_series(0, 2015) i`,
		`insert into tasks (account_id, region, task_arn, cluster_name, service_name, last_status, cpu, memory, capacity_provider, launch_type,
		     availability_zone, subnet_id, content_hash, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'run' || g, 'c' || (1 + g % 100), 's' || (1 + g % 600), 'RUNNING', '512', '1024',
		     case when g % 5 = 0 then 'FARGATE_SPOT' else 'FARGATE' end, 'FARGATE', 'eu-west-1a', 'subnet-' || (g % 50), 'h', '{}', now()
		   from generate_series(1, 1000) g`,
		`insert into tasks (account_id, region, task_arn, cluster_name, service_name, last_status, stopped_at, stop_reason_group, availability_zone,
		     containers, content_hash, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'stop' || g, 'c' || (1 + g % 100), 's' || (1 + g % 600), 'STOPPED',
		     now() - make_interval(mins => (g * 7) % 10080),
		     (array['Out of memory (exit 137)', 'Replaced by a deployment', 'Scaled in', 'Container exited with an error', 'Failed to start'])[1 + g % 5],
		     'eu-west-1' || (array['a', 'b', 'c'])[1 + g % 3],
		     jsonb_build_array(jsonb_build_object('Name', 'app', 'ExitCode', (array[0, 1, 137, 143, 255])[1 + g % 5])), 'h', '{}', now()
		   from generate_series(1, 20000) g`,
		`insert into service_events (account_id, region, cluster_name, service_name, event_id, created_at, message, content_hash, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'c' || (1 + g % 100), 's' || (1 + g % 600), 'e' || g, now() - make_interval(mins => (g * 3) % 43200),
		     case when g % 400 = 0 then '(service x) was unable to place a task.' else '(service x) has reached a steady state.' end, 'h', '{}', now()
		   from generate_series(1, 150000) g`,
		`analyze`,
	}
	for _, sql := range statements {
		if _, err := s.Pool.Exec(ctx, sql); err != nil {
			b.Fatalf("seed: %v\n%s", err, sql)
		}
	}
	return int64(tag.ID)
}

// BenchmarkAnalytics_Reliability_1_2M times the Reliability and Capacity store calls against 1.2M counts-history
// rows. It is skipped unless HM_BENCH=1 (seeding takes about a minute):
//
//	HM_BENCH=1 go test ./internal/store -run '^$' -bench Analytics_Reliability_1_2M -benchtime 1x
func BenchmarkAnalytics_Reliability_1_2M(b *testing.B) {
	if os.Getenv("HM_BENCH") != "1" {
		b.Skip("set HM_BENCH=1 to run")
	}
	s := storetest.Open(b)
	prod := seedAnalyticsScale(b, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(5 * time.Minute)
	from, to := now.Add(-7*24*time.Hour), now
	bucket := 4 * time.Hour
	f := store.TargetFilter{}

	for _, viewer := range []struct {
		name string
		vis  store.Visibility
	}{{"admin", store.Visibility{Admin: true}}, {"prod-only", store.Visibility{TagIDs: []int64{prod}}}} {
		calls := []struct {
			name string
			run  func() error
		}{
			{"AvailabilitySeries", func() error {
				_, err := s.AnalyticsAvailabilitySeries(ctx, f, viewer.vis, from, to, bucket)
				return err
			}},
			{"AvailabilityByService", func() error {
				_, err := s.AnalyticsAvailabilityByService(ctx, f, viewer.vis, from, to)
				return err
			}},
			{"Stops", func() error { _, err := s.AnalyticsStops(ctx, f, viewer.vis, from, to, bucket); return err }},
			{"PlacementFailures", func() error {
				_, err := s.AnalyticsPlacementFailures(ctx, f, viewer.vis, now.Add(-30*24*time.Hour), to)
				return err
			}},
			{"RunningTasks", func() error { _, err := s.AnalyticsRunningTasks(ctx, f, viewer.vis); return err }},
			{"AtScalingMax", func() error { _, err := s.AnalyticsAtScalingMax(ctx, f, viewer.vis); return err }},
			{"SubnetsLow", func() error { _, err := s.AnalyticsSubnetsLow(ctx, f, viewer.vis); return err }},
			{"ReservedTrend", func() error {
				_, err := s.AnalyticsReservedTrend(ctx, f, viewer.vis, from, to, bucket)
				return err
			}},
		}
		for _, call := range calls {
			b.Run(viewer.name+"/"+call.name, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if err := call.run(); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
