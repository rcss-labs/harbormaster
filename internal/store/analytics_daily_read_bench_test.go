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

// BenchmarkAnalytics_DailyReads_Year times the rollup-table reads against a year of data: 600 services x 365
// days of availability and utilisation, 100 clusters x 365 days of capacity and 2,000 deployments with
// failure reasons. Skipped unless HM_BENCH=1:
//
//	HM_BENCH=1 go test ./internal/store -run '^$' -bench Analytics_DailyReads_Year -benchtime 5x
func BenchmarkAnalytics_DailyReads_Year(b *testing.B) {
	if os.Getenv("HM_BENCH") != "1" {
		b.Skip("set HM_BENCH=1 to run")
	}
	s := storetest.Open(b)
	ctx := context.Background()
	tag, err := s.CreateTag(ctx, "Prod", "red")
	if err != nil {
		b.Fatal(err)
	}
	prod := int64(tag.ID)
	for _, sql := range []string{
		`insert into clusters (account_id, region, cluster_name, content_hash, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'c' || g, 'h', '{}', now() from generate_series(1, 100) g`,
		`insert into cluster_tags (account_id, region, cluster_name, tag_id)
		   select '111111111111', 'eu-west-1', 'c' || g, ` + strconv.FormatInt(prod, 10) + ` from generate_series(1, 100, 2) g`,
		`insert into services (account_id, region, cluster_name, service_name, desired, running, content_hash, raw, polled_at)
		   select '111111111111', 'eu-west-1', 'c' || (1 + g % 100), 's' || g, 3, 3, 'h', '{}', now() from generate_series(1, 600) g`,
		`insert into service_availability_daily
		   select '111111111111', 'eu-west-1', 'c' || (1 + g % 100), 's' || g, current_date - d, 288, 288 - (g + d) % 7, ((g + d) % 7) * 5, (g + d) % 3
		   from generate_series(1, 600) g, generate_series(1, 365) d`,
		`insert into service_util_daily
		   select '111111111111', 'eu-west-1', 'c' || (1 + g % 100), 's' || g, current_date - d, 20 + d % 10, 40 + d % 30, 30, 50 + d % 20, 512, 1024
		   from generate_series(1, 600) g, generate_series(1, 365) d`,
		`insert into cluster_capacity_daily
		   select '111111111111', 'eu-west-1', 'c' || g, current_date - d, 288, 288 * 4.5, 6, 288 * 9, 12, 288 * 10, 14, 288 * 2
		   from generate_series(1, 100) g, generate_series(0, 364) d`,
		`insert into deployment_history (account_id, region, cluster_name, service_name, deployment_id, revision, kind, started_at, finished_at, outcome, failure_reason, failure_detail)
		   select '111111111111', 'eu-west-1', 'c' || (1 + g % 100), 's' || g, 'd' || g || '-' || d, d, 'deploy', now() - make_interval(hours => d * 2 + g % 5),
		     now() - make_interval(hours => d * 2 + g % 5) + interval '3 minutes', case when d % 6 = 0 then 'failed' else 'completed' end,
		     case when d % 6 = 0 then 'circuit_breaker' end, case when d % 6 = 0 then 'tasks failed to start' end
		   from generate_series(1, 600) g, generate_series(1, 10) d`,
		`analyze`,
	} {
		if _, err := s.Pool.Exec(ctx, sql); err != nil {
			b.Fatalf("seed: %v\n%s", err, sql)
		}
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from, to := today.AddDate(0, 0, -89), today.AddDate(0, 0, 1)
	f := store.TargetFilter{}
	for _, viewer := range []struct {
		name string
		vis  store.Visibility
	}{{"admin", store.Visibility{Admin: true}}, {"prod-only", store.Visibility{TagIDs: []int64{prod}}}} {
		calls := []struct {
			name string
			run  func() error
		}{
			{"AvailabilityDaily", func() error { _, err := s.AnalyticsAvailabilityDaily(ctx, f, viewer.vis, from, to); return err }},
			{"AvailabilityTrend", func() error { _, err := s.AnalyticsAvailabilityTrend(ctx, f, viewer.vis, today); return err }},
			{"CapacityDaily", func() error { _, err := s.AnalyticsCapacityDaily(ctx, f, viewer.vis, from, to); return err }},
			{"UtilDaily", func() error { _, err := s.AnalyticsUtilDaily(ctx, f, viewer.vis, from, to); return err }},
			{"OverReserved", func() error { _, err := s.AnalyticsOverReserved(ctx, f, viewer.vis, from); return err }},
			{"RecentFailures", func() error {
				_, _, err := s.AnalyticsRecentFailures(ctx, f, viewer.vis, today.AddDate(0, 0, -90), to, 50)
				return err
			}},
			{"FailureReasonSince", func() error { _, err := s.AnalyticsFailureReasonSince(ctx, f, viewer.vis); return err }},
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
