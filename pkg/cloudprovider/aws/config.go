package aws

import (
	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"

	awskarpenterapis "github.com/aws/karpenter-provider-aws/pkg/apis"
	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const operandCredentialsSecret = "karpenter-cloud-credentials"

func (p *Provider) AddToScheme(s *runtime.Scheme) error {
	awsKarpenterGV := schema.GroupVersion{Group: awskarpenterapis.Group, Version: "v1"}
	metav1.AddToGroupVersion(s, awsKarpenterGV)
	s.AddKnownTypes(awsKarpenterGV, &awskarpenterv1.EC2NodeClass{}, &awskarpenterv1.EC2NodeClassList{})
	return nil
}

func (p *Provider) KarpenterImage() string {
	return p.karpenterImage
}

func (p *Provider) OperandConfig() common.OperandCloudConfig {
	return common.OperandCloudConfig{
		CredentialsSecretName: operandCredentialsSecret,
		Env: []corev1.EnvVar{
			{Name: "AWS_REGION", Value: p.region},
			{Name: "AWS_SHARED_CREDENTIALS_FILE", Value: "/etc/provider/credentials"},
			{Name: "AWS_SDK_LOAD_CONFIG", Value: "true"},
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

func (p *Provider) CRDs() []*apiextensionsv1.CustomResourceDefinition {
	return assets.AWSCRDs
}

func (p *Provider) RBAC() common.RBACAssets {
	return assets.AWSRBACAssets
}

func (p *Provider) RelatedObjects() []configv1.ObjectReference {
	return []configv1.ObjectReference{
		{Group: awskarpenterapis.Group, Resource: "ec2nodeclasses"},
	}
}
