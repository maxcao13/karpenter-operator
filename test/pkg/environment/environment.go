package environment

import (
	"context"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	testclient "github.com/openshift/karpenter-operator/test/pkg/client"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Environment holds shared state for all test suites. Constructed once in
// BeforeSuite and referenced via a package-level var in each suite.
type Environment struct {
	Client         client.Client
	Infrastructure common.InfrastructureInfo
}

// New creates an Environment by building a client and reading cluster metadata.
func New() (*Environment, error) {
	cl, err := testclient.NewClient()
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}

	infra := &configv1.Infrastructure{}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "cluster"}, infra); err != nil {
		return nil, fmt.Errorf("reading Infrastructure CR: %w", err)
	}

	env := &Environment{
		Client: cl,
		Infrastructure: common.InfrastructureInfo{
			InfraName: infra.Status.InfrastructureName,
		},
	}
	if infra.Status.PlatformStatus != nil {
		env.Infrastructure.PlatformType = infra.Status.PlatformStatus.Type
		env.Infrastructure.PlatformStatus = *infra.Status.PlatformStatus
		if infra.Status.PlatformStatus.AWS != nil {
			env.Infrastructure.Region = infra.Status.PlatformStatus.AWS.Region
		}
	}
	if infra.Status.ControlPlaneTopology != "" {
		env.Infrastructure.TopologyMode = infra.Status.ControlPlaneTopology
	}
	if infra.Status.APIServerInternalURL != "" {
		env.Infrastructure.ClusterEndpoint = infra.Status.APIServerInternalURL
	}

	return env, nil
}

// IsAWSPlatform returns true if the cluster is on AWS.
func (e *Environment) IsAWSPlatform() bool {
	return e.Infrastructure.PlatformType == configv1.AWSPlatformType
}

// IsExternalTopology returns true if the cluster is in external topology mode.
func (e *Environment) IsExternalTopology() bool {
	return e.Infrastructure.TopologyMode == configv1.ExternalTopologyMode
}
