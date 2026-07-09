package aws

import (
	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

const (
	defaultEC2NodeClassName = "default"

	rhcosDeviceName            = "/dev/xvda"
	defaultRootVolumeSize      = "120Gi"
	defaultRootVolumeType      = "gp3"
	defaultRootVolumeEncrypted = true
)

func defaultBlockDeviceMappings() []*awskarpenterv1.BlockDeviceMapping {
	return []*awskarpenterv1.BlockDeviceMapping{
		{
			DeviceName: ptr.To(rhcosDeviceName),
			EBS: &awskarpenterv1.BlockDevice{
				VolumeSize: ptr.To(resource.MustParse(defaultRootVolumeSize)),
				VolumeType: ptr.To(defaultRootVolumeType),
				Encrypted:  ptr.To(defaultRootVolumeEncrypted),
			},
		},
	}
}
