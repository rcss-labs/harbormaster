package lease

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
)

// Every give-back must strictly reduce the sum of squares of the members' held weights (counting the slice as taken by
// the lightest other member), which is what rules out oscillation: a slice only ever moves towards evenness.
func TestRebalance_EveryGiveBackStrictlyReducesTheSumOfSquares(t *testing.T) {
	for name, tc := range map[string]struct {
		members int
		weights []int
	}{
		"4 equal over 2":      {2, []int{1, 1, 1, 1}},
		"5 equal over 2":      {2, []int{1, 1, 1, 1, 1}},
		"7 equal over 3":      {3, []int{1, 1, 1, 1, 1, 1, 1}},
		"uneven over 2":       {2, []int{9, 5, 5, 3, 2, 1, 1}},
		"uneven over 4":       {4, []int{50, 30, 20, 20, 10, 10, 5, 5, 3, 3, 2, 1}},
		"heavy first over 3":  {3, []int{8, 1, 1, 1, 1, 1, 1, 1, 1}},
		"two heavy over 3":    {3, []int{6, 6, 1, 1, 1, 1}},
		"single slice over 2": {2, []int{4}},
	} {
		t.Run(name, func(t *testing.T) {
			sim := &simulation{}
			for i := range tc.members {
				sim.members = append(sim.members, member(byte(i+1)))
			}
			for i, w := range tc.weights {
				sim.slices = append(sim.slices, slice(fmt.Sprintf("s%02d", i), w, sim.members[0])) // all start on one member
			}
			for round := 1; round <= 200; round++ {
				moved := false
				for _, m := range sim.members {
					plan := Rebalance(m, sim.members, sim.slices, true)
					if plan.GiveBack != nil {
						held := sim.held()
						w := sim.slices[sim.index(*plan.GiveBack)].Weight
						lightest := -1
						for _, other := range sim.members {
							if other != m && (lightest < 0 || held[other] < lightest) {
								lightest = held[other]
							}
						}
						before := held[m]*held[m] + lightest*lightest
						after := (held[m]-w)*(held[m]-w) + (lightest+w)*(lightest+w)
						if after >= before {
							t.Fatalf("round %d: giving back weight %d from %d to a member at %d does not reduce the sum of squares (%d -> %d)",
								round, w, held[m], lightest, before, after)
						}
						s := sim.slices[sim.index(*plan.GiveBack)]
						s.Owner = uuid.Nil
						sim.slices[sim.index(*plan.GiveBack)] = s
						moved = true
						continue
					}
					for _, k := range plan.Claim {
						i := sim.index(k)
						if sim.slices[i].Owner == uuid.Nil {
							sim.slices[i].Owner = m
							moved = true
						}
					}
				}
				if !moved {
					return
				}
			}
			t.Fatalf("still moving after 200 rounds: held %v", sim.held())
		})
	}
}

// Four equal slices over two pollers settle at two each: the member holding three gives one back, because what it keeps
// still reaches its fair share.
func TestRebalance_FourSlicesOverTwoPollersSettleTwoAndTwo(t *testing.T) {
	sim := &simulation{members: []uuid.UUID{member(1), member(2)}}
	for i := range 4 {
		sim.slices = append(sim.slices, slice(fmt.Sprintf("s%d", i), 1, member(1)))
	}
	sim.settle(t, 50)
	for m, w := range sim.held() {
		if w != 2 {
			t.Fatalf("member %v holds %d, want 2 each: %v", m[15], w, sim.held())
		}
	}
}
