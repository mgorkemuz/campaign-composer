package store_test

import (
	"context"
	"errors"
	"os"
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
