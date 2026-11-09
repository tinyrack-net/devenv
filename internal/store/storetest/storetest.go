// Package storetest opens throwaway databases for tests.
package storetest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tinyrack-net/devenv/internal/store"
)

// New returns an empty SQLite store in a temporary directory.
func New(t testing.TB) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), "sqlite", filepath.Join(t.TempDir(), "devenv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
