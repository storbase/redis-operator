//go:build integration
// +build integration

package controller

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	redisv1alpha1 "github.com/storbase/redis-operator/api/v1alpha1"
)

var (
	testEnv    *envtest.Environment
	testCtx    context.Context
	testStop   context.CancelFunc
	k8sClient  client.Client
	testScheme *runtime.Scheme
)

func TestMain(m *testing.M) {
	ctrl.SetLogger(zap.New(zap.WriteTo(io.Discard), zap.UseDevMode(true)))

	testScheme = runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(testScheme)
	_ = appsv1.AddToScheme(testScheme)
	_ = corev1.AddToScheme(testScheme)
	_ = redisv1alpha1.AddToScheme(testScheme)

	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if path := firstEnvtestBinaryDir(); path != "" {
			testEnv.BinaryAssetsDirectory = path
		}
	}

	cfg, err := testEnv.Start()
	if err != nil {
		panic(err)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		panic(err)
	}

	testCtx, testStop = context.WithCancel(context.Background())
	code := m.Run()
	testStop()
	_ = testEnv.Stop()
	os.Exit(code)
}

func firstEnvtestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
