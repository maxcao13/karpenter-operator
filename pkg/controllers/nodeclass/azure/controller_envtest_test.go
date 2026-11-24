//go:build envtest

package azurenodeclass

import (
	"encoding/json"
	"slices"
	"testing"

	. "github.com/onsi/gomega"

	openshiftkarpenterv1 "github.com/openshift/karpenter-operator/api/karpenter/v1"
	"github.com/openshift/karpenter-operator/pkg/assets"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/uuid"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	azurekarpenterv1beta1 "github.com/Azure/karpenter-provider-azure/pkg/apis/v1beta1"
)

func TestAKSNodeClassReconcileDefaults(t *testing.T) {
	g := NewWithT(t)
	index := slices.IndexFunc(assets.AzureCRDs, func(crd *apiextensionsv1.CustomResourceDefinition) bool {
		return crd.Name == "aksnodeclasses.karpenter.azure.com"
	})
	g.Expect(index).To(BeNumerically(">=", 0))
	crd := assets.AzureCRDs[index].DeepCopy()
	var kubeletProperties map[string]apiextensionsv1.JSONSchemaProps
	for _, version := range crd.Spec.Versions {
		if version.Name == azurekarpenterv1beta1.SchemeGroupVersion.Version {
			kubeletProperties = version.Schema.OpenAPIV3Schema.Properties["spec"].Properties["kubelet"].Properties
		}
	}
	g.Expect(kubeletProperties).NotTo(BeEmpty())
	testEnv := &envtest.Environment{CRDs: []*apiextensionsv1.CustomResourceDefinition{crd}}
	config, err := testEnv.Start()
	g.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { NewWithT(t).Expect(testEnv.Stop()).To(Succeed()) })
	hostedClient, err := client.New(config, client.Options{Scheme: nodeClassTestScheme()})
	g.Expect(err).NotTo(HaveOccurred())
	c := &NodeClassController{hostedClient: hostedClient}

	configured := openshiftkarpenterv1.KubeletConfiguration{
		MaxPods:      110,
		KubeReserved: map[string]string{"cpu": "500m", "memory": "1Gi"},
		EvictionHard: map[string]openshiftkarpenterv1.EvictionThreshold{"memory.available": "200Mi", "nodefs.available": "10%"},
	}
	tests := map[string]struct {
		initial              openshiftkarpenterv1.KubeletConfiguration
		updated              *openshiftkarpenterv1.KubeletConfiguration
		legacy               bool
		removeBeforeAdoption bool
		wantReserved         *azurekarpenterv1beta1.KubeReserved
		wantEviction         *azurekarpenterv1beta1.EvictionThreshold
		tags                 map[string]string
		wantTags             map[string]string
		wantUnsafeSysctls    []string
	}{
		"When kubelet is unset, it should reconcile without repeated updates": {
			wantReserved: nil,
		},
		"When kubelet is configured, it should retain API defaults without repeated updates": {
			initial:      configured,
			wantReserved: &azurekarpenterv1beta1.KubeReserved{CPUMillicores: new(int32(500)), MemoryMB: new(int32(1024))},
			wantEviction: &azurekarpenterv1beta1.EvictionThreshold{MemoryAvailable: new("200Mi"), NodeFsAvailable: new("10%")},
		},
		"When CPU reservation is removed, it should clear CPU without removing memory or API defaults": {
			initial:      configured,
			updated:      &openshiftkarpenterv1.KubeletConfiguration{KubeReserved: map[string]string{"memory": "1Gi"}},
			wantReserved: &azurekarpenterv1beta1.KubeReserved{MemoryMB: new(int32(1024))},
		},
		"When memory reservation is removed, it should clear memory without removing CPU or API defaults": {
			initial:      configured,
			updated:      &openshiftkarpenterv1.KubeletConfiguration{KubeReserved: map[string]string{"cpu": "500m"}},
			wantReserved: &azurekarpenterv1beta1.KubeReserved{CPUMillicores: new(int32(500))},
		},
		"When memory eviction is removed, it should clear memory eviction without removing nodefs eviction": {
			initial:      configured,
			updated:      &openshiftkarpenterv1.KubeletConfiguration{EvictionHard: map[string]openshiftkarpenterv1.EvictionThreshold{"nodefs.available": "10%"}},
			wantEviction: &azurekarpenterv1beta1.EvictionThreshold{NodeFsAvailable: new("10%")},
		},
		"When nodefs eviction is removed, it should clear nodefs eviction without removing memory eviction": {
			initial:      configured,
			updated:      &openshiftkarpenterv1.KubeletConfiguration{EvictionHard: map[string]openshiftkarpenterv1.EvictionThreshold{"memory.available": "200Mi"}},
			wantEviction: &azurekarpenterv1beta1.EvictionThreshold{MemoryAvailable: new("200Mi")},
		},
		"When all kubelet settings are removed, it should clear the projected fields": {
			initial:      configured,
			updated:      &openshiftkarpenterv1.KubeletConfiguration{},
			wantReserved: nil,
		},
		"When an update-managed object is adopted, it should clear subsequently removed settings": {
			initial:      configured,
			updated:      &openshiftkarpenterv1.KubeletConfiguration{},
			legacy:       true,
			wantReserved: nil,
		},
		"When settings are removed before reconciling an existing object, it should clear them on the first update": {
			initial:              configured,
			updated:              &openshiftkarpenterv1.KubeletConfiguration{},
			legacy:               true,
			removeBeforeAdoption: true,
			wantReserved:         nil,
		},
		"When a tag is removed from an update-managed object, it should clear only that tag": {
			tags:     map[string]string{"cost-center": "platform", "team": "autoscaling"},
			updated:  &openshiftkarpenterv1.KubeletConfiguration{},
			legacy:   true,
			wantTags: map[string]string{"cost-center": "platform"},
		},
		"When all tags are removed from an update-managed object, it should clear them": {
			tags:     map[string]string{"cost-center": "platform"},
			updated:  &openshiftkarpenterv1.KubeletConfiguration{},
			legacy:   true,
			wantTags: nil,
		},
		"When unrelated kubelet settings already exist, it should preserve them after clearing projected settings": {
			initial:           configured,
			updated:           &openshiftkarpenterv1.KubeletConfiguration{},
			legacy:            true,
			wantUnsafeSysctls: []string{"net.core.somaxconn"},
		},
		"When CPU is removed from an update-managed object, it should preserve memory and unrelated defaults": {
			initial:      configured,
			updated:      &openshiftkarpenterv1.KubeletConfiguration{KubeReserved: map[string]string{"memory": "1Gi"}},
			legacy:       true,
			wantReserved: &azurekarpenterv1beta1.KubeReserved{MemoryMB: new(int32(1024))},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			g := NewWithT(t)
			nodeClass := newNodeClass()
			nodeClass.Name = "test-" + string(uuid.NewUUID())
			nodeClass.Spec.Kubelet = tc.initial
			nodeClass.Spec.Tags = tc.tags
			secret := newUserDataSecret(userData421, marketplaceImage421)
			if tc.legacy {
				// Seed an object managed by Create/Update, including server-defaulted fields.
				spec, err := aksNodeClassSpec(nodeClass.Spec)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(reconcileAKSNodeClassRelease(&spec, azurekarpenterv1beta1.AKSNodeClassSpec{}, nodeClass, stableHCP(), secret)).To(Succeed())
				legacy := &azurekarpenterv1beta1.AKSNodeClass{}
				legacy.Name = nodeClass.Name
				legacy.Spec = spec
				if legacy.Spec.Kubelet != nil {
					legacy.Spec.Kubelet.AllowedUnsafeSysctls = tc.wantUnsafeSysctls
				}
				g.Expect(hostedClient.Create(t.Context(), legacy)).To(Succeed())
				g.Expect(hostedClient.Update(t.Context(), legacy)).To(Succeed())
			}
			if tc.removeBeforeAdoption {
				nodeClass.Spec.Kubelet = *tc.updated
				tc.updated = nil
			}
			got, err := c.reconcileAKSNodeClass(t.Context(), nodeClass, stableHCP(), secret)
			g.Expect(err).NotTo(HaveOccurred())
			if tc.updated != nil {
				nodeClass.Spec.Kubelet = *tc.updated
				nodeClass.Spec.Tags = tc.wantTags
				got, err = c.reconcileAKSNodeClass(t.Context(), nodeClass, stableHCP(), secret)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(got.Spec.MaxPods).To(Equal((*int32)(nil)))
			}
			var reserved *azurekarpenterv1beta1.KubeReserved
			var eviction *azurekarpenterv1beta1.EvictionThreshold
			if got.Spec.Kubelet != nil {
				reserved, eviction = got.Spec.Kubelet.KubeReserved, got.Spec.Kubelet.EvictionHard
				expectKubeletDefaults(t, got.Spec.Kubelet, kubeletProperties)
				g.Expect(got.Spec.Kubelet.AllowedUnsafeSysctls).To(Equal(tc.wantUnsafeSysctls))
			}
			g.Expect(reserved).To(Equal(tc.wantReserved))
			g.Expect(eviction).To(Equal(tc.wantEviction))
			g.Expect(got.Spec.Tags).To(Equal(tc.wantTags))
			resourceVersion := got.ResourceVersion
			for range 3 {
				repeated, err := c.reconcileAKSNodeClass(t.Context(), nodeClass, stableHCP(), secret)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(repeated.ResourceVersion).To(Equal(resourceVersion))
			}
		})
	}
}

// expectKubeletDefaults derives expectations from the CRD instead of duplicating provider defaults.
func expectKubeletDefaults(t *testing.T, kubelet *azurekarpenterv1beta1.KubeletConfiguration, properties map[string]apiextensionsv1.JSONSchemaProps) {
	t.Helper()
	g := NewWithT(t)
	data, err := json.Marshal(kubelet)
	g.Expect(err).NotTo(HaveOccurred())
	var fields map[string]json.RawMessage
	g.Expect(json.Unmarshal(data, &fields)).To(Succeed())
	for field, property := range properties {
		if property.Default != nil {
			g.Expect(fields[field]).To(MatchJSON(property.Default.Raw), "defaulted kubelet field %s", field)
		}
	}
}
