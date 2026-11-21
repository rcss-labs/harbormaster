// Package lease decides who owns which polling slice and the coordinator role: timing constants, the weighted
// rebalance planner, the shared advisory gate that keeps old and new tasks from both acting during an upgrade, and the
// manager that claims, renews, gives back and releases leases in the database.
package lease

import "time"

// Timing holds every duration the lease logic uses. Tests shorten them; production uses DefaultTiming.
type Timing struct {
	// TTL is how long a claim or renewal keeps a lease alive in the database.
	TTL time.Duration
	// RenewEvery is how often a held lease is renewed.
	RenewEvery time.Duration
	// Tick is the claim and rebalance period.
	Tick time.Duration
	// MemberHeartbeat is how often a member row is beaten; MemberLive is how long a beat counts as alive.
	MemberHeartbeat, MemberLive time.Duration
	// SelfFenceAfter stops a holder when its last successful renewal is older than this on the monotonic clock,
	// before the database lease can expire.
	SelfFenceAfter time.Duration
	// GiveBackEvery limits how often one slice is handed back during rebalancing.
	GiveBackEvery time.Duration
	// GuardMinRemaining is how much lease must be left for a guarded side effect to go ahead.
	GuardMinRemaining time.Duration
}

// DefaultTiming is the design's timing: a crashed owner's slices are taken over within TTL plus one tick (35 s), a
// graceful stop hands over within one tick.
func DefaultTiming() Timing {
	return Timing{
		TTL:               30 * time.Second,
		RenewEvery:        10 * time.Second,
		Tick:              5 * time.Second,
		MemberHeartbeat:   10 * time.Second,
		MemberLive:        30 * time.Second,
		SelfFenceAfter:    20 * time.Second,
		GiveBackEvery:     30 * time.Second,
		GuardMinRemaining: 5 * time.Second,
	}
}
