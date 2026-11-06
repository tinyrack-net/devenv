package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/api"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/capacity"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/keys"
	"github.com/tinyrack-net/devenv/internal/server"
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
// the file named by KEY_FILE.
func readSecret(key string) (string, error) {
	if f := os.Getenv(key + "_FILE"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("read %s_FILE: %w", key, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return os.Getenv(key), nil
}

func main() {
	var (
		metricsAddr     string
		probeAddr       string
		httpAddr        string
		leaderElect     bool
		envNamespace    string
		leaderNamespace string
		authMode        string
		serverNamespace string
		keysSecret      string
		adminUser       string
		secureCookies   bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8081", "Metrics endpoint address. \"0\" disables it.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8082", "Health probe endpoint address.")
	flag.StringVar(&httpAddr, "http-bind-address", envOr("DEVENV_HTTP_ADDR", ":8080"), "API and web console address. Empty disables it.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election.")
	flag.StringVar(&envNamespace, "environment-namespace", envOr("DEVENV_ENVIRONMENT_NAMESPACE", "devenv"),
		"Namespace where environments live.")
	flag.StringVar(&leaderNamespace, "leader-election-namespace", os.Getenv("POD_NAMESPACE"), "Namespace for the leader election lease.")
	flag.StringVar(&authMode, "auth-mode", envOr("DEVENV_AUTH_MODE", "password"), "Authentication mode: password or none (development only).")
	flag.StringVar(&adminUser, "admin-username", envOr("DEVENV_ADMIN_USERNAME", "admin"), "Admin username for password auth.")
	flag.BoolVar(&secureCookies, "secure-cookies", envOr("DEVENV_SECURE_COOKIES", "false") == "true", "Set the Secure flag on session cookies (enable behind HTTPS).")
	flag.StringVar(&serverNamespace, "server-namespace", envOr("POD_NAMESPACE", ""), "Namespace of the devenv server, used for its key Secret. Defaults to the environment namespace.")
	flag.StringVar(&keysSecret, "keys-secret", envOr("DEVENV_KEYS_SECRET", "devenv-keys"), "Secret holding generated server keys.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")
	exit := func(err error, msg string) {
		log.Error(err, msg)
		os.Exit(1)
	}

	// Cluster scoped CRDs are cached cluster wide; namespaced objects only in the environment namespace.
	cacheOpts := cache.Options{DefaultNamespaces: map[string]cache.Config{envNamespace: {}}}

	cfg := ctrl.GetConfigOrDie()
	if serverNamespace == "" {
		serverNamespace = envNamespace
	}

	// Uncached client for bootstrap and cluster wide reads (nodes, pods, events).
	direct, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		exit(err, "create client")
	}
	serverKeys, err := keys.Ensure(context.Background(), direct, serverNamespace, keysSecret)
	if err != nil {
		exit(err, "bootstrap server keys")
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          leaderElect,
		LeaderElectionID:        "devenv.tinyrack.net",
		LeaderElectionNamespace: leaderNamespace,
		Cache:                   cacheOpts,
	})
	if err != nil {
		exit(err, "create manager")
	}

	if err := (&controller.DevEnvironmentReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("devenv"),

		GatewayAuthorizedKey: serverKeys.ClientAuthorizedKey(),
	}).SetupWithManager(mgr); err != nil {
		exit(err, "setup controller")
	}

	if httpAddr != "" {
		var authenticator auth.Authenticator
		switch authMode {
		case "password":
			pw, err := readSecret("DEVENV_ADMIN_PASSWORD")
			if err != nil {
				exit(err, "read admin password")
			}
			if pw == "" {
				exit(fmt.Errorf("DEVENV_ADMIN_PASSWORD or DEVENV_ADMIN_PASSWORD_FILE is required"), "configure auth")
			}
			authenticator = &auth.PasswordAuthenticator{Username: adminUser, Password: pw}
		case "none":
			log.Info("WARNING: authentication is disabled")
			authenticator = &auth.NoneAuthenticator{Username: adminUser}
		default:
			exit(fmt.Errorf("unknown auth mode %q", authMode), "configure auth")
		}
		sessions, err := auth.NewSessions(serverKeys.SessionKey, secureCookies)
		if err != nil {
			exit(err, "create sessions")
		}
		reader := direct

		srv := &api.Server{
			Client:    mgr.GetClient(),
			Reader:    reader,
			Namespace: envNamespace,
			Auth:      authenticator,
			Sessions:  sessions,
			Capacity:  &capacity.Checker{Reader: reader, Namespace: envNamespace},
			Static:    web.Dist(),
			Version:   version,
		}
		if srv.Static == nil {
			log.Info("web console assets not embedded; serving API only")
		}
		if err := mgr.Add(&server.HTTP{Addr: httpAddr, Handler: srv.Handler()}); err != nil {
			exit(err, "add http server")
		}
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		exit(err, "add healthz")
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		exit(err, "add readyz")
	}

	log.Info("starting manager", "version", version, "environmentNamespace", envNamespace, "http", httpAddr)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		exit(err, "run manager")
	}
}
