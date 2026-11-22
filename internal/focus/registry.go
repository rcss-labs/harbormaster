package focus

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Admission is the outcome of one heartbeat.
type Admission string

const (
	Admitted   Admission = "admitted"
	Refreshed  Admission = "refreshed"
	Capped     Admission = "capped"
	UserCapped Admission = "user_capped"
)

// Limits are the registry's caps and the expiry given to a heartbeat.
type Limits struct {
	TTL                    time.Duration
	MaxObjects, MaxPerUser int
}

// Fresh is the evidence the poller leaves after a successful focus read.
type Fresh struct {
	At       time.Time
	Interval time.Duration
	State    string // "live" or "throttled"
}

// Registry is the set of focused objects. One member stands for an object however many users view it.
type Registry interface {
	// Heartbeat registers or refreshes the object for the user, subject to the estate and per-user caps.
	Heartbeat(ctx context.Context, o Object, userID int64, lim Limits) (Admission, error)
	// Release drops the user's claim and shortens the object's expiry to ReleaseGrace; another viewer's next heartbeat
	// restores it. It never raises an expiry.
	Release(ctx context.Context, o Object, userID int64) error
	// List returns the unexpired objects.
	List(ctx context.Context) ([]Object, error)
	SetFresh(ctx context.Context, o Object, f Fresh) error
	Fresh(ctx context.Context, o Object) (Fresh, bool, error)
}

const (
	// ReleaseGrace is how long a released object stays registered, so another viewer's next heartbeat (every 10 s) can
	// keep it without the loop stopping.
	ReleaseGrace = 12 * time.Second
	// freshTTL is how long a fresh entry survives without a new successful read.
	freshTTL = 60 * time.Second
	// userKeyTTL bounds a user's set in the shared store.
	userKeyTTL = 60 * time.Second
)

// NewMemoryRegistry is the registry of a single-task install and the fallback when the shared store is down.
func NewMemoryRegistry(now func() time.Time) Registry {
	return &memoryRegistry{
		now:     now,
		estate:  map[string]time.Time{},
		users:   map[int64]map[string]time.Time{},
		fresh:   map[string]Fresh{},
		freshAt: map[string]time.Time{},
	}
}

type memoryRegistry struct {
	now     func() time.Time
	mu      sync.Mutex
	estate  map[string]time.Time
	users   map[int64]map[string]time.Time
	fresh   map[string]Fresh
	freshAt map[string]time.Time // when each fresh entry expires
}

// expire drops members whose time has passed. Callers hold mu.
func (r *memoryRegistry) expire(now time.Time) {
	for m, exp := range r.estate {
		if !exp.After(now) {
			delete(r.estate, m)
		}
	}
	for u, set := range r.users {
		for m, exp := range set {
			if !exp.After(now) {
				delete(set, m)
			}
		}
		if len(set) == 0 {
			delete(r.users, u)
		}
	}
	for m, exp := range r.freshAt {
		if !exp.After(now) {
			delete(r.fresh, m)
			delete(r.freshAt, m)
		}
	}
}

func (r *memoryRegistry) Heartbeat(_ context.Context, o Object, userID int64, lim Limits) (Admission, error) {
	member := o.Member()
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.expire(now)
	expiry := now.Add(lim.TTL)

	_, inEstate := r.estate[member]
	_, inUser := r.users[userID][member]
	if !inUser && len(r.users[userID]) >= lim.MaxPerUser {
		return UserCapped, nil
	}
	result := Refreshed
	if !inEstate {
		if len(r.estate) >= lim.MaxObjects {
			return Capped, nil
		}
		result = Admitted
	}
	r.estate[member] = expiry
	if r.users[userID] == nil {
		r.users[userID] = map[string]time.Time{}
	}
	r.users[userID][member] = expiry
	return result, nil
}

func (r *memoryRegistry) Release(_ context.Context, o Object, userID int64) error {
	member := o.Member()
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.expire(now)
	delete(r.users[userID], member)
	if exp, ok := r.estate[member]; ok {
		if limit := now.Add(ReleaseGrace); exp.After(limit) {
			r.estate[member] = limit
		}
	}
	return nil
}

func (r *memoryRegistry) List(context.Context) ([]Object, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire(r.now())
	members := make([]string, 0, len(r.estate))
	for m := range r.estate {
		members = append(members, m)
	}
	slices.Sort(members)
	return parseMembers(members), nil
}

func (r *memoryRegistry) SetFresh(_ context.Context, o Object, f Fresh) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	member := o.Member()
	r.fresh[member] = f
	r.freshAt[member] = r.now().Add(freshTTL)
	return nil
}

func (r *memoryRegistry) Fresh(_ context.Context, o Object) (Fresh, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire(r.now())
	f, ok := r.fresh[o.Member()]
	return f, ok, nil
}

// parseMembers turns stored members back into objects, skipping any that do not parse (a corrupt or foreign member
// must never stop the others).
func parseMembers(members []string) []Object {
	out := make([]Object, 0, len(members))
	for _, m := range members {
		if o, err := ParseMember(m); err == nil {
			out = append(out, o)
		}
	}
	return out
}
