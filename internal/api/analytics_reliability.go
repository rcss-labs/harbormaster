package api

import (
	"net/http"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/status"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	maxZoneLines     = 8
	maxExitCodeLines = 4
	topTableRows     = 50
	otherKey         = "other"
)

// shortestRetention is the smaller of two retentions, where zero means kept forever.
func shortestRetention(a, b time.Duration) time.Duration {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	}
	return min(a, b)
}

// keptText is a retention as shown in the "Data used" table.
func keptText(d time.Duration) string {
	if text := formatRetention(d); text != "" {
		return text
	}
	return "forever"
}

func settingPtr(name string) *string { return &name }

// countsInterval is the counts-history sampling interval; samples are assumed to be that far apart.
func (s *Server) countsInterval() time.Duration {
	if s.Cfg.Poll.CountsHistory > 0 {
		return s.Cfg.Poll.CountsHistory
	}
	return analyticsPollStep
}

// analyticsReliability answers GET /api/analytics/reliability?range=&account=&region=&tag=.
func (s *Server) analyticsReliability(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	requested, err := analyticsRangeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	cfg := s.Cfg.Retention
	now := s.now()
	retention := shortestRetention(cfg.CountsHistory, cfg.StoppedTasks)
	win := clampWindow(requested, retention, now)
	eventsWin := clampWindow(requested, cfg.ServiceEvents, now)
	interval := s.countsInterval()
	daily, err := s.Store.DailyDataSince(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	availabilitySince := daily[store.TableServiceAvailabilityDaily]
	dailyWin := newDailyWindow(now)

	var (
		dailyPoints []store.DailyAvailabilityPoint
		trend       []store.AvailabilityTrendFact
		buckets     []store.AvailBucket
		perSvc      []store.ServiceAvail
		stops       store.StopsResult
		placement   int
		tags        map[authz.TagID]TagDTO
		polledAt    time.Time
	)
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() (err error) {
		buckets, err = s.Store.AnalyticsAvailabilitySeries(gctx, sc.Filter, sc.Vis, win.from, win.to, win.bucket)
		return err
	})
	group.Go(func() (err error) {
		perSvc, err = s.Store.AnalyticsAvailabilityByService(gctx, sc.Filter, sc.Vis, win.from, win.to)
		return err
	})
	group.Go(func() (err error) {
		stops, err = s.Store.AnalyticsStops(gctx, sc.Filter, sc.Vis, win.from, win.to, win.bucket)
		return err
	})
	group.Go(func() (err error) {
		placement, err = s.Store.AnalyticsPlacementFailures(gctx, sc.Filter, sc.Vis, eventsWin.from, eventsWin.to)
		return err
	})
	if availabilitySince != nil {
		group.Go(func() (err error) {
			dailyPoints, err = s.Store.AnalyticsAvailabilityDaily(gctx, sc.Filter, sc.Vis, dailyWin.from, dailyWin.to())
			return err
		})
		group.Go(func() (err error) {
			trend, err = s.Store.AnalyticsAvailabilityTrend(gctx, sc.Filter, sc.Vis, now)
			return err
		})
	}
	group.Go(func() (err error) {
		tags, err = s.tagDTOs(gctx)
		return err
	})
	group.Go(func() (err error) {
		polledAt, err = s.Store.PolledAt(gctx, sc.Filter)
		return err
	})
	if err := group.Wait(); err != nil {
		s.fail(w, err)
		return
	}

	dto := newReliabilityDTO()
	dto.Tab = "reliability"
	dto.Window = win.dto(retention, true)
	dto.PolledAt = polledAt
	dto.Sources = reliabilitySources(s, win, eventsWin)
	fillAvailability(&dto, win, buckets)
	fillServiceAvailability(&dto, perSvc, stops, tags, interval)
	fillStops(&dto, win, stops)
	dto.KPIs.PlacementFailures = placement
	if availabilitySince != nil {
		fillReliabilityDaily(&dto, availabilitySince, dailyWin, dailyPoints, trend, tags)
		dto.Sources = append(dto.Sources, prunedTableSource(store.TableServiceAvailabilityDaily,
			"Daily availability and time below desired per service, for the 90-day panels",
			cfg.AvailabilityDaily, "HM_RETENTION_AVAILABILITY_DAILY"))
	}
	writeJSON(w, http.StatusOK, dto)
}

func reliabilitySources(s *Server, tab, events windowSpec) []AnalyticsSourceDTO {
	cfg := s.Cfg.Retention
	eventsSource := AnalyticsSourceDTO{
		Table: "service_events", Reads: "Service events saying a task could not be placed",
		Kept: keptText(cfg.ServiceEvents), Setting: settingPtr("HM_RETENTION_SERVICE_EVENTS"),
	}
	if events.used != tab.used {
		used := string(events.used)
		eventsSource.WindowUsed = &used
	}
	return []AnalyticsSourceDTO{
		{Table: "service_counts_history", Reads: "Running against desired count, sampled every poll",
			Kept: keptText(cfg.CountsHistory), Setting: settingPtr("HM_RETENTION_COUNTS_HISTORY")},
		{Table: "tasks", Reads: "Stopped tasks: reason, availability zone and container exit codes",
			Kept: keptText(cfg.StoppedTasks), Setting: settingPtr("HM_RETENTION_STOPPED_TASKS")},
		eventsSource,
	}
}

