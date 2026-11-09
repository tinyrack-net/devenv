package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Store saves backups.
type Store interface {
	Put(ctx context.Context, name string, data []byte) error
	Get(ctx context.Context, name string) ([]byte, error)
	// List returns backup names, oldest first.
	List(ctx context.Context) ([]string, error)
	Delete(ctx context.Context, name string) error
}

// Name returns the object name for a backup taken at t.
func Name(t time.Time) string { return "devenv-" + t.UTC().Format("20060102T150405Z") + ".json" }

// Latest is the alias for the newest backup in restore.
const Latest = "latest"

// OpenStore opens a destination:
//
//	file:///var/backups/devenv      a directory
//	s3://bucket/prefix              S3 compatible storage; endpoint and
//	                                credentials from AWS_ENDPOINT_URL (or
//	                                S3_ENDPOINT), AWS_ACCESS_KEY_ID,
//	                                AWS_SECRET_ACCESS_KEY, AWS_REGION
func OpenStore(dest string) (Store, error) {
	u, err := url.Parse(dest)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "", "file":
		dir := u.Path
		if u.Scheme == "" {
			dir = dest
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		return dirStore(dir), nil
	case "s3":
		endpoint := firstEnv("AWS_ENDPOINT_URL_S3", "AWS_ENDPOINT_URL", "S3_ENDPOINT")
		if endpoint == "" {
			endpoint = "https://s3.amazonaws.com"
		}
		eu, err := url.Parse(endpoint)
		if err != nil || eu.Host == "" {
			return nil, fmt.Errorf("invalid S3 endpoint %q", endpoint)
		}
		cl, err := minio.New(eu.Host, &minio.Options{
			Creds:        credentials.NewStaticV4(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), os.Getenv("AWS_SESSION_TOKEN")),
			Secure:       eu.Scheme != "http",
			Region:       firstEnv("AWS_REGION", "AWS_DEFAULT_REGION"),
			BucketLookup: minio.BucketLookupPath,
		})
		if err != nil {
			return nil, err
		}
		return &s3Store{c: cl, bucket: u.Host, prefix: strings.Trim(u.Path, "/")}, nil
	}
	return nil, fmt.Errorf("unsupported destination %q (use file:///dir or s3://bucket/prefix)", dest)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

type dirStore string

func (d dirStore) Put(_ context.Context, name string, data []byte) error {
	tmp := path.Join(string(d), "."+name+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path.Join(string(d), name))
}

func (d dirStore) Get(_ context.Context, name string) ([]byte, error) {
	return os.ReadFile(path.Join(string(d), name))
}

func (d dirStore) List(context.Context) ([]string, error) {
	ents, err := os.ReadDir(string(d))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "devenv-") && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func (d dirStore) Delete(_ context.Context, name string) error {
	return os.Remove(path.Join(string(d), name))
}

type s3Store struct {
	c      *minio.Client
	bucket string
	prefix string
}

func (s *s3Store) key(name string) string {
	if s.prefix == "" {
		return name
	}
	return s.prefix + "/" + name
}

func (s *s3Store) Put(ctx context.Context, name string, data []byte) error {
	_, err := s.c.PutObject(ctx, s.bucket, s.key(name), bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "application/json"})
	return err
}

func (s *s3Store) Get(ctx context.Context, name string) ([]byte, error) {
	o, err := s.c.GetObject(ctx, s.bucket, s.key(name), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer o.Close()
	return io.ReadAll(o)
}

func (s *s3Store) List(ctx context.Context) ([]string, error) {
	prefix := s.prefix
	if prefix != "" {
		prefix += "/"
	}
	var out []string
	for o := range s.c.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix + "devenv-"}) {
		if o.Err != nil {
			return nil, o.Err
		}
		name := strings.TrimPrefix(o.Key, prefix)
		if strings.HasSuffix(name, ".json") && !strings.Contains(name, "/") {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *s3Store) Delete(ctx context.Context, name string) error {
	return s.c.RemoveObject(ctx, s.bucket, s.key(name), minio.RemoveObjectOptions{})
}

// Prune keeps the newest keep backups.
func Prune(ctx context.Context, s Store, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	names, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var removed []string
	for len(names) > keep {
		if err := s.Delete(ctx, names[0]); err != nil {
			return removed, err
		}
		removed = append(removed, names[0])
		names = names[1:]
	}
	return removed, nil
}

// Resolve maps "latest" to the newest backup name.
func Resolve(ctx context.Context, s Store, name string) (string, error) {
	if name != Latest && name != "" {
		return name, nil
	}
	names, err := s.List(ctx)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no backups found")
	}
	return names[len(names)-1], nil
}
