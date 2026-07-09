package operator

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/util"

	configv1 "github.com/openshift/api/config/v1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	operatorNamespace      = "openshift-karpenter"
	clusterOperatorName    = "karpenter"
	operatorDeploymentName = "karpenter-operator"
	operandDeploymentName  = "karpenter"

	pollOneMinute  = 1 * time.Minute
	pollFiveSecond = 5 * time.Second
)

var _ = Describe("Resources", Ordered, func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Context("ClusterOperator", func() {
		BeforeEach(func() {
			if env.IsExternalTopology() {
				Skip("ClusterOperator shouldn't run on hosted control plane topologies")
			}
		})

		It("should be available", func() {
			co := &configv1.ClusterOperator{}
			Eventually(func(g Gomega) {
				g.Expect(env.Client.Get(ctx, types.NamespacedName{Name: clusterOperatorName}, co)).To(Succeed())

				condByType := make(map[configv1.ClusterStatusConditionType]configv1.ConditionStatus)
				for _, c := range co.Status.Conditions {
					condByType[c.Type] = c.Status
				}
				g.Expect(condByType).To(HaveKeyWithValue(configv1.OperatorAvailable, configv1.ConditionTrue))
				g.Expect(condByType).To(HaveKeyWithValue(configv1.OperatorDegraded, configv1.ConditionFalse))
				g.Expect(condByType).To(HaveKeyWithValue(configv1.OperatorProgressing, configv1.ConditionFalse))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})
	})

	Context("Deployments", func() {
		It("karpenter-operator should be ready", func() {
			dep := &appsv1.Deployment{}
			Eventually(func(g Gomega) {
				g.Expect(env.Client.Get(ctx, types.NamespacedName{
					Name:      operatorDeploymentName,
					Namespace: operatorNamespace,
				}, dep)).To(Succeed())

				g.Expect(dep.Status.ReadyReplicas).To(Equal(*dep.Spec.Replicas))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("karpenter should be ready", func() {
			dep := &appsv1.Deployment{}
			Eventually(func(g Gomega) {
				g.Expect(env.Client.Get(ctx, types.NamespacedName{
					Name:      operandDeploymentName,
					Namespace: operatorNamespace,
				}, dep)).To(Succeed())

				g.Expect(dep.Status.ReadyReplicas).To(Equal(*dep.Spec.Replicas))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("karpenter should reconcile after deletion", func() {
			key := types.NamespacedName{Name: operandDeploymentName, Namespace: operatorNamespace}

			dep := &appsv1.Deployment{}
			Expect(env.Client.Get(ctx, key, dep)).To(Succeed())
			originalUID := dep.UID
			Expect(env.Client.Delete(ctx, dep)).To(Succeed())

			Eventually(func(g Gomega) {
				restored := &appsv1.Deployment{}
				g.Expect(env.Client.Get(ctx, key, restored)).To(Succeed())
				g.Expect(restored.UID).NotTo(Equal(originalUID))
				g.Expect(restored.Status.ReadyReplicas).To(Equal(*restored.Spec.Replicas))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("karpenter should reconcile after spec mutations", func() {
			key := types.NamespacedName{Name: operandDeploymentName, Namespace: operatorNamespace}

			dep := &appsv1.Deployment{}
			Expect(env.Client.Get(ctx, key, dep)).To(Succeed())

			originalReplicas := *dep.Spec.Replicas
			originalPodAnnotations := dep.Spec.Template.Annotations
			originalPodLabels := dep.Spec.Template.Labels

			patch := client.MergeFrom(dep.DeepCopy())
			zero := int32(0)
			dep.Spec.Replicas = &zero
			if dep.Spec.Template.Annotations == nil {
				dep.Spec.Template.Annotations = map[string]string{}
			}
			dep.Spec.Template.Annotations["e2e-test/mutated"] = "true"
			if dep.Spec.Template.Labels == nil {
				dep.Spec.Template.Labels = map[string]string{}
			}
			dep.Spec.Template.Labels["e2e-test/mutated"] = "true"
			Expect(env.Client.Patch(ctx, dep, patch)).To(Succeed())

			Eventually(func(g Gomega) {
				restored := &appsv1.Deployment{}
				g.Expect(env.Client.Get(ctx, key, restored)).To(Succeed())
				g.Expect(*restored.Spec.Replicas).To(Equal(originalReplicas))
				g.Expect(restored.Spec.Template.Annotations).To(Equal(originalPodAnnotations))
				g.Expect(restored.Spec.Template.Labels).To(Equal(originalPodLabels))
				g.Expect(restored.Status.ReadyReplicas).To(Equal(originalReplicas))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})
	})

	Context("CRDs", func() {
		It("should install core CRDs", func() {
			for _, expected := range assets.CoreCRDs {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				Eventually(func(g Gomega) {
					g.Expect(env.Client.Get(ctx, types.NamespacedName{Name: expected.Name}, crd)).To(Succeed())
					g.Expect(util.CRDEstablished(crd)).To(BeTrue(), "CRD %s should be Established", expected.Name)
				}, pollOneMinute, pollFiveSecond).Should(Succeed())
			}
		})

		It("should install AWS CRDs", func() {
			if !env.IsAWSPlatform() {
				Skip("not an AWS cluster")
			}

			for _, expected := range assets.AWSCRDs {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				Eventually(func(g Gomega) {
					g.Expect(env.Client.Get(ctx, types.NamespacedName{Name: expected.Name}, crd)).To(Succeed())
					g.Expect(util.CRDEstablished(crd)).To(BeTrue(), "CRD %s should be Established", expected.Name)
				}, pollOneMinute, pollFiveSecond).Should(Succeed())
			}
		})

		It("should reconcile a deleted CRD", func() {
			target := assets.CoreCRDs[0]
			key := types.NamespacedName{Name: target.Name}

			existing := &apiextensionsv1.CustomResourceDefinition{}
			Expect(env.Client.Get(ctx, key, existing)).To(Succeed())
			originalUID := existing.UID
			Expect(env.Client.Delete(ctx, existing)).To(Succeed())

			Eventually(func(g Gomega) {
				restored := &apiextensionsv1.CustomResourceDefinition{}
				g.Expect(env.Client.Get(ctx, key, restored)).To(Succeed())
				g.Expect(restored.UID).NotTo(Equal(originalUID))
				g.Expect(util.CRDEstablished(restored)).To(BeTrue(), "CRD %s should be re-established after deletion", target.Name)
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})
	})
	Context("NodeClass", func() {
		BeforeEach(func() {
			if !env.IsAWSPlatform() {
				Skip("not an AWS cluster")
			}
		})

		It("should create the default EC2NodeClass", func() {
			nc := &awskarpenterv1.EC2NodeClass{}
			Eventually(func(g Gomega) {
				g.Expect(env.Client.Get(ctx, types.NamespacedName{Name: "default"}, nc)).To(Succeed())
				g.Expect(ptr.Deref(nc.Spec.AMIFamily, "")).To(Equal(awskarpenterv1.AMIFamilyCustom))
				g.Expect(nc.Spec.AMISelectorTerms).NotTo(BeEmpty())
				g.Expect(ptr.Deref(nc.Spec.UserData, "")).NotTo(BeEmpty())
				g.Expect(nc.Spec.SubnetSelectorTerms).NotTo(BeEmpty())
				g.Expect(nc.Spec.SecurityGroupSelectorTerms).NotTo(BeEmpty())
				g.Expect(nc.Spec.BlockDeviceMappings).NotTo(BeEmpty())
				g.Expect(nodeClassReady(nc)).To(BeTrue(), "EC2NodeClass should have Ready=True condition")
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("should recreate the default EC2NodeClass after deletion", func() {
			key := types.NamespacedName{Name: "default"}

			nc := &awskarpenterv1.EC2NodeClass{}
			Expect(env.Client.Get(ctx, key, nc)).To(Succeed())
			originalUID := nc.UID
			Expect(env.Client.Delete(ctx, nc)).To(Succeed())

			Eventually(func(g Gomega) {
				restored := &awskarpenterv1.EC2NodeClass{}
				g.Expect(env.Client.Get(ctx, key, restored)).To(Succeed())
				g.Expect(restored.UID).NotTo(Equal(originalUID))
				g.Expect(ptr.Deref(restored.Spec.AMIFamily, "")).To(Equal(awskarpenterv1.AMIFamilyCustom))
				g.Expect(restored.Spec.AMISelectorTerms).NotTo(BeEmpty())
				g.Expect(ptr.Deref(restored.Spec.UserData, "")).NotTo(BeEmpty())
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("should re-apply protected fields after mutation", func() {
			key := types.NamespacedName{Name: "default"}

			nc := &awskarpenterv1.EC2NodeClass{}
			Expect(env.Client.Get(ctx, key, nc)).To(Succeed())
			originalAMI := nc.Spec.AMISelectorTerms[0].ID
			originalUserData := ptr.Deref(nc.Spec.UserData, "")

			patch := client.MergeFrom(nc.DeepCopy())
			nc.Spec.AMISelectorTerms = []awskarpenterv1.AMISelectorTerm{{ID: "ami-tampered"}}
			nc.Spec.UserData = ptr.To("tampered-userdata")
			Expect(env.Client.Patch(ctx, nc, patch)).To(Succeed())

			Eventually(func(g Gomega) {
				restored := &awskarpenterv1.EC2NodeClass{}
				g.Expect(env.Client.Get(ctx, key, restored)).To(Succeed())
				g.Expect(restored.Spec.AMISelectorTerms[0].ID).To(Equal(originalAMI))
				g.Expect(ptr.Deref(restored.Spec.UserData, "")).To(Equal(originalUserData))
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})

		It("should enforce protected fields on a user-created EC2NodeClass", func() {
			// Get the expected protected values from the default NodeClass
			defaultNC := &awskarpenterv1.EC2NodeClass{}
			Expect(env.Client.Get(ctx, types.NamespacedName{Name: "default"}, defaultNC)).To(Succeed())
			expectedAMI := defaultNC.Spec.AMISelectorTerms[0].ID
			expectedUserData := ptr.Deref(defaultNC.Spec.UserData, "")

			// Create a user NodeClass with user-chosen values for protected fields
			userNC := &awskarpenterv1.EC2NodeClass{
				ObjectMeta: metav1.ObjectMeta{Name: "e2e-user-nodeclass"},
				Spec: awskarpenterv1.EC2NodeClassSpec{
					AMIFamily: ptr.To(awskarpenterv1.AMIFamilyAL2023),
					AMISelectorTerms: []awskarpenterv1.AMISelectorTerm{
						{ID: "ami-userchosen"},
					},
					UserData:                   ptr.To("user-ignition"),
					SubnetSelectorTerms:        defaultNC.Spec.SubnetSelectorTerms,
					SecurityGroupSelectorTerms: defaultNC.Spec.SecurityGroupSelectorTerms,
					InstanceProfile:            defaultNC.Spec.InstanceProfile,
				},
			}
			Expect(env.Client.Create(ctx, userNC)).To(Succeed())
			DeferCleanup(func() {
				_ = env.Client.Delete(ctx, userNC)
			})

			Eventually(func(g Gomega) {
				restored := &awskarpenterv1.EC2NodeClass{}
				g.Expect(env.Client.Get(ctx, types.NamespacedName{Name: "e2e-user-nodeclass"}, restored)).To(Succeed())

				// Protected fields enforced by operator
				g.Expect(ptr.Deref(restored.Spec.AMIFamily, "")).To(Equal(awskarpenterv1.AMIFamilyCustom))
				g.Expect(restored.Spec.AMISelectorTerms).To(HaveLen(1))
				g.Expect(restored.Spec.AMISelectorTerms[0].ID).To(Equal(expectedAMI))
				g.Expect(ptr.Deref(restored.Spec.UserData, "")).To(Equal(expectedUserData))

				// User-chosen non-protected fields preserved
				g.Expect(restored.Spec.SubnetSelectorTerms).To(Equal(defaultNC.Spec.SubnetSelectorTerms))
				g.Expect(restored.Spec.SecurityGroupSelectorTerms).To(Equal(defaultNC.Spec.SecurityGroupSelectorTerms))

				// Status becomes ready
				g.Expect(nodeClassReady(restored)).To(BeTrue(), "user EC2NodeClass should have Ready=True condition")
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})
	})
})

func nodeClassReady(nc *awskarpenterv1.EC2NodeClass) bool {
	for _, c := range nc.Status.Conditions {
		if c.Type == "Ready" {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}
