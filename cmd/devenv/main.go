package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

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

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/api"
	"github.com/tinyrack-net/devenv/internal/auth"
	"github.com/tinyrack-net/devenv/internal/capacity"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/gateway"
	"github.com/tinyrack-net/devenv/internal/keys"
	"github.com/tinyrack-net/devenv/internal/mcpserver"
	"github.com/tinyrack-net/devenv/internal/server"
	"github.com/tinyrack-net/devenv/internal/sshclient"
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

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
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
		sshAddr         string
		sshPublicHost   string
		sshPublicPort   int
		mcpEnabled      bool
		helperImage     string
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
	flag.StringVar(&sshAddr, "ssh-bind-address", envOr("DEVENV_SSH_ADDR", ":2222"), "SSH gateway address. Empty disables it.")
	flag.StringVar(&sshPublicHost, "ssh-public-host", envOr("DEVENV_SSH_PUBLIC_HOST", ""), "Host users connect to for SSH. Empty uses the web console host.")
	flag.IntVar(&sshPublicPort, "ssh-public-port", envInt("DEVENV_SSH_PUBLIC_PORT", 2222), "Port users connect to for SSH.")
	flag.StringVar(&helperImage, "helper-image", envOr("DEVENV_HELPER_IMAGE", ""), "Image mounted into environments at /opt/devenv to provide devenv-agent (usually the server image). Empty disables the helper.")
	flag.BoolVar(&mcpEnabled, "mcp", envOr("DEVENV_MCP_ENABLED", "true") == "true", "Serve the MCP endpoint at /mcp.")
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

	// Namespaced objects are cached in the environment namespace.
	cacheOpts := cache.Options{DefaultNamespaces: map[string]cache.Config{envNamespace: {}}}

	cfg := ctrl.GetConfigOrDie()
	if serverNamespace == "" {
		serverNamespace = envNamespace
	}
	// The image prepull DaemonSet lives in the server namespace; only that
	// type is cached there (the server has no other rights in it).
	cacheOpts.ByObject = map[client.Object]cache.ByObject{
		&appsv1.DaemonSet{}: {Namespaces: map[string]cache.Config{serverNamespace: {}}},
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

		GatewayAuthorizedKey:  serverKeys.ClientAuthorizedKey(),
		HelperImage:           helperImage,
		HelperImagePullPolicy: corev1.PullIfNotPresent,
	}).SetupWithManager(mgr); err != nil {
		exit(err, "setup controller")
	}

	// Environment connections read the environment and its Secret through the
	// cache (namespaced to the environment namespace).
	dialer := &sshclient.Dialer{
		Client:    mgr.GetClient(),
		Namespace: envNamespace,
		Signer:    serverKeys.ClientKey,
	}
	if sshAddr != "" {
		if err := mgr.Add(&gateway.Gateway{Addr: sshAddr, HostKey: serverKeys.HostKey, Dialer: dialer}); err != nil {
			exit(err, "add ssh gateway")
		}
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
			Dialer:    dialer,
			SSH: api.SSHInfo{
				Enabled:     sshAddr != "",
				Host:        sshPublicHost,
				Port:        sshPublicPort,
				HostKey:     strings.TrimSpace(string(ssh.MarshalAuthorizedKey(serverKeys.HostKey.PublicKey()))),
				Fingerprint: ssh.FingerprintSHA256(serverKeys.HostKey.PublicKey()),
			},
			Version: version,
		}
		if srv.Static == nil {
			log.Info("web console assets not embedded; serving API only")
		}
		handler := srv.Handler()
		if mcpEnabled {
			token := serverKeys.MCPToken
			if t, err := readSecret("DEVENV_MCP_TOKEN"); err != nil {
				exit(err, "read mcp token")
			} else if t != "" {
				token = t
			}
			mcpSrv := &mcpserver.Server{
				API: srv, Dialer: dialer, Token: token, Version: version,
			}
			mux := http.NewServeMux()
			mux.Handle("/mcp", mcpSrv.Handler())
			mux.Handle("/", handler)
			handler = mux
			log.Info("MCP endpoint enabled", "path", "/mcp")
		}
		if err := mgr.Add(&server.HTTP{Addr: httpAddr, Handler: handler}); err != nil {
			exit(err, "add http server")
		}
	}

	if helperImage != "" {
		if err := (&controller.PrepullReconciler{Client: mgr.GetClient(), Namespace: serverNamespace, Image: helperImage}).SetupWithManager(mgr); err != nil {
			exit(err, "setup image prepull")
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
