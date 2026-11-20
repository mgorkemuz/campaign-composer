// Package store owns the PostgreSQL connection, migrations and shared database helpers.
package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrConflict means a unique constraint was violated.
	ErrConflict = errors.New("already exists")
	// ErrInUse means a row is still referenced by another.
	ErrInUse = errors.New("still in use")
)

// NotifyChannel is the single PostgreSQL channel for doorbells. Payloads are JSON
// {"topic": "...", "delta": "..."}; listeners read the real data from tables.
const NotifyChannel = "doorbell"

// DBTX is what both *pgxpool.Pool and pgx.Tx provide, so helpers can run inside
// a caller's transaction or on their own.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// MapErr turns driver errors into the package's sentinel errors so callers can
// use errors.Is without knowing PostgreSQL error codes.
func MapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return ErrConflict
		case "23503": // foreign_key_violation
			return ErrInUse
		case "22P02": // invalid_text_representation, e.g. a malformed UUID
			return ErrNotFound
		}
	}
	return err
}
