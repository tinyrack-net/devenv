// Package store keeps devenv's state in a SQL database (SQLite or
// PostgreSQL): users, environments, console created images, access tokens and
// server keys. It is the source of truth; Kubernetes objects are derived from
// it by the controller, home volumes are adopted.
//
// Rows hold JSON documents plus the columns needed for lookups. Writes use
// optimistic concurrency on a revision column. Environment status is a
// separate column written by the controller only.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // postgres driver "pgx"
	_ "modernc.org/sqlite"             // sqlite driver "sqlite"

	"github.com/tinyrack-net/devenv/internal/model"
)

var (
	ErrNotFound = errors.New("not found")
	ErrExists   = errors.New("already exists")
	ErrConflict = errors.New("modified concurrently, retry")
)

// Kind of a changed record.
type Kind string

const (
	KindEnvironment Kind = "environment"
	KindUser        Kind = "user"
	KindImage       Kind = "image"
)

// Change notifies about a written or deleted record.
type Change struct {
	Kind Kind
	Name string
}

// Store is a devenv database.
type Store struct {
	db       *sql.DB
	postgres bool

	mu   sync.Mutex
	subs map[chan Change]struct{}
}

// Open connects to a database and applies migrations. driver is "sqlite"
// (dsn is a file path) or "postgres" (dsn is a URL).
func Open(ctx context.Context, driver, dsn string) (*Store, error) {
	s := &Store{subs: map[chan Change]struct{}{}}
	var err error
	switch driver {
	case "sqlite":
		if dir := filepath.Dir(dsn); dir != "" && dsn != ":memory:" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, err
			}
		}
		s.db, err = sql.Open("sqlite", "file:"+dsn+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_txlock=immediate")
		if err != nil {
			return nil, err
		}
		// One writer; keeps transactions simple and avoids SQLITE_BUSY.
		s.db.SetMaxOpenConns(1)
	case "postgres":
		s.postgres = true
		s.db, err = sql.Open("pgx", dsn)
		if err != nil {
			return nil, err
		}
		s.db.SetMaxOpenConns(8)
		s.db.SetConnMaxIdleTime(5 * time.Minute)
	default:
		return nil, fmt.Errorf("unknown database driver %q", driver)
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.db.PingContext(pctx); err != nil {
		s.db.Close()
		return nil, fmt.Errorf("connect to %s database: %w", driver, err)
	}
	if err := s.migrate(ctx); err != nil {
		s.db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping checks the connection (readiness).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// q rewrites ? placeholders for PostgreSQL.
func (s *Store) q(query string) string {
	if !s.postgres {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.q(query), args...)
}

func (s *Store) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.q(query), args...)
}

// isUnique reports a primary key violation.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "UNIQUE constraint failed") || strings.Contains(m, "SQLSTATE 23505") || strings.Contains(m, "duplicate key")
}

// Subscribe returns a channel of changes made through this store. Changes
// from other processes are found by Poll. Call cancel to unsubscribe.
func (s *Store) Subscribe() (<-chan Change, func()) {
	ch := make(chan Change, 256)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.mu.Unlock()
	}
}

func (s *Store) notify(kind Kind, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- Change{Kind: kind, Name: name}:
		default: // slow subscriber; the poller catches up
		}
	}
}

// --- migrations ---

var migrations = []string{
	// 1: initial schema
	`CREATE TABLE users (
		name TEXT PRIMARY KEY,
		data TEXT NOT NULL,
		revision BIGINT NOT NULL
	);
	CREATE TABLE environments (
		name TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		data TEXT NOT NULL,
		status TEXT NOT NULL,
		revision BIGINT NOT NULL,
		status_revision BIGINT NOT NULL
	);
	CREATE TABLE images (
		name TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		data TEXT NOT NULL,
		revision BIGINT NOT NULL
	);
	CREATE TABLE tokens (
		id TEXT PRIMARY KEY,
		user_name TEXT NOT NULL,
		data TEXT NOT NULL
	);
	CREATE INDEX tokens_user ON tokens (user_name);
	CREATE TABLE kv (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,
}

// SchemaVersion is the schema version of this binary.
var SchemaVersion = len(migrations)

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version BIGINT PRIMARY KEY)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	if s.postgres {
		// Serialize migrations of concurrently starting replicas.
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return err
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(7419223)`); err != nil {
			return err
		}
		defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(7419223)`)
	}
	var current int
	if err := s.queryRow(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	if current > SchemaVersion {
		return fmt.Errorf("database schema version %d is newer than this server (%d); upgrade devenv", current, SchemaVersion)
	}
	for v := current + 1; v <= SchemaVersion; v++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, stmt := range strings.Split(migrations[v-1], ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w", v, err)
			}
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO schema_migrations (version) VALUES (?)`), v); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: %w", v, err)
		}
	}
	return nil
}

