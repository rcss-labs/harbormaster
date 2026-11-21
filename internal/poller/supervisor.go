package poller

import (
	"context"
	"sync"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// targets lists the enabled account x region pairs: the slices that exist.
func (p *Poller) targets(ctx context.Context) ([]awsx.Target, error) {
	accounts, err := p.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	var out []awsx.Target
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		for _, region := range a.Regions {
			out = append(out, awsx.Target{AccountID: a.AccountID, Region: region})
		}
	}
	return out, nil
}

// holdSlice runs the polling loop of one slice while its lease is held. Every write goes through a writer fenced by
// the lease's epoch; the first write rejected with ErrLeaseLost stops this loop (and only this one).
func (p *Poller) holdSlice(ctx context.Context, l store.Lease) {
	t := awsx.Target{AccountID: l.Key.AccountID, Region: l.Key.Region}
	w, err := p.store.SliceWriter(l)
	if err != nil {
		p.log.Error("poller: slice writer", "account", t.AccountID, "region", t.Region, "err", err)
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	w.OnRejected(func() {
		p.cfg.Telemetry.CountFencedWriteRejected()
		once.Do(func() {
			p.log.Warn("poller: lease lost, stopping slice", "account", t.AccountID, "region", t.Region, "epoch", l.Epoch)
		})
		cancel()
	})
	p.loopFactory(loopCtx, t, w)
}
