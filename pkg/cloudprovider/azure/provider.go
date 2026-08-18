package azure

import (
	"context"
	"fmt"
	"os"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
)

// Provider holds Azure cluster settings used to configure the karpenter operand.
type Provider struct {
	region             string
	karpenterImage     string
	clientID           string
	tenantID           string
	subscriptionID     string
	federatedTokenFile string
	vnetSubnetID       string
	nodeResourceGroup  string
}

func New(_ context.Context, infra common.InfrastructureInfo) (*Provider, error) {
	if infra.Region == "" {
		return nil, fmt.Errorf("region not available")
	}

	karpenterImage, err := requireEnv(KarpenterImageEnvName)
	if err != nil {
		return nil, err
	}
	clientID, err := requireEnv(AzureClientIDEnvName)
	if err != nil {
		return nil, err
	}
	tenantID, err := requireEnv(AzureTenantIDEnvName)
	if err != nil {
		return nil, err
	}
	subscriptionID, err := requireEnv(AzureSubscriptionIDEnvName)
	if err != nil {
		return nil, err
	}
	federatedTokenFile, err := requireEnv(AzureFederatedTokenFileEnvName)
	if err != nil {
		return nil, err
	}
	vnetSubnetID, err := requireEnv(AzureVNetSubnetIDEnvName)
	if err != nil {
		return nil, err
	}
	nodeResourceGroup, err := requireEnv(AzureNodeResourceGroupEnvName)
	if err != nil {
		return nil, err
	}

	return &Provider{
		region:             infra.Region,
		karpenterImage:     karpenterImage,
		clientID:           clientID,
		tenantID:           tenantID,
		subscriptionID:     subscriptionID,
		federatedTokenFile: federatedTokenFile,
		vnetSubnetID:       vnetSubnetID,
		nodeResourceGroup:  nodeResourceGroup,
	}, nil
}

func requireEnv(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%s not set", name)
	}
	return v, nil
}
