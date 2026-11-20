package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EmbeddedPort is where the embedded PostgreSQL listens when DATABASE_URL is
// empty. It differs from 5432 so it does not clash with a local install.
const EmbeddedPort = 5433

// Open connects to PostgreSQL. With an empty databaseURL it first starts an
// embedded PostgreSQL under dataDir. The returned close function releases the
// pool and stops the embedded server, if any.
func Open(ctx context.Context, databaseURL, dataDir string, log *slog.Logger) (*pgxpool.Pool, func(), error) {
	stopEmbedded := func() {}
	if databaseURL == "" {
		url, stop, err := startEmbedded(dataDir, log)
		if err != nil {
			return nil, nil, err
		}
		databaseURL, stopEmbedded = url, stop
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		stopEmbedded()
		return nil, nil, fmt.Errorf("connect to postgres: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		stopEmbedded()
		return nil, nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, func() { pool.Close(); stopEmbedded() }, nil
}

func startEmbedded(dataDir string, log *slog.Logger) (string, func(), error) {
	base, err := filepath.Abs(filepath.Join(dataDir, "pg"))
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", nil, err
	}
	password, err := embeddedPassword(filepath.Join(base, "password"))
	if err != nil {
		return "", nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(base, "postgres.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return "", nil, err
	}

	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V17).
		Port(EmbeddedPort).
		Username("composer").
		Password(password).
		Database("composer").
		DataPath(filepath.Join(base, "data")).
		RuntimePath(filepath.Join(base, "runtime")).
		BinariesPath(filepath.Join(base, "bin")).
		StartTimeout(2 * time.Minute).
		Logger(logFile))

	log.Info("starting embedded PostgreSQL; the first run downloads it once", "dir", base, "port", EmbeddedPort)
	if err := pg.Start(); err != nil {
		logFile.Close()
		return "", nil, fmt.Errorf("start embedded postgres (log: %s): %w", logFile.Name(), err)
	}
	stop := func() {
		if err := pg.Stop(); err != nil {
			log.Error("stop embedded postgres", "error", err)
		}
		logFile.Close()
	}
	url := fmt.Sprintf("postgres://composer:%s@localhost:%d/composer?sslmode=disable", password, EmbeddedPort)
	return url, stop, nil
}

// embeddedPassword returns the password of the embedded database, creating a
// random one on first use. Keeping it in the data directory means no password
// lives in the code or in git.
func embeddedPassword(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	pw := hex.EncodeToString(raw)
	if err := os.WriteFile(path, []byte(pw), 0o600); err != nil {
		return "", err
	}
	return pw, nil
}
