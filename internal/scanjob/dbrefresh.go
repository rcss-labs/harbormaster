package scanjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultRefreshTimeout = 15 * time.Minute
	// minRefreshGap is the least time between two refresh attempts, so a failing upstream is not hammered.
	minRefreshGap = time.Hour
	// refreshingGrace is how long a recorded refresh counts as in progress; a leader lost mid-refresh leaves it set.
	refreshingGrace = 15 * time.Minute
	// staleRetryDelay is how long images wait when the DB is missing or too old.
	staleRetryDelay = 10 * time.Minute
	staleMarker     = "stale"
)

var errArchiveTooLarge = errors.New("vulnerability archive is larger than the allowed size")

// RefreshInProgress reports whether a DB refresh is running. A refresh marker older than 15 minutes is ignored: it is
// what a leader lost mid-refresh leaves behind.
func RefreshInProgress(state store.ScanDBState, now time.Time) bool {
	return state.RefreshingSince != nil && now.Sub(*state.RefreshingSince) < refreshingGrace
}

// capReader fails once more than the allowed number of bytes has been read.
type capReader struct {
	src  io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.src.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, errArchiveTooLarge
	}
	return n, err
}

// refreshDB copies the current upstream DB into the bucket: the archive first, streamed and hashed on the way and
// aborted unless it matches the listing's checksum, then latest.json. It never decompresses or parses the archive.
func (r *Runner) refreshDB(ctx context.Context) error {
	timeout := r.refreshTimeout
	if timeout <= 0 {
		timeout = defaultRefreshTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	listing, err := r.DB.FetchLatest(ctx)
	if err != nil {
		return fmt.Errorf("fetch the upstream listing: %w", err)
	}
	upstream, err := scan.ParseUpstreamDB(listing, r.now())
	if err != nil {
		return err
	}
	archive, err := r.DB.OpenArchive(ctx, upstream.Path)
	if err != nil {
		return fmt.Errorf("open the upstream archive: %w", err)
	}
	defer archive.Close()

	limit := r.archiveCap
	if limit <= 0 {
		limit = scan.MaxDBArchiveBytes
	}
	hasher := sha256.New()
	stream := io.TeeReader(&capReader{src: archive, left: limit}, hasher)
	key := "grype-db/v6/" + upstream.Built.UTC().Format("20060102T150405Z") + ".tar.zst"
	err = r.Bucket.PutStream(ctx, key, stream, func() error {
		if got := "sha256:" + hex.EncodeToString(hasher.Sum(nil)); got != upstream.Checksum {
			return errors.New("vulnerability archive does not match its published checksum")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store the archive: %w", err)
	}
	latest, err := json.Marshal(scan.DBLatest{BuiltAt: upstream.Built, Checksum: upstream.Checksum, ArchiveKey: key})
	if err != nil {
		return err
	}
	if err := r.Bucket.Put(ctx, scan.DBLatestKey, latest); err != nil {
		return fmt.Errorf("store latest.json: %w", err)
	}
	return nil
}

// saveDBState replaces the single DB state row.
func (r *Runner) saveDBState(ctx context.Context, state store.ScanDBState) error {
	return r.Store.SetScanDBState(ctx, state)
}

// syncLatest reads latest.json from the bucket into state. Only "not found" means no DB yet; any other failure is
// returned as text for the caller to record. It saves state when the DB changed.
func (r *Runner) syncLatest(ctx context.Context, state *store.ScanDBState) string {
	data, err := r.Bucket.Get(ctx, scan.DBLatestKey, scan.MaxDBLatestBytes)
	if errors.Is(err, awsx.ErrObjectNotFound) {
		return ""
	}
	if err != nil {
		return scan.SanitizeText("could not read the vulnerability data listing: " + err.Error())
	}
	now := r.now()
	latest, err := scan.ParseDBLatest(data, now)
	if err != nil {
		return scan.SanitizeText("the vulnerability data listing is invalid: " + err.Error())
	}
	if state.BuiltAt != nil && state.BuiltAt.Equal(latest.BuiltAt) && state.Checksum == latest.Checksum && state.ArchiveKey == latest.ArchiveKey {
		return ""
	}
	builtAt := latest.BuiltAt
	state.BuiltAt, state.Checksum, state.ArchiveKey, state.ImportedAt = &builtAt, latest.Checksum, latest.ArchiveKey, &now
	if state.LastRefreshError == staleMarker {
		state.LastRefreshError = ""
	}
	if err := r.saveDBState(ctx, *state); err != nil {
		return scan.SanitizeText(err.Error())
	}
	return ""
}

func (r *Runner) needsRefresh(state store.ScanDBState, now time.Time) bool {
	if r.Cfg.DBRefresh <= 0 || RefreshInProgress(state, now) {
		return false
	}
	if state.LastRefreshAt != nil && now.Sub(*state.LastRefreshAt) < minRefreshGap {
		return false
	}
	return state.BuiltAt == nil || now.Sub(*state.BuiltAt) > r.Cfg.DBRefresh || state.LastRefreshError == staleMarker
}

// ensureDB brings the DB state up to date, refreshing the DB first when a scan needs a newer one. The text it returns is
// the last refresh problem ("" when there is none), already sanitised.
func (r *Runner) ensureDB(ctx context.Context) (store.ScanDBState, string, error) {
	state, err := r.Store.ScanDBState(ctx)
	if err != nil {
		return state, "", err
	}
	problem := r.syncLatest(ctx, &state)
	now := r.now()
	if r.needsRefresh(state, now) {
		state.LastRefreshAt, state.RefreshingSince = &now, &now
		if err := r.saveDBState(ctx, state); err != nil {
			return state, "", err
		}
		refreshErr := r.refreshDB(ctx)
		state.RefreshingSince = nil
		state.LastRefreshError = ""
		if refreshErr != nil {
			state.LastRefreshError = scan.SanitizeText(refreshErr.Error())
			r.log().Warn("scan: vulnerability data refresh failed", "err", refreshErr)
		}
		if err := r.saveDBState(context.WithoutCancel(ctx), state); err != nil {
			return state, "", err
		}
		problem = r.syncLatest(ctx, &state)
		if refreshErr != nil {
			problem = state.LastRefreshError
		}
	}
	if problem != "" && problem != state.LastRefreshError {
		state.LastRefreshError = problem
		if err := r.saveDBState(ctx, state); err != nil {
			return state, problem, err
		}
	}
	return state, problem, nil
}
