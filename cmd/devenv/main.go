package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
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
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/activity"
	"github.com/tinyrack-net/devenv/internal/api"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/capacity"
	"github.com/tinyrack-net/devenv/internal/config"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/gateway"
	"github.com/tinyrack-net/devenv/internal/keys"
	"github.com/tinyrack-net/devenv/internal/mcpserver"
	"github.com/tinyrack-net/devenv/internal/server"
	"github.com/tinyrack-net/devenv/internal/sshclient"
	"github.com/tinyrack-net/devenv/internal/tokens"
	"github.com/tinyrack-net/devenv/internal/users"
	"github.com/tinyrack-net/devenv/web"
)

// version is set at build time.
var version = "dev"

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(devenvv1.AddToScheme(scheme))
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// readSecret returns the value of an environment variable or the content of
// the file named by KEY_FILE, and whether either was set.
func readSecret(key string) (string, bool, error) {
	if f := os.Getenv(key + "_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", true, fmt.Errorf("read %s_FILE: %w", key, err)
		}
		return strings.TrimSpace(string(b)), true, nil
	}
	v, ok := os.LookupEnv(key)
	return v, ok, nil
}

// applyLegacy maps the flags and environment variables of earlier versions
// onto the configuration. Explicitly set values override the file.
func applyLegacy(cfg *config.Config, set map[string]bool, f *flags) error {
	if v, ok := os.LookupEnv("DEVENV_AUTH_MODE"); ok || set["auth-mode"] {
		if set["auth-mode"] {
			v = f.authMode
		}
		switch v {
		case "password":
		case "none":
			cfg.Auth.Disabled = true
		default:
			return fmt.Errorf("unknown auth mode %q", v)
		}
	}
	if v, ok := os.LookupEnv("DEVENV_ADMIN_USERNAME"); ok && v != "" {
		cfg.Auth.Password.Username = v
	}
	if set["admin-username"] {
		cfg.Auth.Password.Username = f.adminUser
	}
	if v, ok := os.LookupEnv("DEVENV_SECURE_COOKIES"); ok {
		cfg.Auth.Session.SecureCookies = v == "true"
	}
	if set["secure-cookies"] {
		cfg.Auth.Session.SecureCookies = f.secureCookies
	}
	if pw, ok, err := readSecret("DEVENV_ADMIN_PASSWORD"); err != nil {
		return err
	} else if ok && pw != "" {
		cfg.Auth.Password.Password = pw
	}
	if v, ok := os.LookupEnv("DEVENV_MCP_ENABLED"); ok {
		cfg.MCP.Enabled = new(v == "true")
	}
	if set["mcp"] {
		cfg.MCP.Enabled = new(f.mcpEnabled)
	}
	if t, ok, err := readSecret("DEVENV_MCP_TOKEN"); err != nil {
		return err
	} else if ok && t != "" {
		cfg.MCP.LegacyToken.Token = t
	}
	if v, ok := os.LookupEnv("DEVENV_SSH_PUBLIC_HOST"); ok && v != "" {
		cfg.SSH.PublicHost = v
	}
	if v, ok := os.LookupEnv("DEVENV_SSH_PUBLIC_PORT"); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.SSH.PublicPort = n
		}
	}
	if set["ssh-public-host"] {
		cfg.SSH.PublicHost = f.sshPublicHost
	}
	if set["ssh-public-port"] {
		cfg.SSH.PublicPort = f.sshPublicPort
	}
	if v, ok := os.LookupEnv("DEVENV_EXTERNAL_URL"); ok && v != "" {
		cfg.Server.ExternalURL = v
	}
	return nil
}

type flags struct {
	metricsAddr, probeAddr, httpAddr, sshAddr string
	sshPublicHost                             string
	sshPublicPort                             int
	mcpEnabled                                bool
	helperImage                               string
	leaderElect                               bool
	envNamespace, leaderNamespace             string
	authMode, serverNamespace, keysSecret     string
	adminUser                                 string
	secureCookies                             bool
	configFile                                string
}

