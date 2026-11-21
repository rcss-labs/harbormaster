package main

import (
	"context"
	"log/slog"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// kvOptions parses HM_KV_URL into client options, or reports that no store is configured. Errors never contain
// the URL or its password.
func kvOptions(cfg config.KVConfig) (opts kv.Options, configured bool, err error) {
	if cfg.URL == "" {
		return kv.Options{}, false, nil
	}
	opts, err = kv.ParseURL(cfg.URL)
	if err != nil {
		return kv.Options{}, false, kv.Scrub(err, cfg.URL)
	}
	opts.Prefix = cfg.Prefix
	return opts, true, nil
}

// startKV opens the store and starts its health loop. An unreachable server does not fail start: the client
// reports down and recovers by itself. The returned stop function ends the loop and closes the client.
func startKV(ctx context.Context, cfg config.KVConfig, opts kv.Options, log *slog.Logger) (*kv.Health, func(), error) {
	client, err := kv.Open(opts)
	if err != nil {
		return nil, nil, kv.Scrub(err, cfg.URL)
	}
	health := kv.NewHealth(client, telemetry.Default, log)
	healthCtx, stopHealth := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		health.Run(healthCtx)
	}()
	log.Info("kv: configured", "prefix", cfg.Prefix)
	return health, func() {
		stopHealth()
		<-done
		client.Close()
		telemetry.Default.ClearKV()
	}, nil
}
