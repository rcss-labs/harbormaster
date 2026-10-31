package execbridge

import "sync"

// Limiter caps the number of concurrent terminal sessions per user.
type Limiter struct {
	max int

	mu     sync.Mutex
	counts map[int64]int
}

func NewLimiter(max int) *Limiter {
	return &Limiter{max: max, counts: map[int64]int{}}
}

// Acquire takes a slot for userID. The returned release is idempotent.
func (l *Limiter) Acquire(userID int64) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.counts[userID] >= l.max {
		return func() {}, false
	}
	l.counts[userID]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if l.counts[userID]--; l.counts[userID] <= 0 {
				delete(l.counts, userID)
			}
		})
	}, true
}

// Count reports the slots currently held by userID.
func (l *Limiter) Count(userID int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.counts[userID]
}