func main() {
	var f flags
	flag.StringVar(&f.configFile, "config", envOr("DEVENV_CONFIG", ""), "Configuration file (YAML). See docs/configuration.md.")
	flag.StringVar(&f.metricsAddr, "metrics-bind-address", ":8081", "Metrics endpoint address. \"0\" disables it.")
	flag.StringVar(&f.probeAddr, "health-probe-bind-address", ":8082", "Health probe endpoint address.")
	flag.StringVar(&f.httpAddr, "http-bind-address", envOr("DEVENV_HTTP_ADDR", ":8080"), "API and web console address. Empty disables it.")
	flag.StringVar(&f.sshAddr, "ssh-bind-address", envOr("DEVENV_SSH_ADDR", ":2222"), "SSH gateway address. Empty disables it.")
	flag.StringVar(&f.sshPublicHost, "ssh-public-host", "", "Host users connect to for SSH (overrides ssh.publicHost).")
	flag.IntVar(&f.sshPublicPort, "ssh-public-port", 0, "Port users connect to for SSH (overrides ssh.publicPort).")
	flag.StringVar(&f.helperImage, "helper-image", envOr("DEVENV_HELPER_IMAGE", ""), "Image mounted into environments at /opt/devenv to provide devenv-agent (usually the server image). Empty disables the helper.")
	flag.BoolVar(&f.mcpEnabled, "mcp", true, "Serve the MCP endpoint at /mcp (overrides mcp.enabled).")
	flag.BoolVar(&f.leaderElect, "leader-elect", false, "Enable leader election.")
	flag.StringVar(&f.envNamespace, "environment-namespace", envOr("DEVENV_ENVIRONMENT_NAMESPACE", "devenv"), "Namespace where environments live.")
	flag.StringVar(&f.leaderNamespace, "leader-election-namespace", os.Getenv("POD_NAMESPACE"), "Namespace for the leader election lease.")
	flag.StringVar(&f.authMode, "auth-mode", "password", "Deprecated: password or none (auth.disabled).")
	flag.StringVar(&f.adminUser, "admin-username", "admin", "Deprecated: auth.password.username.")
	flag.BoolVar(&f.secureCookies, "secure-cookies", false, "Deprecated: auth.session.secureCookies.")
	flag.StringVar(&f.serverNamespace, "server-namespace", envOr("POD_NAMESPACE", ""), "Namespace of the devenv server (key Secret, tokens). Defaults to the environment namespace.")
	flag.StringVar(&f.keysSecret, "keys-secret", envOr("DEVENV_KEYS_SECRET", "devenv-keys"), "Secret holding generated server keys.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	set := map[string]bool{}
	flag.Visit(func(fl *flag.Flag) { set[fl.Name] = true })

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
	if err := applyLegacy(cfg, set, &f); err != nil {
		exit(err, "apply flags")
	}
	cfg.Defaults()

	envNamespace := f.envNamespace
	serverNamespace := f.serverNamespace
	if serverNamespace == "" {
		serverNamespace = envNamespace
	}

	// Namespaced objects are cached in the environment namespace; the server
	// namespace holds the prepull DaemonSet and token Secrets.
	cacheOpts := cache.Options{DefaultNamespaces: map[string]cache.Config{envNamespace: {}}}
	cacheOpts.ByObject = map[client.Object]cache.ByObject{
		&appsv1.DaemonSet{}: {Namespaces: map[string]cache.Config{serverNamespace: {}}},
	}

	restCfg := ctrl.GetConfigOrDie()
	direct, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		exit(err, "create client")
	}
	serverKeys, err := keys.Ensure(context.Background(), direct, serverNamespace, f.keysSecret)
	if err != nil {
		exit(err, "bootstrap server keys")
	}
	if cfg.MCP.LegacyToken.Token == "" {
		cfg.MCP.LegacyToken.Token = serverKeys.MCPToken
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

	if err := (&controller.DevEnvironmentReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("devenv"),

		GatewayAuthorizedKey:  serverKeys.ClientAuthorizedKey(),
		HelperImage:           f.helperImage,
		HelperImagePullPolicy: corev1.PullIfNotPresent,
	}).SetupWithManager(mgr); err != nil {
		exit(err, "setup controller")
	}

	userStore := &users.Store{Client: mgr.GetClient(), Config: cfg}
	// Tokens live in the server namespace, which the cache does not cover;
	// read them directly (one GET per token request).
	tokenStore := &tokens.Store{Client: direct, Namespace: serverNamespace}

	dialer := &sshclient.Dialer{Client: mgr.GetClient(), Namespace: envNamespace, Signer: serverKeys.ClientKey}
	pool := &sshclient.Pool{Dialer: dialer}
	if f.sshAddr != "" {
		if err := mgr.Add(&gateway.Gateway{
			Addr: f.sshAddr, HostKey: serverKeys.HostKey, Dialer: dialer,
			Users: func(ctx context.Context) ([]devenvv1.DevUser, error) { return userStore.List(ctx) },
		}); err != nil {
			exit(err, "add ssh gateway")
		}
	}

	// One-time data migration from single-user versions, after the cache
	// starts (the user store reads through it).
	if err := mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			return nil
		}
		for attempt := 0; ; attempt++ {
			err := userStore.Migrate(ctx, direct, envNamespace)
			if err == nil {
				return nil
			}
			log.Error(err, "migrate data (retrying)")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(10 * time.Second):
			}
		}
	})); err != nil {
		exit(err, "add migration")
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
		if err := broker.Watch(context.Background(), mgr.GetCache()); err != nil {
			exit(err, "watch environments")
		}
		srv := &api.Server{
			Client:      mgr.GetClient(),
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
		if err := (&controller.PrepullReconciler{Client: mgr.GetClient(), Namespace: serverNamespace, Image: f.helperImage}).SetupWithManager(mgr); err != nil {
			exit(err, "setup image prepull")
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		exit(err, "add healthz")
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		exit(err, "add readyz")
	}

	log.Info("starting manager", "version", version, "environmentNamespace", envNamespace, "http", f.httpAddr,
		"passwordLogin", cfg.Auth.Password.IsEnabled(), "oidcProviders", len(cfg.Auth.OIDC))
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		exit(err, "run manager")
	}
}
