package rollup

import (
	"context"
	"fmt"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// availability rolls up every complete day after the table's watermark, at most backfillDays back. A watermark
// older than that window means the leader was down for longer than the source rows live: the days in between are
// logged as a gap and left empty.
func (r *Runner) availability(ctx context.Context, today time.Time, res Result) error {
	marks, err := r.Store.DailyWatermarks(ctx)
	if err != nil {
		return err
	}
	first := today.AddDate(0, 0, -backfillDays)
	if mark := marks[store.TableServiceAvailabilityDaily]; mark != nil {
		if next := mark.AddDate(0, 0, 1); next.After(first) {
			first = next
		} else if next.Before(first) {
			r.log().Warn("rollup: availability gap, source rows are past retention and will not be backfilled",
				"gap_from", next.Format(time.DateOnly), "gap_to", first.AddDate(0, 0, -1).Format(time.DateOnly))
		}
	}
	for d := first; d.Before(today); d = d.AddDate(0, 0, 1) {
		n, err := r.Store.RollupAvailabilityDay(ctx, d, r.sampleInterval())
		if err != nil {
			return fmt.Errorf("availability %s: %w", d.Format(time.DateOnly), err)
		}
		res[store.TableServiceAvailabilityDaily] += n
		if n == 0 {
			r.log().Info("rollup: no counts history for day, nothing recorded", "day", d.Format(time.DateOnly))
		}
	}
	return nil
}
