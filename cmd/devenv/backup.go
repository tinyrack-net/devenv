package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tinyrack-net/devenv/internal/backup"
)

// runBackupCommand handles `devenv backup` and `devenv restore`. It returns
// false when args are not a backup command.
func runBackupCommand(args []string) bool {
	if len(args) == 0 || (args[0] != "backup" && args[0] != "restore") {
		return false
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dest := fs.String("dest", envOr("DEVENV_BACKUP_DEST", ""), "Destination: file:///dir or s3://bucket/prefix (S3 endpoint and credentials from AWS_* variables).")
	envNS := fs.String("environment-namespace", envOr("DEVENV_ENVIRONMENT_NAMESPACE", "devenv"), "Namespace of environments.")
	serverNS := fs.String("server-namespace", envOr("POD_NAMESPACE", "devenv-system"), "Namespace of the devenv server (keys, tokens).")
	keys := fs.String("keys-secret", envOr("DEVENV_KEYS_SECRET", "devenv-keys"), "Server key Secret.")
	keep := fs.Int("keep", 14, "backup: number of backups to keep (0 keeps all).")
	name := fs.String("name", backup.Latest, "restore: backup to restore (file name or \"latest\").")
	overwrite := fs.Bool("overwrite", false, "restore: replace objects that already exist.")
	start := fs.Bool("start", true, "restore: start environments that were running once their home volume exists.")
	wait := fs.Duration("volume-timeout", 30*time.Minute, "restore: how long to wait for home volumes before giving up on starting.")
	dryRun := fs.Bool("dry-run", false, "restore: only print what the backup contains.")
	_ = fs.Parse(args[1:])

	if *dest == "" {
		fmt.Fprintln(os.Stderr, "--dest (or DEVENV_BACKUP_DEST) is required")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := backup.OpenStore(*dest)
	if err != nil {
		fail(err)
	}
	opts := backup.Options{EnvironmentNamespace: *envNS, ServerNamespace: *serverNS, KeysSecret: *keys, ServerVersion: version}

	var c client.Client
	if !*dryRun {
		c, err = client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
		if err != nil {
			fail(err)
		}
	}

	switch cmd {
	case "backup":
		b, err := backup.Export(ctx, c, opts)
		if err != nil {
			fail(err)
		}
		data, err := b.Marshal()
		if err != nil {
			fail(err)
		}
		n := backup.Name(b.CreatedAt)
		if err := store.Put(ctx, n, data); err != nil {
			fail(fmt.Errorf("upload %s: %w", n, err))
		}
		fmt.Printf("backup %s: %d users, %d images, %d classes, %d environments, %d server secrets (%d bytes)\n",
			n, len(b.Users), len(b.Images), len(b.Classes), len(b.Environments), len(b.ServerSecrets), len(data))
		removed, err := backup.Prune(ctx, store, *keep)
		if err != nil {
			fail(fmt.Errorf("prune: %w", err))
		}
		for _, r := range removed {
			fmt.Println("removed old backup", r)
		}
	case "restore":
		n, err := backup.Resolve(ctx, store, *name)
		if err != nil {
			fail(err)
		}
		data, err := store.Get(ctx, n)
		if err != nil {
			fail(fmt.Errorf("download %s: %w", n, err))
		}
		b, err := backup.Unmarshal(data)
		if err != nil {
			fail(err)
		}
		fmt.Printf("backup %s from %s (devenv %s): %d users, %d images, %d classes, %d environments\n",
			n, b.CreatedAt.Format(time.RFC3339), b.ServerVersion, len(b.Users), len(b.Images), len(b.Classes), len(b.Environments))
		if *dryRun {
			for _, e := range b.Environments {
				fmt.Printf("  %s owner=%s image=%s class=%s running=%v\n", e.Name, e.Spec.Owner, e.Spec.ImageRef, e.ClassName(), e.WasRunning)
			}
			return true
		}
		res, err := backup.Restore(ctx, c, b, backup.RestoreOptions{
			Options: opts, Overwrite: *overwrite, Start: *start, VolumeTimeout: *wait,
			Log: func(format string, a ...any) { fmt.Printf(format+"\n", a...) },
		})
		if err != nil {
			fail(err)
		}
		fmt.Printf("restored: %d created, %d updated, %d kept; started %v; waiting for volumes %v\n",
			res.Created, res.Updated, res.Kept, res.Started, res.Waiting)
	}
	return true
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
