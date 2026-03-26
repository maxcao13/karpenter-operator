package cloudprovider

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	certificatesv1 "k8s.io/api/certificates/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	cloudaws "github.com/openshift/karpenter-operator/pkg/cloudprovider/aws"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/types"
)

// CloudProvider encapsulates all cloud-specific behavior the operator needs.
// Generic controllers interact with the cloud exclusively through this interface.
type CloudProvider interface {
	// AddToScheme registers provider-specific CRD types (e.g. EC2NodeClass).
	AddToScheme(s *runtime.Scheme) error

	// ReconcileDefaultNodeClass creates/updates the default provider-specific
	// NodeClass with infrastructure defaults discovered from the cluster.
	ReconcileDefaultNodeClass(ctx context.Context, c client.Client, infraName string) error

	// NodeClassObject returns a zero-value instance of the provider-specific
	// NodeClass type, used to set up cache watches.
	NodeClassObject() client.Object

	// GetInstanceDNSNames returns DNS names of cloud instances matching the
	// given NodeClaims, used for CSR verification.
	GetInstanceDNSNames(ctx context.Context, nodeClaims []karpenterv1.NodeClaim) ([]string, error)

	// AuthorizeCSR verifies that a pending CSR was issued by a real cloud
	// instance owned by Karpenter.
	AuthorizeCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, c client.Client) (bool, error)

	// OperandConfig returns cloud-specific deployment configuration for the
	// karpenter operand: env vars, RBAC rules, volumes, and volume mounts.
	OperandConfig() types.OperandCloudConfig

	// RelatedObjects returns cloud-specific ObjectReferences for the
	// ClusterOperator status.
	RelatedObjects() []configv1.ObjectReference

	// Region returns the discovered cloud region string.
	Region() string

	// NodeClassLabel returns the label key that Karpenter applies to nodes
	// indicating which NodeClass they belong to (e.g. "karpenter.k8s.aws/ec2nodeclass").
	NodeClassLabel() string
}

// GetCloudProvider returns the CloudProvider for the cluster's platform.
// The Infrastructure CR's status.platformStatus.type drives the selection.
func GetCloudProvider(ctx context.Context, platformStatus *configv1.PlatformStatus, infraName, clusterEndpoint string) (CloudProvider, error) {
	if platformStatus == nil {
		return nil, fmt.Errorf("Infrastructure status.platformStatus is nil")
	}

	switch platformStatus.Type {
	case configv1.AWSPlatformType:
		return cloudaws.New(ctx, platformStatus, infraName, clusterEndpoint)
	default:
		return nil, fmt.Errorf("unsupported platform type: %s", platformStatus.Type)
	}
}
