package cloudprovider

import (
	"context"
	"fmt"

	cloudaws "github.com/openshift/karpenter-operator/pkg/cloudprovider/aws"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"
)

// GetCloudProvider returns the CloudProvider for the cluster's platform.
func GetCloudProvider(ctx context.Context, infra common.InfrastructureInfo) (common.CloudProvider, error) {
	switch infra.PlatformType {
	case configv1.AWSPlatformType:
		return cloudaws.New(ctx, infra)
	default:
		return nil, fmt.Errorf("unsupported platform type: %s", infra.PlatformType)
	}
}
