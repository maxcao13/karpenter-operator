package azure

import (
	"testing"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"
)

func azureTestInfra() common.InfrastructureInfo {
	return common.InfrastructureInfo{
		PlatformType: configv1.AzurePlatformType,
		Region:       "eastus",
		InfraName:    "test-cluster",
	}
}

func setAzureTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv(KarpenterImageEnvName, "quay.io/example/karpenter-provider-azure:latest")
	t.Setenv(AzureClientIDEnvName, "client-id")
	t.Setenv(AzureTenantIDEnvName, "tenant-id")
	t.Setenv(AzureSubscriptionIDEnvName, "subscription-id")
	t.Setenv(AzureFederatedTokenFileEnvName, "/var/run/secrets/openshift/serviceaccount/token")
	t.Setenv(AzureVNetSubnetIDEnvName, "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/test-rg/providers/Microsoft.Network/virtualNetworks/test-vnet/subnets/test-subnet")
	t.Setenv(AzureNodeResourceGroupEnvName, "test-rg")
}

func TestNewRequiresEnv(t *testing.T) {
	if _, err := New(t.Context(), azureTestInfra()); err == nil {
		t.Fatal("expected error when Azure env vars are unset")
	}
}

func TestNewRequiresRegion(t *testing.T) {
	setAzureTestEnv(t)

	_, err := New(t.Context(), common.InfrastructureInfo{PlatformType: configv1.AzurePlatformType})
	if err == nil {
		t.Fatal("expected error when region is empty")
	}
}

func TestOperandConfig(t *testing.T) {
	setAzureTestEnv(t)

	p, err := New(t.Context(), azureTestInfra())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.KarpenterImage() != "quay.io/example/karpenter-provider-azure:latest" {
		t.Errorf("KarpenterImage = %q", p.KarpenterImage())
	}

	cfg := p.OperandConfig()
	got := map[string]string{}
	for _, e := range cfg.Env {
		got[e.Name] = e.Value
	}
	want := map[string]string{
		common.RegionEnvName:              "eastus",
		AzureLocationEnvName:              "eastus",
		AzureClientIDEnvName:              "client-id",
		AzureTenantIDEnvName:              "tenant-id",
		AzureSubscriptionIDEnvName:        "subscription-id",
		AzureFederatedTokenFileEnvName:    "/var/run/secrets/openshift/serviceaccount/token",
		AzureKubeletBootstrapTokenEnvName: azureKubeletBootstrapTokenPlaceholder,
		AzureSSHPublicKeyEnvName:          azureSSHPublicKeyPlaceholder,
		AzureVNetSubnetIDEnvName:          "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/test-rg/providers/Microsoft.Network/virtualNetworks/test-vnet/subnets/test-subnet",
		AzureNodeResourceGroupEnvName:     "test-rg",
		AzureARMResourceGroupEnvName:      "test-rg",
		AzureProvisionModeEnvName:         "openshift",
	}
	for name, value := range want {
		if got[name] != value {
			t.Errorf("env %s = %q, want %q", name, got[name], value)
		}
	}
	if cfg.CredentialsSecretName != "" {
		t.Errorf("CredentialsSecretName = %q, want empty", cfg.CredentialsSecretName)
	}
	if len(cfg.Volumes) != 0 || len(cfg.VolumeMounts) != 0 {
		t.Errorf("Azure OperandConfig should not mount a credentials secret")
	}
}

func TestCRDsAndRBAC(t *testing.T) {
	setAzureTestEnv(t)

	p, err := New(t.Context(), azureTestInfra())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	crds := p.CRDs()
	if len(crds) != 1 {
		t.Fatalf("CRDs() len = %d, want 1", len(crds))
	}
	if crds[0].Name != "aksnodeclasses.karpenter.azure.com" {
		t.Errorf("CRD name = %q", crds[0].Name)
	}
	if len(p.RBAC().ClusterRoles) != 1 {
		t.Errorf("RBAC ClusterRoles len = %d, want 1", len(p.RBAC().ClusterRoles))
	}
}
