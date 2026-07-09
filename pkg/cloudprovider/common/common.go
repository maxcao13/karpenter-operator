package common

import (
	"context"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// InfrastructureInfo contains cluster infrastructure metadata from the Infrastructure CR.
type InfrastructureInfo struct {
	PlatformType    configv1.PlatformType
	PlatformStatus  configv1.PlatformStatus
	TopologyMode    configv1.TopologyMode
	Region          string
	InfraName       string
	ClusterEndpoint string
}

// NodeClassReconciler is the interface for topology-specific NodeClass reconciliation.
type NodeClassReconciler interface {
	ReconcileNodeClass(ctx context.Context, c client.Client, infraName string, name string) error
	NodeClassObject() client.Object
	AdditionalSources(c cache.Cache) []source.Source
}

// CloudProvider abstracts platform-specific behavior the operator delegates to each implementation.
type CloudProvider interface {
	AddToScheme(s *runtime.Scheme) error
	KarpenterImage() string
	OperandConfig() OperandCloudConfig
	CRDs() []*apiextensionsv1.CustomResourceDefinition
	RBAC() RBACAssets
	RelatedObjects() []configv1.ObjectReference
	NodeClass() NodeClassReconciler
}

// RBACAssets groups all operand RBAC resources (namespace-scoped and cluster-scoped).
type RBACAssets struct {
	Roles               []*rbacv1.Role
	RoleBindings        []*rbacv1.RoleBinding
	ClusterRoles        []*rbacv1.ClusterRole
	ClusterRoleBindings []*rbacv1.ClusterRoleBinding
}

// OperandCloudConfig holds cloud-specific pieces that the deployment
// reconciler injects into the karpenter operand.
type OperandCloudConfig struct {
	// CredentialsSecretName is the name of the Secret (in the operator namespace)
	// that CCO provisions for the operand.
	CredentialsSecretName string
	Env                   []corev1.EnvVar
	Volumes               []corev1.Volume
	VolumeMounts          []corev1.VolumeMount
}
