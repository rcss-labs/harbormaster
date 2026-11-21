package lease

import (
	"cmp"
	"slices"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// SliceState is one slice as the planner sees it. Owner is uuid.Nil when the slice is free or its lease expired.
type SliceState struct {
	Key    store.LeaseKey
	Weight int // clusters, at least 1
	Owner  uuid.UUID
}

// Plan is what a member should do this tick. A plan never both claims and gives back.
type Plan struct {
	Claim    []store.LeaseKey
	GiveBack *store.LeaseKey
}

func compareKeys(a, b store.LeaseKey) int {
	return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.AccountID, b.AccountID), cmp.Compare(a.Region, b.Region))
}

func compareIDs(a, b uuid.UUID) int { return slices.Compare(a[:], b[:]) }

func weightOf(s SliceState) int { return max(s.Weight, 1) }

// Rebalance is pure: what should self do this tick? Fair share is the total weight over the number of live pollers
// (integer division; self is always counted), so rounding never leaves a slice that nobody is entitled to take.
//
//   - Claim free slices heaviest first (ties by key) while the weight held, planned claims included, is below the share.
//   - If free slices would still remain and every live member is at or above its share, the lightest member (ties by
//     lowest instance id) takes them all, so nothing stays unowned.
//   - Give back at most one slice, the lightest self holds, only when mayGiveBack, the weight left after giving it back
//     still exceeds the share, and another live member is below its share.
//
// A slice owned by an id that is not in livePollers is not free: its lease expiry decides, never membership.
func Rebalance(self uuid.UUID, livePollers []uuid.UUID, all []SliceState, mayGiveBack bool) Plan {
	members := map[uuid.UUID]bool{self: true}
	for _, id := range livePollers {
		members[id] = true
	}
	total := 0
	held := map[uuid.UUID]int{}
	var free []SliceState
	var mine []SliceState
	for _, s := range all {
		w := weightOf(s)
		total += w
		switch {
		case s.Owner == uuid.Nil:
			free = append(free, s)
		case members[s.Owner]:
			held[s.Owner] += w
			if s.Owner == self {
				mine = append(mine, s)
			}
		}
	}
	share := total / len(members)

	var plan Plan
	slices.SortFunc(free, func(a, b SliceState) int {
		return cmp.Or(cmp.Compare(weightOf(b), weightOf(a)), compareKeys(a.Key, b.Key))
	})
	mineHeld := held[self]
	taken := 0
	for taken < len(free) && mineHeld < share {
		plan.Claim = append(plan.Claim, free[taken].Key)
		mineHeld += weightOf(free[taken])
		taken++
	}
	held[self] = mineHeld

	if taken < len(free) && isLightest(self, members, held) && allAtOrAbove(members, held, share) {
		for _, s := range free[taken:] {
			plan.Claim = append(plan.Claim, s.Key)
		}
	}
	if len(plan.Claim) > 0 || !mayGiveBack || len(mine) == 0 {
		return plan
	}

	lightest := slices.MinFunc(mine, func(a, b SliceState) int {
		return cmp.Or(cmp.Compare(weightOf(a), weightOf(b)), compareKeys(a.Key, b.Key))
	})
	if mineHeld-weightOf(lightest) <= share {
		return plan
	}
	for id := range members {
		if id != self && held[id] < share {
			k := lightest.Key
			plan.GiveBack = &k
			break
		}
	}
	return plan
}

func allAtOrAbove(members map[uuid.UUID]bool, held map[uuid.UUID]int, share int) bool {
	for id := range members {
		if held[id] < share {
			return false
		}
	}
	return true
}

// isLightest reports whether self holds the least weight of all members, ties broken by the lowest instance id.
func isLightest(self uuid.UUID, members map[uuid.UUID]bool, held map[uuid.UUID]int) bool {
	for id := range members {
		if id == self {
			continue
		}
		if held[id] < held[self] || held[id] == held[self] && compareIDs(id, self) < 0 {
			return false
		}
	}
	return true
}
