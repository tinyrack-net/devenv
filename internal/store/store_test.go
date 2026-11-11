package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tinyrack-net/devenv/internal/model"
)

// Open returns a store for tests: SQLite in a temp dir, or the PostgreSQL
// database in DEVENV_TEST_POSTGRES (wiped first) when kind is "postgres".
func openTest(t *testing.T, kind string) *Store {
	t.Helper()
	ctx := context.Background()
	if kind == "postgres" {
		url := os.Getenv("DEVENV_TEST_POSTGRES")
		if url == "" {
			t.Skip("DEVENV_TEST_POSTGRES not set")
		}
		s, err := Open(ctx, "postgres", url)
		if err != nil {
			t.Fatal(err)
		}
		for _, tbl := range []string{"users", "environments", "images", "tokens", "kv"} {
			if _, err := s.exec(ctx, "DELETE FROM "+tbl); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	s, err := Open(ctx, "sqlite", filepath.Join(t.TempDir(), "db", "devenv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStore(t *testing.T) {
	for _, kind := range []string{"sqlite", "postgres"} {
		t.Run(kind, func(t *testing.T) { testStore(t, kind) })
	}
}

func testStore(t *testing.T, kind string) {
	ctx := context.Background()
	s := openTest(t, kind)
	changes, cancel := s.Subscribe()
	defer cancel()

	// Environments: create, conflict, generation, status.
	e := &model.Environment{Name: "work", Spec: model.EnvironmentSpec{Owner: "alice", ImageRef: "base"}, HostKey: []byte("K")}
	if err := s.CreateEnvironment(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateEnvironment(ctx, &model.Environment{Name: "work"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if c := <-changes; c.Kind != KindEnvironment || c.Name != "work" {
		t.Fatalf("change %+v", c)
	}
	a, _ := s.GetEnvironment(ctx, "work")
	b, _ := s.GetEnvironment(ctx, "work")
	a.Spec.ImageRef = "agent"
	if err := s.UpdateEnvironment(ctx, a); err != nil {
		t.Fatal(err)
	}
	if a.Generation != 2 {
		t.Fatalf("generation %d", a.Generation)
	}
	b.VolumeCreated = true
	if err := s.UpdateEnvironment(ctx, b); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	got, err := s.MutateEnvironment(ctx, "work", func(e *model.Environment) error { e.VolumeCreated = true; return nil })
	if err != nil || got.Generation != 2 {
		t.Fatalf("mutate without spec change: %v gen %d", err, got.Generation)
	}
	if err := s.UpdateEnvironmentStatus(ctx, "work", model.EnvironmentStatus{Phase: model.PhaseRunning, IP: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	// Status writes do not conflict with spec writers.
	_, err = s.MutateEnvironment(ctx, "work", func(e *model.Environment) error { e.Spec.Running = new(false); return nil })
	if err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetEnvironment(ctx, "work")
	if got.Status.IP != "10.0.0.1" || got.IsRunning() || string(got.HostKey) != "K" || !got.VolumeCreated || got.Spec.ImageRef != "agent" || got.CreatedAt.IsZero() {
		t.Fatalf("got %+v", got)
	}
	list, _ := s.ListEnvironments(ctx)
	if len(list) != 1 {
		t.Fatalf("list %d", len(list))
	}
	if err := s.DeleteEnvironment(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEnvironment(ctx, "work"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}

	// Users.
	u := &model.User{Name: "alice", Spec: model.UserSpec{Email: "a@example.com"}}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := s.MutateUser(ctx, "alice", func(u *model.User) error { u.Status.LastLoginAt = &now; return nil }); err != nil {
		t.Fatal(err)
	}
	us, _ := s.ListUsers(ctx)
	if len(us) != 1 || us[0].Status.LastLoginAt == nil || !us[0].Status.LastLoginAt.Equal(now) || us[0].Revision != 2 {
		t.Fatalf("users %+v", us)
	}

	// Images.
	img := &model.Image{Name: "mine", Spec: model.ImageSpec{Image: "x", Owner: "alice"}}
	if err := s.CreateImage(ctx, img); err != nil {
		t.Fatal(err)
	}
	img.Spec.Description = "d"
	if err := s.UpdateImage(ctx, img); err != nil {
		t.Fatal(err)
	}
	imgs, _ := s.ListImages(ctx)
	if len(imgs) != 1 || imgs[0].Spec.Description != "d" {
		t.Fatalf("images %+v", imgs)
	}

	// Tokens.
	for _, id := range []string{"t1", "t2"} {
		if err := s.CreateToken(ctx, &TokenRecord{ID: id, User: "alice", Hash: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.CreateToken(ctx, &TokenRecord{ID: "t3", User: "bob"})
	ts, _ := s.ListTokens(ctx, "alice")
	all, _ := s.ListTokens(ctx, "")
	if len(ts) != 2 || len(all) != 3 {
		t.Fatalf("tokens %d %d", len(ts), len(all))
	}
	_ = s.DeleteUserTokens(ctx, "alice")
	if ts, _ = s.ListTokens(ctx, ""); len(ts) != 1 {
		t.Fatalf("after delete %d", len(ts))
	}

	// Key/value.
	v, err := s.SetValueIfAbsent(ctx, "k", "first")
	v2, _ := s.SetValueIfAbsent(ctx, "k", "second")
	if err != nil || v != "first" || v2 != "first" {
		t.Fatalf("kv %q %q %v", v, v2, err)
	}
}

func TestPoll(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	a, err := Open(ctx, "sqlite", filepath.Join(dir, "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, "sqlite", filepath.Join(dir, "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	ch, unsub := a.Subscribe()
	defer unsub()
	go a.Poll(ctx, 50*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	// A write through another store (another replica) is noticed.
	if err := b.CreateEnvironment(ctx, &model.Environment{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-ch:
		if c.Name != "x" {
			t.Fatalf("change %+v", c)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no change")
	}
}

func TestSchemaVersionMismatch(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "d.db")
	s, err := Open(ctx, "sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Reopening the same version works.
	if s, err = Open(ctx, "sqlite", p); err != nil {
		t.Fatal(err)
	}
	_, _ = s.exec(ctx, `INSERT INTO schema_migrations (version) VALUES (999)`)
	s.Close()
	if _, err := Open(ctx, "sqlite", p); !errors.Is(err, ErrSchemaVersion) {
		t.Fatalf("opened another schema version: %v", err)
	}
}
