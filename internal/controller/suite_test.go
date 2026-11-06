package controller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
)

var (
	k8sClient client.Client
	testCtx   context.Context
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// envtest binaries are required; run through `make test`.
		os.Stderr.WriteString("KUBEBUILDER_ASSETS not set, skipping controller tests\n")
		os.Exit(0)
	}
	ctrl.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(os.Stderr), zap.Level(zapLevel())))

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		panic(err)
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(devenvv1.AddToScheme(scheme))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		panic(err)
	}
	if err := (&DevEnvironmentReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("devenv"),
	}).SetupWithManager(mgr); err != nil {
		panic(err)
	}

	if err := (&PrepullReconciler{Client: mgr.GetClient(), Namespace: "devenv-system", Image: "devenv:test"}).SetupWithManager(mgr); err != nil {
		panic(err)
	}

	var cancel context.CancelFunc
	testCtx, cancel = context.WithCancel(context.Background())
	go func() {
		if err := mgr.Start(testCtx); err != nil {
			panic(err)
		}
	}()

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		panic(err)
	}

	if err := k8sClient.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "devenv-system"}}); err != nil {
		panic(err)
	}
	code := m.Run()
	cancel()
	_ = env.Stop()
	os.Exit(code)
}

func zapLevel() zapLevelT {
	if os.Getenv("DEVENV_TEST_VERBOSE") != "" {
		return -1
	}
	return 2
}

// eventually polls fn until it returns nil or the timeout expires.
func eventually(t *testing.T, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var err error
	for time.Now().Before(deadline) {
		if err = fn(); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("condition not met: %v", err)
}
