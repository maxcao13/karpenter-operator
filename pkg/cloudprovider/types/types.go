package types

import (
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
)

// OperandCloudConfig holds cloud-specific pieces that the deployment
// reconciler injects into the karpenter operand.
type OperandCloudConfig struct {
	// CredentialsSecretName is the name of the Secret (in the operator namespace)
	// that CCO provisions for the operand. The deployment reconciler waits for
	// this secret to exist before creating the operand Deployment.
	CredentialsSecretName string
	Env                   []corev1.EnvVar
	RBACRules             []rbacv1.PolicyRule
	Volumes               []corev1.Volume
	VolumeMounts          []corev1.VolumeMount
}
