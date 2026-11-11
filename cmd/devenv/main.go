package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/tinyrack-net/devenv/internal/activity"
	"github.com/tinyrack-net/devenv/internal/api"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/capacity"
	"github.com/tinyrack-net/devenv/internal/catalog"
	"github.com/tinyrack-net/devenv/internal/config"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/gateway"
	"github.com/tinyrack-net/devenv/internal/keys"
	"github.com/tinyrack-net/devenv/internal/mcpserver"
	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/server"
	"github.com/tinyrack-net/devenv/internal/sshclient"
	"github.com/tinyrack-net/devenv/internal/store"
	"github.com/tinyrack-net/devenv/internal/tokens"
	"github.com/tinyrack-net/devenv/internal/users"
	"github.com/tinyrack-net/devenv/web"
)

// version is set at build time.
var version = "dev"

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

type flags struct {
	metricsAddr, probeAddr, httpAddr, sshAddr string
	helperImage                               string
	leaderElect                               bool
	envNamespace, leaderNamespace             string
	serverNamespace                           string
	configFile                                string
}

func main() {
	var f flags
	flag.StringVar(&f.configFile, "config", envOr("DEVENV_CONFIG", ""), "Configuration file (YAML). See docs/configuration.md.")
	flag.StringVar(&f.metricsAddr, "metrics-bind-address", ":8081", "Metrics endpoint address. \"0\" disables it.")
	flag.StringVar(&f.probeAddr, "health-probe-bind-address", ":8082", "Health probe endpoint address.")
	flag.StringVar(&f.httpAddr, "http-bind-address", envOr("DEVENV_HTTP_ADDR", ":8080"), "API and web console address. Empty disables it.")
	flag.StringVar(&f.sshAddr, "ssh-bind-address", envOr("DEVENV_SSH_ADDR", ":2222"), "SSH gateway address. Empty disables it.")
	flag.StringVar(&f.helperImage, "helper-image", envOr("DEVENV_HELPER_IMAGE", ""), "Image mounted into environments at /opt/devenv to provide devenv-agent (usually the server image). Empty disables the helper.")
	flag.BoolVar(&f.leaderElect, "leader-elect", false, "Enable leader election.")
	flag.StringVar(&f.envNamespace, "environment-namespace", envOr("DEVENV_ENVIRONMENT_NAMESPACE", "devenv"), "Namespace where environments live.")
	flag.StringVar(&f.leaderNamespace, "leader-election-namespace", os.Getenv("POD_NAMESPACE"), "Namespace for the leader election lease.")
	flag.StringVar(&f.serverNamespace, "server-namespace", envOr("POD_NAMESPACE", ""), "Namespace of the devenv server (image prepull DaemonSet). Defaults to the environment namespace.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")
	exit := func(err error, msg string) {
		log.Error(err, msg)
		os.Exit(1)
	}

	cfg, err := config.Load(f.configFile)
	if err != nil {
		exit(err, "load configuration")
	}
	if err := cfg.ResolveSecrets(); err != nil {
		exit(err, "read configuration secrets")
	}
	cfg.Defaults()

	envNamespace := f.envNamespace
	serverNamespace := f.serverNamespace
	if serverNamespace == "" {
		serverNamespace = envNamespace
	}

	// Namespaced objects are cached in the environment namespace; the server
	// namespace holds the prepull DaemonSet.
	cacheOpts := cache.Options{DefaultNamespaces: map[string]cache.Config{envNamespace: {}}}
	cacheOpts.ByObject = map[client.Object]cache.ByObject{
		&appsv1.DaemonSet{}: {Namespaces: map[string]cache.Config{serverNamespace: {}}},
	}

	restCfg := ctrl.GetConfigOrDie()
	direct, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		exit(err, "create client")
	}

	dsn := cfg.Database.Path
	if cfg.Database.Driver == "postgres" {
		dsn = cfg.Database.URL
	}
	var db *store.Store
	for attempt := 0; ; attempt++ {
		db, err = store.Open(context.Background(), cfg.Database.Driver, dsn)
		if err == nil {
			break
		}
		if attempt >= 30 || errors.Is(err, store.ErrSchemaVersion) {
			exit(err, "open database")
		}
		log.Info("database not ready, retrying", "error", err.Error())
		time.Sleep(5 * time.Second)
	}
	log.Info("database ready", "driver", cfg.Database.Driver, "schema", store.SchemaVersion)

	serverKeys, err := keys.Ensure(context.Background(), db)
	if err != nil {
		exit(err, "bootstrap server keys")
	}
	if err := cfg.Validate(); err != nil {
		exit(err, "invalid configuration")
	}
	if cfg.Auth.Disabled {
		log.Info("WARNING: authentication is disabled; every request acts as the local administrator")
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: f.metricsAddr},
		HealthProbeBindAddress:  f.probeAddr,
		LeaderElection:          f.leaderElect,
		LeaderElectionID:        "devenv.tinyrack.net",
		LeaderElectionNamespace: f.leaderNamespace,
		Cache:                   cacheOpts,
	})
	if err != nil {
		exit(err, "create manager")
	}

	cat := catalog.New(cfg, db)
	if err := (&controller.EnvironmentReconciler{
		Client:    mgr.GetClient(),
		Recorder:  mgr.GetEventRecorder("devenv"),
		DB:        db,
		Catalog:   cat,
		Namespace: envNamespace,

		GatewayAuthorizedKey:  serverKeys.ClientAuthorizedKey(),
		HelperImage:           f.helperImage,
		HelperImagePullPolicy: corev1.PullIfNotPresent,
	}).SetupWithManager(mgr); err != nil {
		exit(err, "setup controller")
	}

	userStore := &users.Store{DB: db, Config: cfg}
	tokenStore := &tokens.Store{DB: db}

	dialer := &sshclient.Dialer{DB: db, Namespace: envNamespace, Signer: serverKeys.ClientKey}
	pool := &sshclient.Pool{Dialer: dialer}
	if f.sshAddr != "" {
		if err := mgr.Add(&gateway.Gateway{
			Addr: f.sshAddr, HostKey: serverKeys.HostKey, Dialer: dialer,
			Users: func(ctx context.Context) ([]model.User, error) { return userStore.List(ctx) },
		}); err != nil {
			exit(err, "add ssh gateway")
		}
	}

	if f.httpAddr != "" {
		sessions, err := auth.NewSessions(serverKeys.SessionKey, cfg.Auth.Session.SecureCookies)
		if err != nil {
			exit(err, "create sessions")
		}
		sessions.TTL = cfg.Auth.Session.TTL.Duration
		oidcs := map[string]*auth.OIDC{}
		for _, p := range cfg.Auth.OIDC {
			oidcs[p.ID] = &auth.OIDC{Config: auth.OIDCConfig{
				ID: p.ID, Issuer: p.Issuer, ClientID: p.ClientID, ClientSecret: p.ClientSecret, Scopes: p.Scopes,
				RedirectURL: strings.TrimRight(cfg.Server.ExternalURL, "/") + "/api/v1/auth/oidc/" + p.ID + "/callback",
				Claims:      auth.ClaimNames{Username: p.Claims.Username, Email: p.Claims.Email, Name: p.Claims.Name, Groups: p.Claims.Groups},
			}}
			log.Info("OIDC provider configured", "id", p.ID, "issuer", p.Issuer)
		}
		acts := &activity.Log{}
		broker := &api.Broker{}
		broker.Watch(context.Background(), db)
		srv := &api.Server{
			DB:          db,
			Catalog:     cat,
			Client:      direct,
			Reader:      direct,
			Namespace:   envNamespace,
			Config:      cfg,
			Sessions:    sessions,
			Users:       userStore,
			Tokens:      tokenStore,
			OIDC:        oidcs,
			Capacity:    &capacity.Checker{Reader: direct, Namespace: envNamespace},
			Static:      web.Dist(),
			Dialer:      dialer,
			Pool:        pool,
			Activity:    acts,
			Events:      broker,
			HelperImage: f.helperImage,
			SSH: api.SSHInfo{
				Enabled:     f.sshAddr != "",
				Host:        cfg.SSH.PublicHost,
				Port:        cfg.SSH.PublicPort,
				HostKey:     strings.TrimSpace(string(ssh.MarshalAuthorizedKey(serverKeys.HostKey.PublicKey()))),
				Fingerprint: ssh.FingerprintSHA256(serverKeys.HostKey.PublicKey()),
			},
			Version: version,
		}
		if srv.Static == nil {
			log.Info("web console assets not embedded; serving API only")
		}
		handler := srv.Handler()
		if cfg.MCP.IsEnabled() {
			mcpSrv := &mcpserver.Server{API: srv, Dialer: dialer, Pool: pool, Resolver: srv, Version: version, Activity: acts}
			mux := http.NewServeMux()
			mux.Handle("/mcp", mcpSrv.Handler())
			mux.Handle("/", handler)
			handler = mux
			log.Info("MCP endpoint enabled", "path", "/mcp")
		}
		if err := mgr.Add(&server.HTTP{Addr: f.httpAddr, Handler: handler}); err != nil {
			exit(err, "add http server")
		}
	}

	if f.helperImage != "" {
		if err := (&controller.PrepullReconciler{Client: mgr.GetClient(), Catalog: cat, Namespace: serverNamespace, Image: f.helperImage}).SetupWithManager(mgr); err != nil {
			exit(err, "setup image prepull")
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		exit(err, "add healthz")
	}
	if err := mgr.AddReadyzCheck("readyz", func(r *http.Request) error { return db.Ping(r.Context()) }); err != nil {
		exit(err, "add readyz")
	}

	log.Info("starting manager", "version", version, "environmentNamespace", envNamespace, "http", f.httpAddr,
		"passwordLogin", cfg.Auth.Password.IsEnabled(), "oidcProviders", len(cfg.Auth.OIDC))
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		exit(err, "run manager")
	}
}
