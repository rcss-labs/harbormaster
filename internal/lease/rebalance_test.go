package lease

import (
	"fmt"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/store"
)

func member(n byte) uuid.UUID { return uuid.UUID{15: n} }

func key(name string) store.LeaseKey {
	return store.LeaseKey{Kind: store.LeaseSlice, AccountID: name, Region: "r"}
}

func slice(name string, weight int, owner uuid.UUID) SliceState {
	return SliceState{Key: key(name), Weight: weight, Owner: owner}
}

func keyNames(keys []store.LeaseKey) []string {
	var out []string
	for _, k := range keys {
		out = append(out, k.AccountID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// simulation applies the planner for several members round after round against one shared set of slices, the way
// managers sharing a database would (each member sees what the previous member did within a round).
type simulation struct {
	members []uuid.UUID
	slices  []SliceState
}

func (s *simulation) index(k store.LeaseKey) int {
	for i := range s.slices {
		if s.slices[i].Key == k {
			return i
		}
	}
	panic("unknown slice " + k.AccountID)
}

// round returns the number of claims and give-backs that happened.
func (s *simulation) round() (claims, giveBacks int) {
	for _, m := range s.members {
		plan := Rebalance(m, s.members, s.slices, true)
		if len(plan.Claim) > 0 && plan.GiveBack != nil {
			panic("a plan must not both claim and give back")
		}
		for _, k := range plan.Claim {
			i := s.index(k)
			if s.slices[i].Owner != uuid.Nil {
				continue // lost the race to an earlier member in this round
			}
			s.slices[i].Owner = m
			claims++
		}
		if plan.GiveBack != nil {
			i := s.index(*plan.GiveBack)
			if s.slices[i].Owner != m {
				panic("gave back a slice it does not hold")
			}
			s.slices[i].Owner = uuid.Nil
			giveBacks++
		}
	}
	return claims, giveBacks
}

func (s *simulation) held() map[uuid.UUID]int {
	out := map[uuid.UUID]int{}
	for _, m := range s.members {
		out[m] = 0
	}
	for _, sl := range s.slices {
		if _, ok := out[sl.Owner]; ok {
			out[sl.Owner] += sl.Weight
		}
	}
	return out
}

func (s *simulation) unowned() []string {
	var out []string
	for _, sl := range s.slices {
		if sl.Owner == uuid.Nil {
			out = append(out, sl.Key.AccountID)
		}
	}
	return out
}

// settle runs rounds until one changes nothing and returns how many rounds it took.
func (s *simulation) settle(t *testing.T, maxRounds int) int {
	t.Helper()
	for i := 1; i <= maxRounds; i++ {
		c, g := s.round()
		if c == 0 && g == 0 {
			return i
		}
	}
	t.Fatalf("not settled after %d rounds: held %v, unowned %v", maxRounds, s.held(), s.unowned())
	return 0
}

func (s *simulation) totalWeight() (total, heaviest int) {
	for _, sl := range s.slices {
		total += sl.Weight
		heaviest = max(heaviest, sl.Weight)
	}
	return total, heaviest
}

func TestRebalance_SinglePollerClaimsEverything(t *testing.T) {
	me := member(1)
	slices := []SliceState{slice("b", 1, uuid.Nil), slice("a", 3, uuid.Nil), slice("c", 2, uuid.Nil)}
	plan := Rebalance(me, []uuid.UUID{me}, slices, true)
	if got := keyNames(plan.Claim); !equalStrings(got, []string{"a", "c", "b"}) {
		t.Fatalf("claims = %v, want every slice heaviest first [a c b]", got)
	}
	if plan.GiveBack != nil {
		t.Fatalf("give back = %v, want none", plan.GiveBack)
	}
}

func TestRebalance_SelfIsCountedEvenWhenMissingFromTheLiveList(t *testing.T) {
	me := member(1)
	slices := []SliceState{slice("a", 1, uuid.Nil)}
	if plan := Rebalance(me, nil, slices, true); len(plan.Claim) != 1 {
		t.Fatalf("claims = %v, want the one free slice", plan.Claim)
	}
}

func TestRebalance_ClaimsHeaviestFirstUpToFairShare(t *testing.T) {
	a, b := member(1), member(2)
	slices := []SliceState{
		slice("w1", 1, uuid.Nil), slice("w5", 5, uuid.Nil), slice("w2", 2, uuid.Nil),
		slice("w4", 4, uuid.Nil), slice("w3", 3, uuid.Nil), slice("w4b", 4, uuid.Nil),
	}
	// Total 19, two pollers, fair share 9: take 5 (0 < 9), then 4 (5 < 9) which reaches 9, then stop.
	plan := Rebalance(a, []uuid.UUID{a, b}, slices, true)
	if got := keyNames(plan.Claim); !equalStrings(got, []string{"w5", "w4"}) {
		t.Fatalf("claims = %v, want [w5 w4] (heaviest first, ties by key, stop at the share)", got)
	}
}

func TestRebalance_LightestMemberTakesLeftovers(t *testing.T) {
	a, b, c := member(1), member(2), member(3)
	live := []uuid.UUID{a, b, c}
	var slices []SliceState
	for i := range 3 {
		slices = append(slices, slice(fmt.Sprintf("a%d", i), 1, a), slice(fmt.Sprintf("b%d", i), 1, b), slice(fmt.Sprintf("c%d", i), 1, c))
	}
	slices = append(slices, slice("left1", 1, uuid.Nil))
	// Total 10, three pollers, fair share 3: everyone holds 3, one slice is free. Only the lightest (lowest id on a tie) takes it.
	for _, tc := range []struct {
		self uuid.UUID
		want []string
	}{{a, []string{"left1"}}, {b, nil}, {c, nil}} {
		plan := Rebalance(tc.self, live, slices, true)
		if got := keyNames(plan.Claim); !equalStrings(got, tc.want) {
			t.Fatalf("member %v claims = %v, want %v", tc.self[15], got, tc.want)
		}
	}
	// When b is lighter than the others, b is the one.
	lighter := append([]SliceState(nil), slices...)
	lighter[1].Owner = uuid.Nil // one of b's slices is free too
	lighter[1].Key = key("freed")
	for _, tc := range []struct {
		self uuid.UUID
		want int
	}{{a, 0}, {b, 1}, {c, 0}} {
		// b holds 2 (share 3) so it is below its share and claims one of the two free slices; a and c leave them,
		// because a member below its share exists.
		if got := len(Rebalance(tc.self, live, lighter, true).Claim); got != tc.want {
			t.Fatalf("member %v claims %d, want %d", tc.self[15], got, tc.want)
		}
	}
}

func TestRebalance_GivesBackOneSliceOnlyWhenWorthIt(t *testing.T) {
	a, b := member(1), member(2)
	live := []uuid.UUID{a, b}

	t.Run("sheds its lightest slice when far above share and another member is below", func(t *testing.T) {
		slices := []SliceState{slice("big", 5, a), slice("s2", 1, a), slice("s1", 1, a)}
		plan := Rebalance(a, live, slices, true)
		if len(plan.Claim) != 0 || plan.GiveBack == nil || *plan.GiveBack != key("s1") {
			t.Fatalf("plan = %+v, want a give back of s1 (lightest, ties by key)", plan)
		}
	})
	t.Run("not when the rate limit has not elapsed", func(t *testing.T) {
		slices := []SliceState{slice("big", 5, a), slice("s2", 1, a), slice("s1", 1, a)}
		if plan := Rebalance(a, live, slices, false); plan.GiveBack != nil {
			t.Fatalf("give back = %v, want none", plan.GiveBack)
		}
	})
	t.Run("not when the excess is smaller than its lightest slice", func(t *testing.T) {
		slices := []SliceState{slice("a1", 3, a), slice("a2", 3, a), slice("b1", 3, b), slice("b2", 1, b)}
		// total 10, fair 5: a holds 6, lightest 3: 6-3 = 3 does not exceed the share.
		if plan := Rebalance(a, live, slices, true); plan.GiveBack != nil {
			t.Fatalf("give back = %v, want none", plan.GiveBack)
		}
	})
	t.Run("not when no other member is below its share", func(t *testing.T) {
		c := member(3)
		slices := []SliceState{slice("a1", 4, a), slice("a2", 1, a), slice("b1", 3, b), slice("c1", 3, c)}
		// total 11, three pollers, fair 3 (floor): a holds 5 and 5-1 > 3, but b and c hold 3 each, so nobody needs a slice.
		if plan := Rebalance(a, []uuid.UUID{a, b, c}, slices, true); plan.GiveBack != nil || len(plan.Claim) != 0 {
			t.Fatalf("plan = %+v, want nothing", plan)
		}
	})
}

func TestRebalance_NoMovementWhenFair(t *testing.T) {
	for name, weights := range map[string][]int{
		"equal":   {1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		"unequal": {6, 6, 6, 4, 4, 4, 3, 3, 3, 2, 2, 2}, // 15 each
	} {
		t.Run(name, func(t *testing.T) {
			members := []uuid.UUID{member(1), member(2), member(3)}
			sim := &simulation{members: members}
			for i, w := range weights {
				sim.slices = append(sim.slices, slice(fmt.Sprintf("s%02d", i), w, members[i%3]))
			}
			before := fmt.Sprint(sim.held())
			for round := 1; round <= 20; round++ {
				if c, g := sim.round(); c != 0 || g != 0 {
					t.Fatalf("round %d: %d claims and %d give-backs, want none (held %v)", round, c, g, sim.held())
				}
			}
			if after := fmt.Sprint(sim.held()); after != before {
				t.Fatalf("held changed from %s to %s", before, after)
			}
		})
	}
}

func TestRebalance_ConvergesWhenPollersAreAdded(t *testing.T) {
	weights := []int{50, 30, 20, 20, 10, 10, 5, 5, 3, 3, 2, 1}
	sim := &simulation{members: []uuid.UUID{member(1)}}
	for i, w := range weights {
		sim.slices = append(sim.slices, slice(fmt.Sprintf("s%02d", i), w, uuid.Nil))
	}
	total, heaviest := sim.totalWeight()

	check := func(stage string) {
		t.Helper()
		if un := sim.unowned(); len(un) != 0 {
			t.Fatalf("%s: unowned slices %v", stage, un)
		}
		fair := total / len(sim.members)
		for m, w := range sim.held() {
			if d := w - fair; d > heaviest || -d > heaviest {
				t.Fatalf("%s: member %v holds %d, fair share %d, off by more than the heaviest slice (%d)", stage, m[15], w, fair, heaviest)
			}
		}
		// Settled means stable: a further round moves nothing.
		if c, g := sim.round(); c != 0 || g != 0 {
			t.Fatalf("%s: still moving after settling (%d claims, %d give-backs)", stage, c, g)
		}
	}

	sim.settle(t, 5)
	check("1 poller")
	if h := sim.held()[member(1)]; h != total {
		t.Fatalf("single poller holds %d, want everything (%d)", h, total)
	}

	sim.members = append(sim.members, member(2))
	rounds2 := sim.settle(t, 100)
	check("2 pollers")

	sim.members = append(sim.members, member(3), member(4))
	rounds4 := sim.settle(t, 100)
	check("4 pollers")
	t.Logf("settled in %d rounds for 2 pollers and %d more for 4", rounds2, rounds4)
}

func TestRebalance_RemovedPollerSlicesAreReclaimed(t *testing.T) {
	members := []uuid.UUID{member(1), member(2), member(3)}
	sim := &simulation{members: members}
	for i := range 12 {
		sim.slices = append(sim.slices, slice(fmt.Sprintf("s%02d", i), 1+i%4, members[i%3]))
	}
	sim.settle(t, 20)

	// member 3 stops: its slices are freed (graceful) and it is no longer live.
	sim.members = members[:2]
	for i := range sim.slices {
		if sim.slices[i].Owner == member(3) {
			sim.slices[i].Owner = uuid.Nil
		}
	}
	sim.settle(t, 50)
	if un := sim.unowned(); len(un) != 0 {
		t.Fatalf("unowned after a poller left: %v", un)
	}
	total, heaviest := sim.totalWeight()
	for m, w := range sim.held() {
		if d := w - total/2; d > heaviest || -d > heaviest {
			t.Fatalf("member %v holds %d of %d", m[15], w, total)
		}
	}
}

func TestRebalance_NeverClaimsALiveOwnersSlice(t *testing.T) {
	a, b, gone := member(1), member(2), member(9)
	slices := []SliceState{slice("b1", 4, b), slice("b2", 4, b), slice("g1", 4, gone), slice("free", 1, uuid.Nil)}
	plan := Rebalance(a, []uuid.UUID{a, b}, slices, true)
	for _, k := range plan.Claim {
		if k.AccountID != "free" {
			t.Fatalf("claimed %v, which is owned (a lease owned by a member that is not live is not free: its expiry decides)", k)
		}
	}
	if got := keyNames(plan.Claim); !equalStrings(got, []string{"free"}) {
		t.Fatalf("claims = %v, want [free]", got)
	}
	// Sorted output is deterministic.
	again := Rebalance(a, []uuid.UUID{b, a}, slices, true)
	n1, n2 := keyNames(plan.Claim), keyNames(again.Claim)
	sort.Strings(n1)
	sort.Strings(n2)
	if !equalStrings(n1, n2) {
		t.Fatalf("plan depends on the order of the live list: %v vs %v", n1, n2)
	}
}
