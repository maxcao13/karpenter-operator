package operator

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/assets"

	configv1 "github.com/openshift/api/config/v1"

	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/types"

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
					g.Expect(crdEstablished(crd)).To(BeTrue(), "CRD %s should be Established", expected.Name)
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
					g.Expect(crdEstablished(crd)).To(BeTrue(), "CRD %s should be Established", expected.Name)
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
				g.Expect(crdEstablished(restored)).To(BeTrue(), "CRD %s should be re-established after deletion", target.Name)
			}, pollOneMinute, pollFiveSecond).Should(Succeed())
		})
	})
})

func crdEstablished(crd *apiextensionsv1.CustomResourceDefinition) bool {
	for _, c := range crd.Status.Conditions {
		if c.Type == apiextensionsv1.Established {
			return c.Status == apiextensionsv1.ConditionTrue
		}
	}
	return false
}
