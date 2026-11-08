package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

const (
	errChecksumMismatch = "checksum mismatch"
	dbImportTimeout     = 5 * time.Minute
)

// dbStale reports whether the job's DB is older than the job allows.
func dbStale(db scan.JobDB, now time.Time) bool {
	return now.Sub(db.BuiltAt) > time.Duration(db.MaxAgeHours)*time.Hour
}

// prepareDB downloads the job's archive, verifies its sha256 against the job
// and imports it into the Grype cache. The returned message is the manifest
// error; it is empty on success. latest.json is never read.
func (r *Runner) prepareDB(ctx context.Context, db scan.JobDB) (failure string) {
	if !strings.HasPrefix(db.ArchiveKey, "grype-db/") {
		return "db archive key is not under grype-db/"
	}
	archivePath := filepath.Join(r.Opts.WorkDir, "grype-db.tar.zst")
	got, err := r.downloadArchive(ctx, db.ArchiveKey, archivePath)
	if err != nil {
		r.Log.Error("download db archive", "err", err)
		return "db archive unavailable"
	}
	defer os.Remove(archivePath)
	if got != db.Checksum {
		return errChecksumMismatch
	}
	importCtx, cancel := context.WithTimeout(ctx, dbImportTimeout)
	defer cancel()
	res, err := r.Cmd.Run(importCtx, r.Opts.GrypePath, []string{"db", "import", archivePath}, r.grypeEnv(db.MaxAgeHours), 1<<20)
	if err != nil || res.ExitCode != 0 {
		r.Log.Error("grype db import", "err", err, "exit", res.ExitCode, "stderr", res.StderrTail)
		return "db import failed"
	}
	return ""
}

// downloadArchive streams the object to path and returns "sha256:<hex>" of what it wrote.
func (r *Runner) downloadArchive(ctx context.Context, key, path string) (string, error) {
	out, err := r.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.Opts.Bucket), Key: aws.String(key)})
	if err != nil {
		return "", err
	}
	defer out.Body.Close()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(out.Body, scan.MaxDBArchiveBytes+1))
	if err != nil {
		return "", err
	}
	if written > scan.MaxDBArchiveBytes {
		return "", errors.New("db archive is larger than the limit")
	}
	return fmt.Sprintf("sha256:%s", hex.EncodeToString(hash.Sum(nil))), nil
}

// grypeEnv is the exact environment of every Grype call: no DB updates, and
// a DB older than the job's limit is refused by Grype itself.
func (r *Runner) grypeEnv(maxAgeHours int) []string {
	return []string{
		"HOME=" + r.Opts.WorkDir,
		"TMPDIR=" + r.tmpDir(),
		"GRYPE_CHECK_FOR_APP_UPDATE=false",
		"GRYPE_DB_AUTO_UPDATE=false",
		"GRYPE_DB_REQUIRE_UPDATE_CHECK=false",
		"GRYPE_DB_VALIDATE_AGE=true",
		fmt.Sprintf("GRYPE_DB_MAX_ALLOWED_BUILT_AGE=%dh", maxAgeHours),
		"GRYPE_DB_CACHE_DIR=" + filepath.Join(r.Opts.WorkDir, "cache", "grype"),
	}
}