// fillAvailability sets the availability and services-below series and the availability KPI.
func fillAvailability(dto *ReliabilityDTO, win windowSpec, buckets []store.AvailBucket) {
	availability := nullLine(win, "availability", "Availability")
	below := zeroLine(win, "services_below", "Services below desired", false)
	var samples, ok int
	for _, b := range buckets {
		index := bucketIndex(b.Bucket, win)
		if b.Samples > 0 {
			availability.set(index, 100*float64(b.OK)/float64(b.Samples))
		}
		below.add(index, float64(b.BelowServices))
		samples += b.Samples
		ok += b.OK
	}
	dto.Availability = newBucketedSeries(win)
	dto.Availability.Series = append(dto.Availability.Series, availability)
	dto.ServicesBelow = newBucketedSeries(win)
	dto.ServicesBelow.Series = append(dto.ServicesBelow.Series, below)
	if samples > 0 {
		share := float64(ok) / float64(samples)
		dto.KPIs.Availability = &share
	}
}

// fillServiceAvailability sets the time-below KPIs and the least-available table.
func fillServiceAvailability(dto *ReliabilityDTO, perSvc []store.ServiceAvail, stops store.StopsResult, tags map[authz.TagID]TagDTO, interval time.Duration) {
	unexpected := map[string]store.ServiceStops{}
	for _, entry := range stops.ByService {
		unexpected[entry.Key.ID()] = entry
	}
	var rows []ReliabilityServiceRowDTO
	for _, svc := range perSvc {
		if svc.Samples == 0 {
			continue
		}
		minutes := svc.MinutesBelow(interval)
		dto.KPIs.MinutesBelowDesired += minutes
		if svc.Below == 0 {
			continue
		}
		dto.KPIs.ServicesBelow++
		row := ReliabilityServiceRowDTO{
			ServiceID: svc.Key.ID(), Service: svc.Key.Service, Cluster: svc.Key.Cluster,
			Tag:          inheritedTag(tags, svc.EffectiveTagID, svc.TagInherited),
			Availability: float64(svc.OK) / float64(svc.Samples), MinutesBelow: minutes,
		}
		if entry, found := unexpected[row.ServiceID]; found {
			row.UnexpectedStops = entry.Unexpected
			if entry.TopReason != "" {
				reason := entry.TopReason
				row.TopReason = &reason
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Availability != b.Availability {
			return a.Availability < b.Availability
		}
		if a.MinutesBelow != b.MinutesBelow {
			return a.MinutesBelow > b.MinutesBelow
		}
		return a.ServiceID < b.ServiceID
	})
	dto.LeastAvailable.Total = len(rows)
	dto.LeastAvailable.Rows = rows[:min(len(rows), topTableRows)]
	if dto.LeastAvailable.Rows == nil {
		dto.LeastAvailable.Rows = []ReliabilityServiceRowDTO{}
	}
}

// fillStops sets the three stop charts and the stop KPIs.
func fillStops(dto *ReliabilityDTO, win windowSpec, stops store.StopsResult) {
	byReason := newBucketedSeries(win)
	for _, group := range status.StopReasonGroups {
		byReason.Series = append(byReason.Series, zeroLine(win, group, group, store.IsExpectedStopReason(group)))
	}
	lineOf := map[string]*SeriesLineDTO{}
	for i := range byReason.Series {
		lineOf[byReason.Series[i].Key] = &byReason.Series[i]
	}
	for _, c := range stops.ByReason {
		count := float64(c.Count)
		dto.KPIs.TaskStops += c.Count
		if !store.IsExpectedStopReason(c.Key) {
			dto.KPIs.UnexpectedStops += c.Count
		}
		if c.Key == status.StopReasonGroups[0] {
			dto.KPIs.OutOfMemory += c.Count
		}
		if line := lineOf[c.Key]; line != nil {
			line.add(bucketIndex(c.Bucket, win), count)
		}
	}
	dto.StopsByReason = byReason
	dto.UnexpectedByZone = topKeysSeries(win, stops.UnexpectedByZone, maxZoneLines, func(key string) string {
		if key == "" {
			return "unknown"
		}
		return key
	})
	dto.ExitCodes = topKeysSeries(win, stops.ExitCodes, maxExitCodeLines, func(key string) string { return key })
}

// topKeysSeries turns per-bucket counts into one zero-filled line per key: the maxLines most frequent keys
// and one "other" line for the rest (only when something is left over).
func topKeysSeries(win windowSpec, counts []store.StopCount, maxLines int, label func(string) string) SeriesDTO {
	totals := map[string]int{}
	for _, c := range counts {
		totals[c.Key] += c.Count
	}
	keys := make([]string, 0, len(totals))
	for key := range totals {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if totals[keys[i]] != totals[keys[j]] {
			return totals[keys[i]] > totals[keys[j]]
		}
		return keys[i] < keys[j]
	})
	series := newBucketedSeries(win)
	lines := map[string]*SeriesLineDTO{}
	kept := keys[:min(len(keys), maxLines)]
	for _, key := range kept {
		series.Series = append(series.Series, zeroLine(win, key, label(key), false))
	}
	if len(keys) > maxLines {
		series.Series = append(series.Series, zeroLine(win, otherKey, "Other", false))
	}
	for i := range series.Series {
		lines[series.Series[i].Key] = &series.Series[i]
	}
	for _, c := range counts {
		line := lines[c.Key]
		if line == nil {
			line = lines[otherKey]
		}
		line.add(bucketIndex(c.Bucket, win), float64(c.Count))
	}
	return series
}
