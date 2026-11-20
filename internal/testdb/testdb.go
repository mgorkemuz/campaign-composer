// Package testdb gives each test its own freshly migrated PostgreSQL database.
//
// It uses TEST_DATABASE_URL when set (any database on a server where the user
// may create databases). Otherwise it starts an embedded PostgreSQL once per
// test binary, so `go test ./...` works without installing anything.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"campaign-composer/internal/store"
)

var (
	mu        sync.Mutex
	serverURL string
	stopPG    func()
)

// Main runs the tests and stops the embedded server afterwards, if one was
// started. Use it from TestMain: os.Exit(testdb.Main(m)).
func Main(m *testing.M) int {
	code := m.Run()
	mu.Lock()
	defer mu.Unlock()
	if stopPG != nil {
		stopPG()
	}
	return code
}

// New creates an empty, migrated database for this test and drops it when the
// test ends.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	base, err := server()
	if err != nil {
		t.Fatalf("test database server: %v", err)
	}

	name := "test_" + randomHex(6)
	if err := adminExec(ctx, base, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}
	pool, err := pgxpool.New(ctx, withDatabase(base, name))
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		adminExec(ctx, base, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	return pool
}

func server() (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if serverURL != "" {
		return serverURL, nil
	}
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		serverURL = u
		return u, nil
	}

	port, err := freePort()
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "composer-testpg-")
	if err != nil {
		return "", err
	}
	binDir, unlock, err := sharedBinaries()
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	password := randomHex(12)
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V17).
		Port(uint32(port)).
		Username("postgres").
		Password(password).
		Database("postgres").
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		BinariesPath(binDir).
		Logger(io.Discard))
	err = pg.Start()
	unlock()
	if err != nil {
		os.RemoveAll(dir)
		return "", fmt.Errorf("start embedded postgres: %w", err)
	}
	stopPG = func() {
		pg.Stop()
		os.RemoveAll(dir)
	}
	serverURL = fmt.Sprintf("postgres://postgres:%s@localhost:%d/postgres?sslmode=disable", password, port)
	return serverURL, nil
}

// sharedBinaries returns a per-user folder for the PostgreSQL binaries, locked
// until unlock is called. Unpacking them is slow (a minute under -race), and the
// library skips it when the folder already holds them, so they are unpacked once
// per machine. The lock keeps test binaries of different packages, which go test
// runs in parallel, from unpacking into the same folder at once.
func sharedBinaries() (dir string, unlock func(), err error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	dir = filepath.Join(cache, "campaign-composer", "postgres-17")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return "", nil, err
	}
	return dir, func() {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
	}, nil
}

func adminExec(ctx context.Context, base, sql string) error {
	conn, err := pgx.Connect(ctx, base)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

func withDatabase(base, name string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.Path = "/" + name
	return u.String()
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
