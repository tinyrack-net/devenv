package main

import (
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/controller"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(devenvv1.AddToScheme(scheme))
}

func main() {
	var (
		metricsAddr     string
		probeAddr       string
		leaderElect     bool
		envNamespace    string
		leaderNamespace string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8081", "Metrics endpoint address. \"0\" disables it.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8082", "Health probe endpoint address.")
	flag.BoolVar(&leaderElect, "leader-elect", false, "Enable leader election.")
	flag.StringVar(&envNamespace, "environment-namespace", os.Getenv("DEVENV_ENVIRONMENT_NAMESPACE"),
		"Namespace where environments live. Empty watches all namespaces.")
	flag.StringVar(&leaderNamespace, "leader-election-namespace", os.Getenv("POD_NAMESPACE"), "Namespace for the leader election lease.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("setup")

	cacheOpts := cache.Options{}
	if envNamespace != "" {
		cacheOpts.DefaultNamespaces = map[string]cache.Config{envNamespace: {}}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          leaderElect,
		LeaderElectionID:        "devenv.tinyrack.net",
		LeaderElectionNamespace: leaderNamespace,
		Cache:                   cacheOpts,
	})
	if err != nil {
		log.Error(err, "create manager")
		os.Exit(1)
	}

	if err := (&controller.DevEnvironmentReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("devenv"),
	}).SetupWithManager(mgr); err != nil {
		log.Error(err, "setup controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "add healthz")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "add readyz")
		os.Exit(1)
	}

	log.Info("starting manager", "environmentNamespace", envNamespace)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "run manager")
		os.Exit(1)
	}
}
