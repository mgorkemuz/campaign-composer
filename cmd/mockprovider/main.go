// Command mockprovider runs the fake SMS/email provider. Every misbehaviour is
// set with an environment variable; see internal/mockprovider.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"campaign-composer/internal/mockprovider"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := mockprovider.Config{
		ErrorRate:        envFloat("MOCK_ERROR_RATE", 0.05),
		RateLimitRate:    envFloat("MOCK_RATE_LIMIT_RATE", 0.05),
		DropRate:         envFloat("MOCK_DROP_RATE", 0.01),
		SlowRate:         envFloat("MOCK_SLOW_RATE", 0.02),
		MaxRPS:           int(envFloat("MOCK_MAX_RPS", 150)),
		LatencyMin:       envDuration("MOCK_LATENCY_MIN", 50*time.Millisecond),
		LatencyMax:       envDuration("MOCK_LATENCY_MAX", 500*time.Millisecond),
		SlowLatency:      envDuration("MOCK_SLOW_LATENCY", 3*time.Second),
		WebhookURL:       os.Getenv("WEBHOOK_URL"),
		WebhookSecret:    os.Getenv("WEBHOOK_SECRET"),
		DeliveredRate:    envFloat("MOCK_DELIVERED_RATE", 0.95),
		WebhookDelayMin:  envDuration("MOCK_RECEIPT_DELAY_MIN", 500*time.Millisecond),
		WebhookDelayMax:  envDuration("MOCK_RECEIPT_DELAY_MAX", 5*time.Second),
		WebhookDupRate:   envFloat("MOCK_RECEIPT_DUPLICATE_RATE", 0.05),
		WebhookEarlyRate: envFloat("MOCK_RECEIPT_EARLY_RATE", 0.02),
		Seed:             uint64(time.Now().UnixNano()),
	}
	if cfg.WebhookURL != "" && cfg.WebhookSecret == "" {
		log.Error("WEBHOOK_SECRET is required when WEBHOOK_URL is set")
		os.Exit(1)
	}

	p := mockprovider.New(cfg, log)
	srv := &http.Server{Addr: ":" + env("PORT", "9090"), Handler: p.Handler(), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Info("mock provider listening", "addr", srv.Addr, "config", cfg.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdown)
	p.Close()
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return f
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return def
}
