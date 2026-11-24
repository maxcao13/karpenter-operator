package azurenodeclass

import (
	"cmp"
	"fmt"
	"math"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	openshiftkarpenterv1alpha1 "github.com/openshift/karpenter-operator/api/karpenter/v1alpha1"
	"github.com/openshift/karpenter-operator/pkg/hypershift"

	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
	"github.com/samber/lo"
)

// defaultOSDiskSizeGiB matches the OS disk size of HyperShift NodePools created with the HCP CLI.
const defaultOSDiskSizeGiB int32 = 120

const (
	marketplacePublisherAnnotation = "hypershift.openshift.io/azure-marketplace-publisher"
	marketplaceOfferAnnotation     = "hypershift.openshift.io/azure-marketplace-offer"
	marketplaceSKUAnnotation       = "hypershift.openshift.io/azure-marketplace-sku"
	marketplaceVersionAnnotation   = "hypershift.openshift.io/azure-marketplace-version"
)

// aksNodeClassSpec returns the AKSNodeClass spec for an OpenShiftAzureNodeClass.
// The Marketplace image and user data are left unset; they come from the ignition pipeline.
func aksNodeClassSpec(spec openshiftkarpenterv1alpha1.OpenShiftAzureNodeClassSpec) (azurekarpenterv1beta1.AKSNodeClassSpec, error) {
	kubelet, err := aksKubeletConfiguration(spec.Kubelet)
	if err != nil {
		return azurekarpenterv1beta1.AKSNodeClassSpec{}, err
	}
	return azurekarpenterv1beta1.AKSNodeClassSpec{
		VNETSubnetID: lo.EmptyableToPtr(spec.VNETSubnetID),
		// The image family doesn't select the image in OpenShift mode. It's set to the CRD default because it's part
		// of the drift hash: if it were unset, every update would store the current CRD default, and a changed
		// default would drift all nodes.
		ImageFamily:     new(azurekarpenterv1beta1.UbuntuImageFamily),
		WorkloadRuntime: new(azurekarpenterv1beta1.WorkloadRuntimeOCIContainer),
		OSDiskSizeGB:    new(cmp.Or(spec.OSDiskSizeGiB, defaultOSDiskSizeGiB)),
		Tags:            spec.Tags,
		MaxPods:         lo.EmptyableToPtr(spec.Kubelet.MaxPods),
		Kubelet:         kubelet,
		Security: &azurekarpenterv1beta1.Security{
			EncryptionAtHost: new(spec.Security.Encryption.HostEncryption == openshiftkarpenterv1alpha1.AzureHostEncryptionEnabled),
		},
	}, nil
}

func aksKubeletConfiguration(kubelet openshiftkarpenterv1.KubeletConfiguration) (*azurekarpenterv1beta1.KubeletConfiguration, error) {
	reserved, err := aksKubeReserved(kubelet.KubeReserved)
	if err != nil {
		return nil, err
	}
	eviction := azurekarpenterv1beta1.EvictionThreshold{
		MemoryAvailable: lo.EmptyableToPtr(string(kubelet.EvictionHard["memory.available"])),
		NodeFsAvailable: lo.EmptyableToPtr(string(kubelet.EvictionHard["nodefs.available"])),
	}
	if reserved == nil && eviction.MemoryAvailable == nil && eviction.NodeFsAvailable == nil {
		return nil, nil
	}
	config := &azurekarpenterv1beta1.KubeletConfiguration{KubeReserved: reserved}
	if eviction.MemoryAvailable != nil || eviction.NodeFsAvailable != nil {
		config.EvictionHard = &eviction
	}
	return config, nil
}

