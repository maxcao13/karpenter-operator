package aws

import (
	configv1 "github.com/openshift/api/config/v1"
	awskarpenterapis "github.com/aws/karpenter-provider-aws/pkg/apis"
	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/types"
)

func (p *Provider) AddToScheme(s *runtime.Scheme) error {
	awsKarpenterGV := schema.GroupVersion{Group: awskarpenterapis.Group, Version: "v1"}
	metav1.AddToGroupVersion(s, awsKarpenterGV)
	s.AddKnownTypes(awsKarpenterGV, &awskarpenterv1.EC2NodeClass{}, &awskarpenterv1.EC2NodeClassList{})
	return nil
}

const operandCredentialsSecret = "karpenter-cloud-credentials"

func (p *Provider) OperandConfig() types.OperandCloudConfig {
	return types.OperandCloudConfig{
		CredentialsSecretName: operandCredentialsSecret,
		Env: []corev1.EnvVar{
			{Name: "AWS_REGION", Value: p.region},
			{Name: "AWS_SHARED_CREDENTIALS_FILE", Value: "/etc/provider/credentials"},
			{Name: "AWS_SDK_LOAD_CONFIG", Value: "true"},
		},
		RBACRules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"karpenter.k8s.aws"},
				Resources: []string{"ec2nodeclasses"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"karpenter.k8s.aws"},
				Resources: []string{"ec2nodeclasses", "ec2nodeclasses/status"},
				Verbs:     []string{"patch", "update"},
			},
		},
		Volumes: []corev1.Volume{
			{
				Name: "provider-creds",
				VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: operandCredentialsSecret,
				},
				},
			},
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "provider-creds",
				MountPath: "/etc/provider",
				ReadOnly:  true,
			},
		},
	}
}

func (p *Provider) RelatedObjects() []configv1.ObjectReference {
	return []configv1.ObjectReference{
		{Group: "karpenter.k8s.aws", Resource: "ec2nodeclasses"},
	}
}
