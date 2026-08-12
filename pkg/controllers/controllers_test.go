package controllers

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"

	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// fakeManager satisfies ctrl.Manager for testing NewControllers. Only GetClient
// is called by the controller constructors.
type fakeManager struct {
	ctrl.Manager
	cl client.Client
}

func (f *fakeManager) GetClient() client.Client {
	return f.cl
}

func newFakeManager() *fakeManager {
	s := runtime.NewScheme()
	_ = configv1.Install(s)
	_ = apiextensionsv1.AddToScheme(s)
	return &fakeManager{
		cl: fakeclient.NewClientBuilder().WithScheme(s).Build(),
	}
}

func TestNewControllers_StandaloneMode(t *testing.T) {
	cfg := &Config{
		Namespace:         "openshift-karpenter",
		KarpenterImage:    "registry.example.com/karpenter:latest",
		ClusterName:       "test-cluster",
		ClusterEndpoint:   "https://api.example.com:6443",
		ReleaseVersion:    "4.23.0",
		ManagementCluster: false,
		CloudProvider:     &testfake.CloudProvider{Image: "test:latest"},
	}

	controllers := NewControllers(newFakeManager(), cfg)

	if len(controllers) != 3 {
		t.Errorf("standalone mode: got %d controllers, want 3", len(controllers))
	}
}

func TestNewControllers_ManagementClusterMode(t *testing.T) {
	cfg := &Config{
		Namespace:         "openshift-karpenter",
		KarpenterImage:    "registry.example.com/karpenter:latest",
		ClusterName:       "test-cluster",
		ClusterEndpoint:   "https://api.example.com:6443",
		ReleaseVersion:    "4.23.0",
		ManagementCluster: true,
		CloudProvider:     &testfake.CloudProvider{Image: "test:latest"},
	}

	controllers := NewControllers(newFakeManager(), cfg)

	if len(controllers) != 0 {
		t.Errorf("management cluster mode: got %d controllers, want 0", len(controllers))
	}
}
