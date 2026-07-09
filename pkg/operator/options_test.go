package operator

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestLoadEnv(t *testing.T) {
	g := NewWithT(t)
	t.Setenv(ReleaseVersionEnvName, "4.23.0")
	t.Setenv(ClusterNameEnvName, "my-cluster")
	t.Setenv(ClusterEndpointEnvName, "https://api-int.example.com:6443")

	var opts Options
	opts.LoadEnv()

	g.Expect(opts.ReleaseVersion).To(Equal("4.23.0"))
	g.Expect(opts.ClusterName).To(Equal("my-cluster"))
	g.Expect(opts.ClusterEndpoint).To(Equal("https://api-int.example.com:6443"))
}

func TestLoadEnv_Empty(t *testing.T) {
	g := NewWithT(t)
	t.Setenv(ReleaseVersionEnvName, "")
	t.Setenv(ClusterNameEnvName, "")
	t.Setenv(ClusterEndpointEnvName, "")

	var opts Options
	opts.LoadEnv()

	g.Expect(opts.ReleaseVersion).To(BeEmpty())
	g.Expect(opts.ClusterName).To(BeEmpty())
	g.Expect(opts.ClusterEndpoint).To(BeEmpty())
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		opts    Options
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid with all required fields",
			opts: Options{
				Namespace:      "openshift-karpenter",
				ReleaseVersion: "4.23.0",
			},
			wantErr: false,
		},
		{
			name: "missing namespace",
			opts: Options{
				ReleaseVersion: "4.23.0",
			},
			wantErr: true,
			errMsg:  "--namespace",
		},
		{
			name: "missing release version",
			opts: Options{
				Namespace: "openshift-karpenter",
			},
			wantErr: true,
			errMsg:  ReleaseVersionEnvName,
		},
		{
			name:    "missing all",
			opts:    Options{},
			wantErr: true,
			errMsg:  "--namespace",
		},
		{
			name: "cluster name and endpoint are optional",
			opts: Options{
				Namespace:      "openshift-karpenter",
				ReleaseVersion: "4.23.0",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.opts.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr && tt.errMsg != "" {
				if got := err.Error(); !strings.Contains(got, tt.errMsg) {
					t.Errorf("error %q does not contain %q", got, tt.errMsg)
				}
			}
		})
	}
}

func TestResolveControllerConfig(t *testing.T) {
	tests := []struct {
		name               string
		opts               Options
		infra              common.InfrastructureInfo
		expectClusterName  string
		expectEndpoint     string
		expectInfraName    string
		expectNamespace    string
		expectReleaseVer   string
		expectTopologyMode configv1.TopologyMode
	}{
		{
			name: "env vars override infra values",
			opts: Options{
				Namespace:       "openshift-karpenter",
				ReleaseVersion:  "4.23.0",
				ClusterName:     "env-cluster",
				ClusterEndpoint: "https://env-endpoint:6443",
			},
			infra: common.InfrastructureInfo{
				InfraName:       "infra-cluster-abc",
				ClusterEndpoint: "https://infra-endpoint:6443",
				TopologyMode:    configv1.HighlyAvailableTopologyMode,
			},
			expectClusterName:  "env-cluster",
			expectEndpoint:     "https://env-endpoint:6443",
			expectInfraName:    "infra-cluster-abc",
			expectNamespace:    "openshift-karpenter",
			expectReleaseVer:   "4.23.0",
			expectTopologyMode: configv1.HighlyAvailableTopologyMode,
		},
		{
			name: "falls back to infra when env vars empty",
			opts: Options{
				Namespace:      "openshift-karpenter",
				ReleaseVersion: "4.23.0",
			},
			infra: common.InfrastructureInfo{
				InfraName:       "infra-cluster-xyz",
				ClusterEndpoint: "https://api-int.infra.example.com:6443",
				TopologyMode:    configv1.HighlyAvailableTopologyMode,
			},
			expectClusterName:  "infra-cluster-xyz",
			expectEndpoint:     "https://api-int.infra.example.com:6443",
			expectInfraName:    "infra-cluster-xyz",
			expectNamespace:    "openshift-karpenter",
			expectReleaseVer:   "4.23.0",
			expectTopologyMode: configv1.HighlyAvailableTopologyMode,
		},
		{
			name: "partial override: only cluster name from env",
			opts: Options{
				Namespace:      "openshift-karpenter",
				ReleaseVersion: "4.23.0",
				ClusterName:    "override-name",
			},
			infra: common.InfrastructureInfo{
				InfraName:       "infra-abc",
				ClusterEndpoint: "https://api.infra:6443",
				TopologyMode:    configv1.ExternalTopologyMode,
			},
			expectClusterName:  "override-name",
			expectEndpoint:     "https://api.infra:6443",
			expectInfraName:    "infra-abc",
			expectNamespace:    "openshift-karpenter",
			expectReleaseVer:   "4.23.0",
			expectTopologyMode: configv1.ExternalTopologyMode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			provider := &fakeCloudProvider{image: "quay.io/karpenter:latest"}

			cfg := tt.opts.ResolveControllerConfig(tt.infra, provider)

			g.Expect(cfg.ClusterName).To(Equal(tt.expectClusterName))
			g.Expect(cfg.ClusterEndpoint).To(Equal(tt.expectEndpoint))
			g.Expect(cfg.InfraName).To(Equal(tt.expectInfraName))
			g.Expect(cfg.Namespace).To(Equal(tt.expectNamespace))
			g.Expect(cfg.ReleaseVersion).To(Equal(tt.expectReleaseVer))
			g.Expect(cfg.TopologyMode).To(Equal(tt.expectTopologyMode))
			g.Expect(cfg.KarpenterImage).To(Equal("quay.io/karpenter:latest"))
			g.Expect(cfg.CloudProvider).To(Equal(provider))
		})
	}
}

// fakeCloudProvider is a minimal stub that satisfies common.CloudProvider for testing.
type fakeCloudProvider struct {
	image string
}

func (f *fakeCloudProvider) AddToScheme(_ *runtime.Scheme) error { return nil }
func (f *fakeCloudProvider) KarpenterImage() string              { return f.image }
func (f *fakeCloudProvider) OperandConfig() common.OperandCloudConfig {
	return common.OperandCloudConfig{}
}
func (f *fakeCloudProvider) CRDs() []*apiextensionsv1.CustomResourceDefinition { return nil }
func (f *fakeCloudProvider) RBAC() common.RBACAssets                           { return common.RBACAssets{} }
func (f *fakeCloudProvider) RelatedObjects() []configv1.ObjectReference        { return nil }
func (f *fakeCloudProvider) NodeClass() common.NodeClassReconciler             { return nil }
