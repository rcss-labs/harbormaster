package metrics

import "sort"

func mapSeries(label string, in Series, fn func(float64) float64) Series {
	out := Series{ID: in.ID, Label: label, Values: make([]*float64, len(in.Values))}
	for i, v := range in.Values {
		if v != nil {
			r := fn(*v)
			out.Values[i] = &r
		}
	}
	return out
}

// Percent is 100*utilized/reserved clamped to [0,100]; nil when either side is nil or reserved is 0.
func Percent(utilized, reserved Series) Series {
	out := Series{ID: utilized.ID, Label: utilized.Label, Values: make([]*float64, len(utilized.Values))}
	for i, u := range utilized.Values {
		if u == nil || i >= len(reserved.Values) || reserved.Values[i] == nil || *reserved.Values[i] == 0 {
			continue
		}
		p := min(max(100**u / *reserved.Values[i], 0), 100)
		out.Values[i] = &p
	}
	return out
}

// PerMinute converts a per-period Sum into a per-minute rate.
func PerMinute(sum Series, period int) Series {
	return mapSeries(sum.Label, sum, func(v float64) float64 { return v * 60 / float64(period) })
}

// ToKBps converts bytes per second into KiB per second.
func ToKBps(bytesPerSecond Series) Series {
	return mapSeries(bytesPerSecond.Label, bytesPerSecond, func(v float64) float64 { return v / 1024 })
}

func combine(label string, in []Series, mean bool) Series {
	n := 0
	for _, s := range in {
		n = max(n, len(s.Values))
	}
	out := Series{ID: label, Label: label, Values: make([]*float64, n)}
	for i := range n {
		sum, count := 0.0, 0
		for _, s := range in {
			if i < len(s.Values) && s.Values[i] != nil {
				sum += *s.Values[i]
				count++
			}
		}
		if count == 0 {
			continue
		}
		if mean {
			sum /= float64(count)
		}
		out.Values[i] = &sum
	}
	return out
}

// SumSeries adds series bucket by bucket, ignoring nils; a bucket is nil only when all inputs are nil.
func SumSeries(label string, in ...Series) Series { return combine(label, in, false) }

// MeanSeries averages the non-nil values per bucket.
func MeanSeries(label string, in ...Series) Series { return combine(label, in, true) }

// Last returns the last non-nil value.
func Last(s Series) *float64 {
	for i := len(s.Values) - 1; i >= 0; i-- {
		if s.Values[i] != nil {
			return s.Values[i]
		}
	}
	return nil
}

// SumAll adds every non-nil value.
func SumAll(s Series) float64 {
	total := 0.0
	for _, v := range s.Values {
		if v != nil {
			total += *v
		}
	}
	return total
}

// TopWithOthers keeps the `keep` series with the highest Last() value (series
// with no data rank last) and folds the rest into one series via agg.
func TopWithOthers(in []Series, keep int, othersLabel string, agg func(string, ...Series) Series) []Series {
	if len(in) <= keep+1 {
		return in
	}
	ranked := append([]Series(nil), in...)
	rank := func(s Series) float64 {
		if l := Last(s); l != nil {
			return *l
		}
		return -1 << 62
	}
	sort.SliceStable(ranked, func(i, j int) bool { return rank(ranked[i]) > rank(ranked[j]) })
	out := append([]Series(nil), ranked[:keep]...)
	return append(out, agg(othersLabel, ranked[keep:]...))
}
