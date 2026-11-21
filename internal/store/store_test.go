package store_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"campaign-composer/internal/store"
	"campaign-composer/internal/testdb"
)

func TestMain(m *testing.M) { os.Exit(testdb.Main(m)) }

func TestMigrateIsIdempotent(t *testing.T) {
	pool := testdb.New(t) // already migrated once
	ctx := context.Background()
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no migrations recorded")
	}
}

func TestMapErr(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	insert := `INSERT INTO jobs (kind, dedup_key) VALUES ('k', 'same')`
	if _, err := pool.Exec(ctx, insert); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Exec(ctx, insert)
	if !errors.Is(store.MapErr(err), store.ErrConflict) {
		t.Fatalf("duplicate key: got %v, want ErrConflict", err)
	}
	err = pool.QueryRow(ctx, "SELECT id FROM jobs WHERE kind = 'none'").Scan(new(int64))
	if !errors.Is(store.MapErr(err), store.ErrNotFound) {
		t.Fatalf("no rows: got %v, want ErrNotFound", err)
	}
}

func TestDatabaseIsUTF8(t *testing.T) {
	pool := testdb.New(t)
	var n int
	// "ş" is two bytes but one character; SQL_ASCII would say 10.
	if err := pool.QueryRow(context.Background(), `SELECT char_length('Eskişehir')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 9 {
		t.Fatalf("char_length('Eskişehir') = %d; the test database is not UTF-8", n)
	}
}

func TestOpenRefusesNonUTF8Database(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	name := "ascii_" + strings.ReplaceAll(t.Name(), "/", "_")
	name = strings.ToLower(name)
	if _, err := pool.Exec(ctx, "CREATE DATABASE "+name+" ENCODING 'SQL_ASCII' LC_COLLATE 'C' LC_CTYPE 'C' TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })

	cfg := pool.Config().ConnConfig
	url := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Password, cfg.Host, cfg.Port, name)
	_, closeDB, err := store.Open(ctx, url, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil {
		closeDB()
		t.Fatal("opened a SQL_ASCII database")
	}
	if !strings.Contains(err.Error(), "UTF8 is required") {
		t.Fatalf("err = %v", err)
	}
}
