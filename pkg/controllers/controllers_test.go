package controllers

import (
	"slices"
	"testing"

	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	configv1 "github.com/openshift/api/config/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/samber/lo"
)

func newFakeManager() *testfake.Manager {
	s := runtime.NewScheme()
	_ = configv1.Install(s)
	_ = apiextensionsv1.AddToScheme(s)
	return &testfake.Manager{
		Cl: fakeclient.NewClientBuilder().WithScheme(s).Build(),
		Ca: &testfake.Cache{},
	}
}

func TestNewControllers(t *testing.T) {
	tests := []struct {
		name              string
		managementCluster bool
		wantControllers   []string
	}{
		{
			name:              "When running in standalone mode it should enable all controllers",
			managementCluster: false,
			wantControllers:   []string{"crd", "karpenter", "clusteroperator"},
		},
		{
			name:              "When running in management cluster mode it should only enable HCP compatible controllers",
			managementCluster: true,
			wantControllers:   []string{"crd, karpenter"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Namespace:         "openshift-karpenter",
				KarpenterImage:    "quay.io/openshift/karpenter:latest",
				ClusterName:       "test-cluster",
				ClusterEndpoint:   "https://api.example.com:6443",
				ReleaseVersion:    "4.23.0",
				ManagementCluster: tc.managementCluster,
				CloudProvider:     &testfake.CloudProvider{Image: "test:latest"},
			}

			controllers := NewControllers(newFakeManager(), cfg)
			names := lo.Map(controllers, func(c Controller, _ int) string {
				return c.Name()
			})

			if !slices.Equal(names, tc.wantControllers) {
				t.Errorf("got controllers %v, want %v", names, tc.wantControllers)
			}
		})
	}
}