func aksKubeReserved(kubeReserved map[string]string) (*azurekarpenterv1beta1.KubeReserved, error) {
	var reserved azurekarpenterv1beta1.KubeReserved
	if value, ok := kubeReserved["cpu"]; ok {
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("parsing kubeReserved cpu: %w", err)
		}
		if quantity.Sign() < 0 || quantity.Cmp(*resource.NewMilliQuantity(math.MaxInt32, resource.DecimalSI)) > 0 {
			return nil, fmt.Errorf("kubeReserved cpu %q is outside the Azure millicore range", value)
		}
		reserved.CPUMillicores = new(int32(quantity.MilliValue())) //nolint:gosec // Quantity is bounded to [0, MaxInt32] millicores above.
	}
	if value, ok := kubeReserved["memory"]; ok {
		quantity, err := resource.ParseQuantity(value)
		if err != nil {
			return nil, fmt.Errorf("parsing kubeReserved memory: %w", err)
		}
		const bytesPerMiB = 1024 * 1024
		if quantity.Sign() < 0 || quantity.Cmp(*resource.NewQuantity(math.MaxInt32*bytesPerMiB, resource.BinarySI)) > 0 {
			return nil, fmt.Errorf("kubeReserved memory %q is outside the Azure MiB range", value)
		}
		// Round up to whole MiB so the reservation isn't reduced during conversion.
		reserved.MemoryMB = new(int32((quantity.Value() + bytesPerMiB - 1) / bytesPerMiB)) //nolint:gosec // Quantity is bounded to [0, MaxInt32] MiB above.
	}
	if reserved.CPUMillicores == nil && reserved.MemoryMB == nil {
		return nil, nil
	}
	return &reserved, nil
}

// reconcileAKSNodeClassRelease adds the image and user data from the user data Secret to the desired spec.
// Unpinned NodeClasses keep their current values while the control plane upgrades.
func reconcileAKSNodeClassRelease(desired *azurekarpenterv1beta1.AKSNodeClassSpec, current azurekarpenterv1beta1.AKSNodeClassSpec, nodeClass *openshiftkarpenterv1alpha1.OpenShiftAzureNodeClass, hcp *hyperv1beta1.HostedControlPlane, userDataSecret *corev1.Secret) error {
	// Unpinned NodeClasses keep their current userData and image while the control plane upgrades.
	// This also holds back rotated ignition tokens, which is fine: a token is valid for 11h and rotated
	// after half of that, so a replaced token stays valid for another 5.5h.
	if hypershift.WaitsForControlPlaneUpgrade(hcp, nodeClass.Spec.Version) && current.UserData != nil && current.MarketplaceImage != nil {
		desired.UserData = current.UserData
		desired.MarketplaceImage = current.MarketplaceImage
		return nil
	}

	marketplaceImage, err := marketplaceImageFromUserDataSecret(userDataSecret)
	if err != nil {
		return err
	}
	desired.UserData = new(string(userDataSecret.Data["value"]))
	desired.MarketplaceImage = marketplaceImage
	return nil
}

// marketplaceImageFromUserDataSecret returns the Azure Marketplace image from the annotations of the user data Secret.
// Only the unsuffixed amd64 keys are read; further image metadata changes are tracked in AUTOSCALE-1084.
func marketplaceImageFromUserDataSecret(secret *corev1.Secret) (*azurekarpenterv1beta1.MarketplaceImage, error) {
	for _, annotation := range []string{
		marketplacePublisherAnnotation,
		marketplaceOfferAnnotation,
		marketplaceSKUAnnotation,
		marketplaceVersionAnnotation,
	} {
		if secret.Annotations[annotation] == "" {
			return nil, fmt.Errorf("user data secret %s/%s has missing or empty annotation %q", secret.Namespace, secret.Name, annotation)
		}
	}
	return &azurekarpenterv1beta1.MarketplaceImage{
		Publisher: secret.Annotations[marketplacePublisherAnnotation],
		Offer:     secret.Annotations[marketplaceOfferAnnotation],
		SKU:       secret.Annotations[marketplaceSKUAnnotation],
		Version:   secret.Annotations[marketplaceVersionAnnotation],
	}, nil
}
