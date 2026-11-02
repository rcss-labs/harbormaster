package poller

import (
	"context"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

type runningLoop struct{ cancel context.CancelFunc }

// runSupervisor keeps one polling loop per enabled account x region. It refreshes the account
// list at start and every cfg.AccountsRefresh, and runs retention every cfg.RetentionEvery
// (the first run happens one interval after start, not immediately); each retention tick first runs the daily rollups. It returns after ctx is
// cancelled and every loop has exited.
func (p *Poller) runSupervisor(ctx context.Context) {
	loops := map[awsx.Target]runningLoop{}
	var wg sync.WaitGroup
	defer func() {
		for _, l := range loops {
			l.cancel()
		}
		wg.Wait()
	}()

	refresh := func() {
		accounts, err := p.store.ListAccounts(ctx)
		if err != nil {
			if ctx.Err() == nil {
				p.log.Error("poller: list accounts", "err", err)
			}
			return // keep the current loops rather than tearing them down on a transient error
		}
		wanted := map[awsx.Target]bool{}
		for _, a := range accounts {
			if !a.Enabled {
				continue
			}
			for _, region := range a.Regions {
				wanted[awsx.Target{AccountID: a.AccountID, Region: region}] = true
			}
		}
		for t, l := range loops {
			if !wanted[t] {
				l.cancel()
				delete(loops, t)
			}
		}
		for t := range wanted {
			if _, ok := loops[t]; ok {
				continue
			}
			loopCtx, cancel := context.WithCancel(ctx)
			loops[t] = runningLoop{cancel: cancel}
			wg.Add(1)
			go func() {
				defer wg.Done()
				p.loopFactory(loopCtx, t)
			}()
		}
	}

	refreshTicker := time.NewTicker(p.cfg.AccountsRefresh)
	defer refreshTicker.Stop()
	retentionTicker := time.NewTicker(p.cfg.RetentionEvery)
	defer retentionTicker.Stop()

	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-refreshTicker.C:
			refresh()
		case <-retentionTicker.C:
			p.runRollups(ctx)
			p.runRetention(ctx)
		}
	}
}

// runRollups computes the daily analytics rollups that are due. It must run before runRetention in the same
// tick so the raw rows it reads are still there. Errors are logged, never fatal: retention goes ahead regardless.
func (p *Poller) runRollups(ctx context.Context) {
	result, err := p.rollups.RunDue(ctx)
	if err != nil && ctx.Err() == nil {
		p.log.Error("poller: rollups", "err", err)
	}
	p.log.Debug("poller: rollups done", "rows", result)
}

// runRetention marks orphaned tags, then deletes expired rows. Errors are logged, never fatal.
func (p *Poller) runRetention(ctx context.Context) {
	now := time.Now()
	if err := p.store.MarkOrphanedTags(ctx, now); err != nil && ctx.Err() == nil {
		p.log.Error("poller: mark orphaned tags", "err", err)
	}
	result, err := p.store.RunRetention(ctx, now, p.cfg.Retention)
	if err != nil {
		if ctx.Err() == nil {
			p.log.Error("poller: retention", "err", err)
		}
		return
	}
	p.log.Debug("poller: retention done", "deleted", result)
}
