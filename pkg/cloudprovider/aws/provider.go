package aws

import (
	"context"
	"fmt"
	"os"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// Provider implements cloud.CloudProvider for AWS.
type Provider struct {
	region          string
	infraName       string
	clusterEndpoint string
	ec2Client       EC2API
}

func New(ctx context.Context, platformStatus *configv1.PlatformStatus, infraName, clusterEndpoint string) (*Provider, error) {
	region := ""
	if platformStatus.AWS != nil {
		region = platformStatus.AWS.Region
	}
	if region == "" {
		return nil, fmt.Errorf("AWS region not available in Infrastructure CR")
	}

	if os.Getenv("AWS_SHARED_CREDENTIALS_FILE") == "" {
		return nil, fmt.Errorf("AWS_SHARED_CREDENTIALS_FILE not set")
	}

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return &Provider{
		region:          region,
		infraName:       infraName,
		clusterEndpoint: clusterEndpoint,
		ec2Client:       ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.Region = region }),
	}, nil
}

func (p *Provider) Region() string {
	return p.region
}

func (p *Provider) NodeClassLabel() string {
	return "karpenter.k8s.aws/ec2nodeclass"
}
