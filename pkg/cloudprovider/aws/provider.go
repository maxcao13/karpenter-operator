package aws

import (
	"context"
	"fmt"
	"os"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

type EC2API interface {
	DescribeInstances(ctx context.Context, params *ec2.DescribeInstancesInput, optFns ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
}

const karpenterImageEnvName = "KARPENTER_IMAGE_AWS"

type Provider struct {
	region          string
	infraName       string
	clusterEndpoint string
	karpenterImage  string
	ec2Client       EC2API
	infra           common.InfrastructureInfo
	ncReconciler    common.NodeClassReconciler
}

func New(ctx context.Context, infra common.InfrastructureInfo) (*Provider, error) {
	if infra.Region == "" {
		return nil, fmt.Errorf("AWS region not available in Infrastructure CR")
	}

	karpenterImage := os.Getenv(karpenterImageEnvName)
	if karpenterImage == "" {
		return nil, fmt.Errorf("%s not set", karpenterImageEnvName)
	}

	if os.Getenv("AWS_SHARED_CREDENTIALS_FILE") == "" {
		return nil, fmt.Errorf("AWS_SHARED_CREDENTIALS_FILE not set")
	}

	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(infra.Region))
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	switch infra.TopologyMode {
	case configv1.ExternalTopologyMode:
		return nil, fmt.Errorf("external topology mode not supported yet")
	case configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode:
		return &Provider{
			region:          infra.Region,
			infraName:       infra.InfraName,
			clusterEndpoint: infra.ClusterEndpoint,
			karpenterImage:  karpenterImage,
			ec2Client:       ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.Region = infra.Region }),
			infra:           infra,
			ncReconciler:    NewOCPNodeClassReconciler(),
		}, nil
	default:
		return nil, fmt.Errorf("unknown/unsupported topology mode: %s", infra.TopologyMode)
	}
}

func (p *Provider) NodeClass() common.NodeClassReconciler {
	return p.ncReconciler
}
