package operator

import (
	"fmt"
	"os"
	"strings"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers"
)

const (
	ReleaseVersionEnvName  = "RELEASE_VERSION"
	ClusterNameEnvName     = "CLUSTER_NAME"
	ClusterEndpointEnvName = "CLUSTER_ENDPOINT"
)

type Options struct {
	// Namespace is set via --namespace flag.
	Namespace string
	// ReleaseVersion is read from RELEASE_VERSION env var.
	ReleaseVersion string

	// ClusterName is read from CLUSTER_NAME env var, or discovered from Infrastructure CR.
	ClusterName string
	// ClusterEndpoint is read from CLUSTER_ENDPOINT env var, or discovered from Infrastructure CR.
	ClusterEndpoint string

	MetricsAddr string
	ProbeAddr   string
	LeaderElect bool
}

// LoadEnv populates fields that are sourced exclusively from environment variables.
func (o *Options) LoadEnv() {
	o.ReleaseVersion = os.Getenv(ReleaseVersionEnvName)
	o.ClusterName = os.Getenv(ClusterNameEnvName)
	o.ClusterEndpoint = os.Getenv(ClusterEndpointEnvName)
}

// ResolveControllerConfig merges env var overrides with infrastructure defaults
// and returns a fully resolved controllers.Config.
func (o *Options) ResolveControllerConfig(infra common.InfrastructureInfo, provider common.CloudProvider) *controllers.Config {
	clusterName := o.ClusterName
	if clusterName == "" {
		clusterName = infra.InfraName
	}
	clusterEndpoint := o.ClusterEndpoint
	if clusterEndpoint == "" {
		clusterEndpoint = infra.ClusterEndpoint
	}

	return &controllers.Config{
		Namespace:       o.Namespace,
		KarpenterImage:  provider.KarpenterImage(),
		ClusterName:     clusterName,
		ClusterEndpoint: clusterEndpoint,
		ReleaseVersion:  o.ReleaseVersion,
		CloudProvider:   provider,
	}
}

// Validate checks that required pre-Infrastructure-discovery fields are set.
// ClusterName is NOT validated here — it is discovered from the Infrastructure
// CR in Run() if not set via env vars.
func (o *Options) Validate() error {
	var missing []string
	if o.Namespace == "" {
		missing = append(missing, "--namespace")
	}
	if o.ReleaseVersion == "" {
		missing = append(missing, ReleaseVersionEnvName)
	}
	if len(missing) > 0 {
		return fmt.Errorf("required configuration not set: %s", strings.Join(missing, ", "))
	}
	return nil
}
