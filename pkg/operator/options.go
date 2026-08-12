package operator

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers"
)

// Environment variables consumed by the operator process at startup.
const (
	// ReleaseVersionEnvName is the OpenShift release version string injected by CVO.
	// Used to report the operator's version in ClusterOperator status.
	// Optional. Only used for ClusterOperator on standalone OCP.
	ReleaseVersionEnvName = "RELEASE_VERSION"

	// ClusterNameEnvName overrides the cluster name normally discovered from the
	// Infrastructure CR. Passed through to the operand as CLUSTER_NAME.
	// Required in management cluster mode.
	ClusterNameEnvName = "CLUSTER_NAME"

	// ClusterEndpointEnvName overrides the internal API server endpoint normally
	// discovered from the Infrastructure CR. Passed through to the operand as CLUSTER_ENDPOINT.
	// Required in management cluster mode.
	ClusterEndpointEnvName = "CLUSTER_ENDPOINT"

	// PlatformEnvName selects the cloud provider (e.g. "AWS"). In standalone mode this
	// is discovered from the Infrastructure CR. Required in management cluster mode.
	PlatformEnvName = "PLATFORM"

	// RegionEnvName is the cloud provider region (e.g. "us-east-1"). In standalone mode
	// this is discovered from the Infrastructure CR. Required in management cluster mode.
	RegionEnvName = "REGION"

	// ManagementClusterEnvName, when "true", tells the operator it is running on a
	// HyperShift management cluster rather than the cluster it manages Karpenter for.
	ManagementClusterEnvName = "MANAGEMENT_CLUSTER"
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
	// Platform selects the cloud provider (e.g. "AWS"), read from PLATFORM env var.
	Platform string
	// Region is the cloud provider region (e.g. "us-east-1"), read from REGION env var.
	Region string

	// TargetKubeconfig is set via --target-kubeconfig. Required when ManagementCluster
	// is true; points at the hosted cluster where Karpenter CRDs live.
	TargetKubeconfig string

	// ManagementCluster is read from the MANAGEMENT_CLUSTER env var. When true, the
	// operator assumes it is running on a HyperShift management cluster and is allowed to
	// execute HyperShift specific logic.
	ManagementCluster bool

	MetricsAddr string
	ProbeAddr   string
	LeaderElect bool
}

// LoadEnv populates fields that are sourced exclusively from environment variables.
func (o *Options) LoadEnv() error {
	o.ReleaseVersion = os.Getenv(ReleaseVersionEnvName)
	o.ClusterName = os.Getenv(ClusterNameEnvName)
	o.ClusterEndpoint = os.Getenv(ClusterEndpointEnvName)
	o.Platform = os.Getenv(PlatformEnvName)
	o.Region = os.Getenv(RegionEnvName)

	var err error
	o.ManagementCluster, err = ParseManagementClusterEnv()
	if err != nil {
		return err
	}
	return nil
}

// ResolveControllerConfig builds the controller configuration from resolved
// infrastructure and the operator's own settings.
func (o *Options) ResolveControllerConfig(infra common.InfrastructureInfo, provider common.CloudProvider) *controllers.Config {
	return &controllers.Config{
		Namespace:         o.Namespace,
		KarpenterImage:    provider.KarpenterImage(),
		ClusterName:       infra.InfraName,
		ClusterEndpoint:   infra.ClusterEndpoint,
		ReleaseVersion:    o.ReleaseVersion,
		ManagementCluster: o.ManagementCluster,
		CloudProvider:     provider,
	}
}

// Validate checks that required pre-Infrastructure-discovery fields are set.
func (o *Options) Validate() error {
	var missing []string
	if o.Namespace == "" {
		missing = append(missing, "--namespace")
	}
	if o.ManagementCluster {
		if o.TargetKubeconfig == "" {
			missing = append(missing, "--target-kubeconfig")
		}
		if o.ClusterName == "" {
			missing = append(missing, ClusterNameEnvName)
		}
		if o.ClusterEndpoint == "" {
			missing = append(missing, ClusterEndpointEnvName)
		}
		if o.Platform == "" {
			missing = append(missing, PlatformEnvName)
		}
		if o.Region == "" {
			missing = append(missing, RegionEnvName)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required configuration not set: %s", strings.Join(missing, ", "))
	}
	return nil
}

// ParseManagementClusterEnv reads MANAGEMENT_CLUSTER from the environment.
// Returns false if unset or empty, the parsed bool if valid, or an error if
// the value is non-empty but not a valid bool.
func ParseManagementClusterEnv() (bool, error) {
	v := os.Getenv(ManagementClusterEnvName)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s value %q: %w", ManagementClusterEnvName, v, err)
	}
	return b, nil
}