// --- key/value ---

// GetValue returns a value of the kv table, or ErrNotFound.
func (s *Store) GetValue(ctx context.Context, key string) (string, error) {
	var v string
	err := s.queryRow(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetValue writes a value.
func (s *Store) SetValue(ctx context.Context, key, value string) error {
	_, err := s.exec(ctx, `INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SetValueIfAbsent writes a value unless the key exists and returns the
// stored value.
func (s *Store) SetValueIfAbsent(ctx context.Context, key, value string) (string, error) {
	if _, err := s.exec(ctx, `INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING`, key, value); err != nil {
		return "", err
	}
	return s.GetValue(ctx, key)
}

// --- environments ---

// environment rows keep the status apart from the rest.
type envData struct {
	model.Environment
	Status *struct{} `json:"status,omitempty"`
}

func (s *Store) scanEnv(data, status string, rev int64) (*model.Environment, error) {
	e := &model.Environment{}
	if err := json.Unmarshal([]byte(data), e); err != nil {
		return nil, err
	}
	e.Status = model.EnvironmentStatus{}
	if err := json.Unmarshal([]byte(status), &e.Status); err != nil {
		return nil, err
	}
	e.Revision = rev
	return e, nil
}

func marshalEnv(e *model.Environment) (string, string, error) {
	d, err := json.Marshal(envData{Environment: *e})
	if err != nil {
		return "", "", err
	}
	st, err := json.Marshal(e.Status)
	return string(d), string(st), err
}

// GetEnvironment returns an environment.
func (s *Store) GetEnvironment(ctx context.Context, name string) (*model.Environment, error) {
	var data, status string
	var rev int64
	err := s.queryRow(ctx, `SELECT data, status, revision FROM environments WHERE name = ?`, name).Scan(&data, &status, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.scanEnv(data, status, rev)
}

// ListEnvironments returns all environments ordered by name.
func (s *Store) ListEnvironments(ctx context.Context) ([]model.Environment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data, status, revision FROM environments ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Environment
	for rows.Next() {
		var data, status string
		var rev int64
		if err := rows.Scan(&data, &status, &rev); err != nil {
			return nil, err
		}
		e, err := s.scanEnv(data, status, rev)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// CreateEnvironment inserts an environment. CreatedAt and Generation are set.
func (s *Store) CreateEnvironment(ctx context.Context, e *model.Environment) error {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	if e.Generation == 0 {
		e.Generation = 1
	}
	data, status, err := marshalEnv(e)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO environments (name, owner, data, status, revision, status_revision) VALUES (?, ?, ?, ?, 1, 1)`,
		e.Name, e.Spec.Owner, data, status)
	if isUnique(err) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	e.Revision = 1
	s.notify(KindEnvironment, e.Name)
	return nil
}

// UpdateEnvironment writes everything but the status. It fails with
// ErrConflict when the record changed since e was read. Generation increases
// when the spec changed.
func (s *Store) UpdateEnvironment(ctx context.Context, e *model.Environment) error {
	var oldData string
	err := s.queryRow(ctx, `SELECT data FROM environments WHERE name = ? AND revision = ?`, e.Name, e.Revision).Scan(&oldData)
	if errors.Is(err, sql.ErrNoRows) {
		if _, gerr := s.GetEnvironment(ctx, e.Name); errors.Is(gerr, ErrNotFound) {
			return ErrNotFound
		}
		return ErrConflict
	}
	if err != nil {
		return err
	}
	old := &model.Environment{}
	if err := json.Unmarshal([]byte(oldData), old); err != nil {
		return err
	}
	if specJSON(old.Spec) != specJSON(e.Spec) {
		e.Generation = old.Generation + 1
	} else {
		e.Generation = old.Generation
	}
	data, _, err := marshalEnv(e)
	if err != nil {
		return err
	}
	res, err := s.exec(ctx, `UPDATE environments SET data = ?, owner = ?, revision = revision + 1 WHERE name = ? AND revision = ?`,
		data, e.Spec.Owner, e.Name, e.Revision)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	e.Revision++
	s.notify(KindEnvironment, e.Name)
	return nil
}

func specJSON(spec model.EnvironmentSpec) string {
	b, _ := json.Marshal(spec)
	return string(b)
}

// MutateEnvironment reads, changes and writes an environment, retrying on
// conflicts. fn returning an error aborts.
func (s *Store) MutateEnvironment(ctx context.Context, name string, fn func(*model.Environment) error) (*model.Environment, error) {
	for attempt := 0; ; attempt++ {
		e, err := s.GetEnvironment(ctx, name)
		if err != nil {
			return nil, err
		}
		if err := fn(e); err != nil {
			return nil, err
		}
		err = s.UpdateEnvironment(ctx, e)
		if errors.Is(err, ErrConflict) && attempt < 5 {
			continue
		}
		return e, err
	}
}

// UpdateEnvironmentStatus writes the status (controller only).
func (s *Store) UpdateEnvironmentStatus(ctx context.Context, name string, st model.EnvironmentStatus) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	res, err := s.exec(ctx, `UPDATE environments SET status = ?, status_revision = status_revision + 1 WHERE name = ?`, string(b), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.notify(KindEnvironment, name)
	return nil
}

// DeleteEnvironment removes the record (after the controller cleaned up).
func (s *Store) DeleteEnvironment(ctx context.Context, name string) error {
	res, err := s.exec(ctx, `DELETE FROM environments WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.notify(KindEnvironment, name)
	return nil
}

// --- users ---

func (s *Store) GetUser(ctx context.Context, name string) (*model.User, error) {
	var data string
	var rev int64
	err := s.queryRow(ctx, `SELECT data, revision FROM users WHERE name = ?`, name).Scan(&data, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u := &model.User{}
	if err := json.Unmarshal([]byte(data), u); err != nil {
		return nil, err
	}
	u.Revision = rev
	return u, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data, revision FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.User
	for rows.Next() {
		var data string
		var u model.User
		if err := rows.Scan(&data, &u.Revision); err != nil {
			return nil, err
		}
		rev := u.Revision
		if err := json.Unmarshal([]byte(data), &u); err != nil {
			return nil, err
		}
		u.Revision = rev
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CreateUser(ctx context.Context, u *model.User) error {
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO users (name, data, revision) VALUES (?, ?, 1)`, u.Name, string(b))
	if isUnique(err) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	u.Revision = 1
	s.notify(KindUser, u.Name)
	return nil
}

func (s *Store) UpdateUser(ctx context.Context, u *model.User) error {
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	res, err := s.exec(ctx, `UPDATE users SET data = ?, revision = revision + 1 WHERE name = ? AND revision = ?`, string(b), u.Name, u.Revision)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetUser(ctx, u.Name); errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return ErrConflict
	}
	u.Revision++
	s.notify(KindUser, u.Name)
	return nil
}

// MutateUser reads, changes and writes a user, retrying on conflicts.
func (s *Store) MutateUser(ctx context.Context, name string, fn func(*model.User) error) (*model.User, error) {
	for attempt := 0; ; attempt++ {
		u, err := s.GetUser(ctx, name)
		if err != nil {
			return nil, err
		}
		if err := fn(u); err != nil {
			return nil, err
		}
		err = s.UpdateUser(ctx, u)
		if errors.Is(err, ErrConflict) && attempt < 5 {
			continue
		}
		return u, err
	}
}

func (s *Store) DeleteUser(ctx context.Context, name string) error {
	res, err := s.exec(ctx, `DELETE FROM users WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.notify(KindUser, name)
	return nil
}

// --- images (console created; configuration images are not stored) ---

func (s *Store) GetImage(ctx context.Context, name string) (*model.Image, error) {
	var data string
	var rev int64
	err := s.queryRow(ctx, `SELECT data, revision FROM images WHERE name = ?`, name).Scan(&data, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	i := &model.Image{}
	if err := json.Unmarshal([]byte(data), i); err != nil {
		return nil, err
	}
	i.Revision = rev
	return i, nil
}

func (s *Store) ListImages(ctx context.Context) ([]model.Image, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data, revision FROM images ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Image
	for rows.Next() {
		var data string
		var rev int64
		if err := rows.Scan(&data, &rev); err != nil {
			return nil, err
		}
		var i model.Image
		if err := json.Unmarshal([]byte(data), &i); err != nil {
			return nil, err
		}
		i.Revision = rev
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) CreateImage(ctx context.Context, i *model.Image) error {
	if i.CreatedAt.IsZero() {
		i.CreatedAt = time.Now().UTC().Truncate(time.Second)
	}
	b, err := json.Marshal(i)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO images (name, owner, data, revision) VALUES (?, ?, ?, 1)`, i.Name, i.Spec.Owner, string(b))
	if isUnique(err) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	i.Revision = 1
	s.notify(KindImage, i.Name)
	return nil
}

func (s *Store) UpdateImage(ctx context.Context, i *model.Image) error {
	b, err := json.Marshal(i)
	if err != nil {
		return err
	}
	res, err := s.exec(ctx, `UPDATE images SET data = ?, owner = ?, revision = revision + 1 WHERE name = ? AND revision = ?`, string(b), i.Spec.Owner, i.Name, i.Revision)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.GetImage(ctx, i.Name); errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return ErrConflict
	}
	i.Revision++
	s.notify(KindImage, i.Name)
	return nil
}

