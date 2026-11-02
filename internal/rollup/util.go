package rollup

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/metrics"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// utilPeriod is one datapoint per query: the whole UTC day.
const utilPeriod = 24 * 60 * 60

// util rolls up the given complete UTC day for every enabled account and region that has no util rows for it yet.
// A throttled target is logged and retried by the next tick; other failures are joined and returned.
func (r *Runner) util(ctx context.Context, day time.Time, res Result) error {
	accounts, err := r.Store.ListAccounts(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		for _, region := range a.Regions {
			target := awsx.Target{AccountID: a.AccountID, Region: region}
			n, err := r.utilDay(ctx, target, day)
			switch {
			case err == nil:
				res[store.TableServiceUtilDaily] += n
			case ctx.Err() != nil:
				return ctx.Err()
			case awsx.IsThrottle(err):
				r.log().Warn("rollup: util throttled, day left undone until the next tick",
					"account", target.AccountID, "region", target.Region, "day", day.Format(time.DateOnly))
			case errors.Is(err, awsx.ErrAccountUnknown):
				r.log().Info("rollup: util skipped, account not connected", "account", target.AccountID, "region", target.Region)
			default:
				errs = append(errs, fmt.Errorf("util %s/%s: %w", target.AccountID, target.Region, err))
			}
		}
	}
	return errors.Join(errs...)
}

// utilDay writes service_util_daily for one target and day with a single logical GetMetricData batch (six
// queries per service, chunked at 500 by the metrics package). Clusters without Container Insights are skipped.
// The rows go in one transaction, so a failed or throttled fetch leaves the day undone. It returns rows written.
func (r *Runner) utilDay(ctx context.Context, target awsx.Target, day time.Time) (int, error) {
	filter := store.TargetFilter{AccountID: target.AccountID, Region: target.Region}
	if done, err := r.Store.UtilDayRecorded(ctx, filter, day); err != nil || done {
		return 0, err
	}
	clusters, err := r.Store.ListClusters(ctx, filter, store.Visibility{Admin: true})
	if err != nil {
		return 0, err
	}
	insights := map[string]bool{}
	for _, c := range clusters {
		if metrics.InsightsMode(c.Cluster) != "disabled" {
			insights[c.Key.Cluster] = true
		}
	}
	reservations, err := r.Store.ServiceReservations(ctx, filter)
	if err != nil {
		return 0, err
	}
	services := reservations[:0]
	for _, s := range reservations {
		if insights[s.Key.Cluster] {
			services = append(services, s)
		}
	}
	if len(services) == 0 {
		return 0, nil
	}

	queries := make([]metrics.Query, 0, len(services)*6)
	for i, s := range services {
		queries = append(queries, metrics.UtilQueries(i, s.Key.Cluster, s.Key.Service, utilPeriod)...)
	}
	api, err := r.Clients.CloudWatch(ctx, target)
	if err != nil {
		return 0, err
	}
	values, err := metrics.FetchValues(ctx, api, queries, day, day.AddDate(0, 0, 1))
	if err != nil {
		return 0, err
	}

	rows := make([]store.ServiceUtilRow, len(services))
	for i, s := range services {
		id := func(prefix string) string { return fmt.Sprintf("%s%d", prefix, i) }
		cpuReserved, memReserved := lookup(values, id(metrics.UtilCPUReserved)), lookup(values, id(metrics.UtilMemReserved))
		rows[i] = store.ServiceUtilRow{
			Key:    s.Key,
			CPUAvg: percent(lookup(values, id(metrics.UtilCPUAvg)), cpuReserved),
			CPUP95: percent(lookup(values, id(metrics.UtilCPUP95)), cpuReserved),
			MemAvg: percent(lookup(values, id(metrics.UtilMemAvg)), memReserved),
			MemP95: percent(lookup(values, id(metrics.UtilMemP95)), memReserved),
			// Reserved size is whatever the service's task definition says now, not what ran that day.
			CPUReservedUnits: s.CPUUnits, MemReservedMiB: s.MemoryMiB,
		}
	}
	if err := r.Store.UpsertServiceUtilDay(ctx, day, rows); err != nil {
		return 0, err
	}
	return len(rows), nil
}

func lookup(values map[string]float64, id string) *float64 {
	if v, ok := values[id]; ok {
		return &v
	}
	return nil
}

// percent is 100*used/reserved clamped to [0,100]; nil when either is missing or reserved is not positive.
func percent(used, reserved *float64) *float64 {
	if used == nil || reserved == nil || *reserved <= 0 {
		return nil
	}
	p := math.Min(math.Max(100**used / *reserved, 0), 100)
	return &p
}
