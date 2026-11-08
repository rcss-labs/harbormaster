// Command harbormaster-scanner is the scan task: it reads a job from the scan
// bucket, scans the listed ECR images with Syft and Grype and writes results.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/rcss-labs/harbormaster/internal/scanner"
)

// Overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

const usageText = `usage: harbormaster-scanner <command>

commands:
  run          scan the images of the job named by HM_SCAN_RUN_ID
  healthcheck  check that this binary, syft and grype are executable
  version      print the version

environment for run:
  HM_SCAN_RUN_ID, HM_SCAN_MODE, HM_SCAN_BUCKET, HM_SCAN_IMAGE_DIGEST
  HM_SCAN_WORK_DIR (default /work), HM_SCAN_SYFT, HM_SCAN_GRYPE
`

const (
	defaultWorkDir = "/work"
	defaultSyft    = "/usr/local/bin/syft"
	defaultGrype   = "/usr/local/bin/grype"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)) }

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, "harbormaster-scanner", version)
		return 0
	case "healthcheck":
		return healthcheck(stderr, getenv)
	case "run":
		return runScan(stderr, getenv)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	}
	fmt.Fprint(stderr, usageText)
	return 2
}

func optionsFromEnv(getenv func(string) string) (scanner.Options, error) {
	runID, err := strconv.ParseInt(getenv("HM_SCAN_RUN_ID"), 10, 64)
	if err != nil || runID <= 0 {
		return scanner.Options{}, errors.New("HM_SCAN_RUN_ID must be a positive number")
	}
	bucket := getenv("HM_SCAN_BUCKET")
	if bucket == "" {
		return scanner.Options{}, errors.New("HM_SCAN_BUCKET is required")
	}
	return scanner.Options{
		Bucket: bucket, RunID: runID, Mode: getenv("HM_SCAN_MODE"), Now: time.Now, Version: version,
		WorkDir:            envOr(getenv, "HM_SCAN_WORK_DIR", defaultWorkDir),
		SyftPath:           envOr(getenv, "HM_SCAN_SYFT", defaultSyft),
		GrypePath:          envOr(getenv, "HM_SCAN_GRYPE", defaultGrype),
		ScannerImageDigest: getenv("HM_SCAN_IMAGE_DIGEST"),
	}, nil
}

func envOr(getenv func(string) string, name, fallback string) string {
	if value := getenv(name); value != "" {
		return value
	}
	return fallback
}

func runScan(stderr io.Writer, getenv func(string) string) int {
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	opts, err := optionsFromEnv(getenv)
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Error("load AWS configuration", "err", err)
		return 1
	}
	runner := &scanner.Runner{
		Opts: opts, S3: s3.NewFromConfig(cfg), Cmd: scanner.OSCommand{},
		NewECR: scanner.NewAssumedECRClient(cfg), Log: logger,
	}
	return runner.Run(ctx)
}

// healthcheck verifies that this binary, syft and grype exist and are executable.
func healthcheck(stderr io.Writer, getenv func(string) string) int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "cannot find own executable:", err)
		return 1
	}
	paths := []string{self, envOr(getenv, "HM_SCAN_SYFT", defaultSyft), envOr(getenv, "HM_SCAN_GRYPE", defaultGrype)}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			fmt.Fprintf(stderr, "%s is missing or not executable\n", path)
			return 1
		}
	}
	return 0
}