func (s *Store) DeleteImage(ctx context.Context, name string) error {
	res, err := s.exec(ctx, `DELETE FROM images WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	s.notify(KindImage, name)
	return nil
}

// --- tokens ---

// TokenRecord is a stored access token (hash only).
type TokenRecord struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	User       string     `json:"user"`
	Scopes     []string   `json:"scopes"`
	Hash       string     `json:"hash"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

func (s *Store) CreateToken(ctx context.Context, t *TokenRecord) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO tokens (id, user_name, data) VALUES (?, ?, ?)`, t.ID, t.User, string(b))
	if isUnique(err) {
		return ErrExists
	}
	return err
}

func (s *Store) GetToken(ctx context.Context, id string) (*TokenRecord, error) {
	var data string
	err := s.queryRow(ctx, `SELECT data FROM tokens WHERE id = ?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t := &TokenRecord{}
	return t, json.Unmarshal([]byte(data), t)
}

// ListTokens returns the tokens of a user, or of everyone when user is empty.
func (s *Store) ListTokens(ctx context.Context, user string) ([]TokenRecord, error) {
	query, args := `SELECT data FROM tokens ORDER BY id`, []any{}
	if user != "" {
		query, args = `SELECT data FROM tokens WHERE user_name = ? ORDER BY id`, []any{user}
	}
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenRecord
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var t TokenRecord
		if err := json.Unmarshal([]byte(data), &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateToken rewrites token metadata (last use).
func (s *Store) UpdateToken(ctx context.Context, t *TokenRecord) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `UPDATE tokens SET data = ? WHERE id = ?`, string(b), t.ID)
	return err
}

func (s *Store) DeleteToken(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteUserTokens(ctx context.Context, user string) error {
	_, err := s.exec(ctx, `DELETE FROM tokens WHERE user_name = ?`, user)
	return err
}

// --- change polling ---

type rowVersion struct{ rev, statusRev int64 }

// Poll emits changes made by other processes (other replicas, restores)
// until ctx ends, by comparing record revisions every interval.
func (s *Store) Poll(ctx context.Context, interval time.Duration) {
	seen := map[Kind]map[string]rowVersion{}
	first := true
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		for kind, query := range map[Kind]string{
			KindEnvironment: `SELECT name, revision, status_revision FROM environments`,
			KindUser:        `SELECT name, revision, 0 FROM users`,
			KindImage:       `SELECT name, revision, 0 FROM images`,
		} {
			cur, err := s.versions(ctx, query)
			if err != nil {
				continue
			}
			if !first {
				for name, v := range cur {
					if seen[kind][name] != v {
						s.notify(kind, name)
					}
				}
				for name := range seen[kind] {
					if _, ok := cur[name]; !ok {
						s.notify(kind, name)
					}
				}
			}
			seen[kind] = cur
		}
		first = false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Store) versions(ctx context.Context, query string) (map[string]rowVersion, error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]rowVersion{}
	for rows.Next() {
		var name string
		var v rowVersion
		if err := rows.Scan(&name, &v.rev, &v.statusRev); err != nil {
			return nil, err
		}
		out[name] = v
	}
	return out, rows.Err()
}
