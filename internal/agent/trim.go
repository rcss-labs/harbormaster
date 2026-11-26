package agent

import "github.com/rcss-labs/harbormaster/internal/agent/provider"

// trimTurns drops the oldest whole turns until size(msgs) is at most limit. A
// turn is a user message and everything after it up to the next user message,
// so a tool call is never separated from its result. The latest turn, which
// holds the latest user message, is always kept, whatever its size.
func trimTurns(msgs []provider.Message, size func([]provider.Message) int64, limit int64) ([]provider.Message, bool) {
	var starts []int
	for i, m := range msgs {
		if m.Role == provider.RoleUser {
			starts = append(starts, i)
		}
	}
	// Anything before the first user message is a turn of its own, dropped first.
	if len(starts) == 0 || starts[0] != 0 {
		starts = append([]int{0}, starts...)
	}
	trimmed := false
	for len(starts) > 1 && size(msgs) > limit {
		cut := starts[1]
		msgs = msgs[cut:]
		starts = starts[1:]
		for i := range starts {
			starts[i] -= cut
		}
		trimmed = true
	}
	return msgs, trimmed
}
