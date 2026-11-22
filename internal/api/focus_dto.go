package api

import "time"

// FocusRequest names the page that is open: kind is "cluster" or "service". Open is true on the first heartbeat of a
// page view (and when the tab becomes visible again), which also asks for an immediate read.
type FocusRequest struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Open bool   `json:"open"`
}

// FocusDTO is what the page shows about its own freshness. State is live, capped, user_capped, throttled or
// unavailable; RefreshedAt is the last successful focus read and is null until one lands.
type FocusDTO struct {
	State           string     `json:"state"`
	IntervalSeconds int        `json:"interval_seconds"`
	RefreshedAt     *time.Time `json:"refreshed_at"`
	Message         string     `json:"message"`
	// Failed is true for a definite fault (the store is down, the registry errored or is absent), as opposed to a page
	// that is only waiting for the poller's first read. The page's grace for the latter never applies to the former.
	Failed bool `json:"failed"`
}
