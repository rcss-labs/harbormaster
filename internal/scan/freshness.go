package scan

import "time"

// Freshness is the age band of the vulnerability DB.
type Freshness string

const (
	FreshnessFresh Freshness = "fresh"
	FreshnessWarn  Freshness = "warn"
	FreshnessStale Freshness = "stale"
	FreshnessNone  Freshness = "none"
)

// DBFreshness bands the DB age: warn from DBWarnAge, stale from staleAfter.
// Stale wins when staleAfter is configured below the warn age.
func DBFreshness(builtAt *time.Time, now time.Time, staleAfter time.Duration) Freshness {
	if builtAt == nil {
		return FreshnessNone
	}
	age := now.Sub(*builtAt)
	switch {
	case age >= staleAfter:
		return FreshnessStale
	case age >= DBWarnAge:
		return FreshnessWarn
	}
	return FreshnessFresh
}
