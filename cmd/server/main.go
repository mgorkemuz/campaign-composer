// Command server runs the Campaign Composer API, the event hub and (later) the
// delivery workers in one process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"campaign-composer/internal/api"
	"campaign-composer/internal/backoff"
	"campaign-composer/internal/campaign"
	"campaign-composer/internal/composer"
	"campaign-composer/internal/config"
	"campaign-composer/internal/delivery"
	"campaign-composer/internal/events"
	"campaign-composer/internal/llm"
	"campaign-composer/internal/metrics"
	"campaign-composer/internal/queue"
	"campaign-composer/internal/seed"
	"campaign-composer/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogJSON)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, closeDB, err := store.Open(ctx, cfg.DatabaseURL, cfg.DataDir, log)
	if err != nil {
		return err
	}
	defer closeDB()
	if err := store.Migrate(ctx, pool); err != nil {
		return err
	}
	if n, err := seed.Users(ctx, pool, seed.Options{N: cfg.SeedUsers, Seed: 42, Now: time.Now()}); err != nil {
		return fmt.Errorf("seed users: %w", err)
	} else if n > 0 {
		log.Info("seeded mock users", "count", n)
	}

	// Background work stops when bgCtx is cancelled, after HTTP has shut down.
	bgCtx, stopBg := context.WithCancel(context.Background())
	defer stopBg()
	hub := events.NewHub(pool, log)
	go hub.Run(bgCtx)

	if cfg.WebhookSecret == config.DevWebhookSecret {
		log.Warn("WEBHOOK_SECRET is not set; using the development secret")
	}
	m := metrics.New(pool)
	provider := newLLM(cfg)
	comp := composer.New(provider, log)
	comp.OnCall = func(task, outcome string, u llm.Usage) { m.LLMCall(task, outcome, u.InputTokens, u.OutputTokens) }
	campaigns := campaign.NewService(pool, comp, time.Now)
	sender := delivery.NewSender(pool, delivery.NewClient(cfg.ProviderURL), cfg.SendRate, log, m)

	var workers sync.WaitGroup
	contentWorker := &queue.Worker{
		Pool: pool, Hub: hub, Log: log, Kind: campaign.JobGenerateContent,
		Handler: campaigns.HandleContentJob, OnFailed: campaigns.OnContentJobFailed,
		Concurrency: 4,
		// A generation streams for several seconds and may run a repair round.
		LeaseTTL: 3 * time.Minute,
	}
	workers.Add(1)
	go func() { defer workers.Done(); contentWorker.Run(bgCtx) }()
	translateWorker := &queue.Worker{
		Pool: pool, Hub: hub, Log: log, Kind: campaign.JobTranslate,
		Handler: campaigns.HandleTranslateJob, OnFailed: campaigns.OnTranslateJobFailed,
		Concurrency: 4, // bounds parallel LLM calls (rate limits) however many languages
		LeaseTTL:    3 * time.Minute,
	}
	workers.Add(1)
	go func() { defer workers.Done(); translateWorker.Run(bgCtx) }()

	// One worker pool per channel. Each send waits for its channel's rate
	// limiter, so a pool never exceeds SendRate however many workers it has.
	for _, ch := range delivery.Channels {
		w := &queue.Worker{
			Pool: pool, Hub: hub, Log: log, Kind: delivery.JobKind(ch),
			Handler: sender.Handle, OnFailed: sender.OnFailed,
			Concurrency: cfg.SendWorkers,
			LeaseTTL:    30 * time.Second,
			Backoff:     backoff.Policy{Base: time.Second, Max: time.Minute},
		}
		workers.Add(1)
		go func() { defer workers.Done(); w.Run(bgCtx) }()
	}

	apiSrv := api.New(pool, log, api.Options{
		Hub: hub, Campaigns: campaigns, WebhookSecret: cfg.WebhookSecret, Metrics: m, Ring: sender.Ring,
		WebDir: cfg.WebDir,
	})
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "port", cfg.Port, "llm", provider.Name(), "provider", cfg.ProviderURL,
			"send_rate_per_channel", cfg.SendRate)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errc:
		return err
	}
	log.Info("shutting down")
	apiSrv.CloseStreams()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "error", err)
	}
	stopBg()
	workers.Wait() // jobs in progress finish (each bounded by its lease)
	return nil
}

// newLLM uses Claude when an API key is set, and the keyword mock otherwise, so
// the app also works offline.
func newLLM(cfg config.Config) llm.Provider {
	if cfg.AnthropicAPIKey == "" {
		return &composer.Mock{ChunkDelay: 15 * time.Millisecond}
	}
	return llm.NewAnthropic(llm.AnthropicOptions{
		APIKey: cfg.AnthropicAPIKey, Model: cfg.AnthropicModel, WorkspaceID: cfg.AnthropicWorkspaceID,
	})
}

func newLogger(json bool) *slog.Logger {
	if json {
		return slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, nil))
}
