package karpenter

import (
	"testing"

	. "github.com/onsi/gomega"

	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	configv1 "github.com/openshift/api/config/v1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testNamespace       = "openshift-karpenter"
	testKarpenterImage  = "quay.io/openshift/karpenter:test"
	testClusterName     = "test-cluster"
	testClusterEndpoint = "https://api.test-cluster.example.com:6443"
)

// fakeCloudProvider implements common.CloudProvider for testing.
type fakeCloudProvider struct{}

func (f *fakeCloudProvider) AddToScheme(_ *runtime.Scheme) error { return nil }
func (f *fakeCloudProvider) KarpenterImage() string              { return testKarpenterImage }
func (f *fakeCloudProvider) CRDs() []*apiextensionsv1.CustomResourceDefinition {
	return nil
}
func (f *fakeCloudProvider) RelatedObjects() []configv1.ObjectReference { return nil }
func (f *fakeCloudProvider) RBAC() common.RBACAssets {
	return common.RBACAssets{
		ClusterRoles: []*rbacv1.ClusterRole{
			{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-cloud-test"}, Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{"test.io"}, Resources: []string{"widgets"}, Verbs: []string{"get", "list"}},
			}},
		},
		ClusterRoleBindings: []*rbacv1.ClusterRoleBinding{
			{ObjectMeta: metav1.ObjectMeta{Name: "karpenter-cloud-test"}, RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "karpenter-cloud-test",
			}, Subjects: []rbacv1.Subject{
				{Kind: "ServiceAccount", Name: karpenterName, Namespace: testNamespace},
			}},
		},
	}
}
func (f *fakeCloudProvider) OperandConfig() common.OperandCloudConfig {
	return common.OperandCloudConfig{
		CredentialsSecretName: "karpenter-cloud-credentials",
		Env: []corev1.EnvVar{
			{Name: "CLOUD_REGION", Value: "us-east-1"},
		},
		Volumes: []corev1.Volume{
			{Name: "cloud-creds", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "karpenter-cloud-credentials"},
			}},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "cloud-creds", MountPath: "/var/run/secrets/cloud", ReadOnly: true},
		},
	}
}

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = autoscalingv1alpha1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	_ = rbacv1.AddToScheme(s)
	return s
}

func newTestController(objs ...client.Object) *Controller {
	c := fakeclient.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		Build()

	return &Controller{
		client: c,
		config: &ControllerConfig{
			Namespace:       testNamespace,
			KarpenterImage:  testKarpenterImage,
			ClusterName:     testClusterName,
			ClusterEndpoint: testClusterEndpoint,
			CloudProvider:   &fakeCloudProvider{},
		},
		imagePullPolicy: corev1.PullIfNotPresent,
	}
}

func karpenterCR(logLevel autoscalingv1alpha1.KarpenterLogLevel) *autoscalingv1alpha1.Karpenter {
	return &autoscalingv1alpha1.Karpenter{
		ObjectMeta: metav1.ObjectMeta{
			Name: autoscalingv1alpha1.SingletonName,
			UID:  types.UID("test-uid-1234"),
		},
		Spec: autoscalingv1alpha1.KarpenterSpec{
			LogLevel: logLevel,
		},
	}
}

func TestReconcile_CRNotFound(t *testing.T) {
	g := NewWithT(t)
	controller := newTestController()

	result, err := controller.Reconcile(t.Context(), ctrl.Request{})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(result).To(Equal(ctrl.Result{}))

	depList := &appsv1.DeploymentList{}
	g.Expect(controller.client.List(t.Context(), depList)).To(Succeed())
	g.Expect(depList.Items).To(BeEmpty())
}

func TestReconcile_CreatesResources(t *testing.T) {
	g := NewWithT(t)
	controller := newTestController(karpenterCR(autoscalingv1alpha1.LogLevelInfo))

	_, err := controller.Reconcile(t.Context(), ctrl.Request{})
	g.Expect(err).NotTo(HaveOccurred())

	// ServiceAccount
	sa := &corev1.ServiceAccount{}
	g.Expect(controller.client.Get(t.Context(), client.ObjectKey{
		Namespace: testNamespace, Name: karpenterName,
	}, sa)).To(Succeed())
	g.Expect(sa.OwnerReferences).To(HaveLen(1))
	g.Expect(sa.OwnerReferences[0].Kind).To(Equal("Karpenter"))
	g.Expect(sa.OwnerReferences[0].Name).To(Equal(autoscalingv1alpha1.SingletonName))

	// Deployment
	dep := &appsv1.Deployment{}
	g.Expect(controller.client.Get(t.Context(), client.ObjectKey{
		Namespace: testNamespace, Name: karpenterName,
	}, dep)).To(Succeed())
	g.Expect(dep.OwnerReferences).To(HaveLen(1))
	g.Expect(dep.OwnerReferences[0].Kind).To(Equal("Karpenter"))
	g.Expect(*dep.Spec.Replicas).To(Equal(int32(1)))
	containers := dep.Spec.Template.Spec.Containers
	g.Expect(containers).To(HaveLen(1))
	g.Expect(containers[0].Name).To(Equal(karpenterName))
	g.Expect(containers[0].Image).To(Equal(testKarpenterImage))

	// Cloud ClusterRole
	cr := &rbacv1.ClusterRole{}
	g.Expect(controller.client.Get(t.Context(), client.ObjectKey{Name: "karpenter-cloud-test"}, cr)).To(Succeed())
	g.Expect(cr.Rules).To(HaveLen(1))
	g.Expect(cr.Rules[0].APIGroups).To(ContainElement("test.io"))

	// Cloud ClusterRoleBinding
	crb := &rbacv1.ClusterRoleBinding{}
	g.Expect(controller.client.Get(t.Context(), client.ObjectKey{Name: "karpenter-cloud-test"}, crb)).To(Succeed())
	g.Expect(crb.Subjects).To(HaveLen(1))
	g.Expect(crb.Subjects[0].Name).To(Equal(karpenterName))
	g.Expect(crb.Subjects[0].Namespace).To(Equal(testNamespace))
}

func TestReconcile_LogLevel(t *testing.T) {
	testCases := []struct {
		name        string
		logLevel    autoscalingv1alpha1.KarpenterLogLevel
		expectedArg string
	}{
		{"debug", autoscalingv1alpha1.LogLevelDebug, "--log-level=debug"},
		{"info", autoscalingv1alpha1.LogLevelInfo, "--log-level=info"},
		{"error", autoscalingv1alpha1.LogLevelError, "--log-level=error"},
		{"empty defaults to info", "", "--log-level=info"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			controller := newTestController(karpenterCR(tc.logLevel))

			_, err := controller.Reconcile(t.Context(), ctrl.Request{})
			g.Expect(err).NotTo(HaveOccurred())

			dep := &appsv1.Deployment{}
			g.Expect(controller.client.Get(t.Context(), client.ObjectKey{
				Namespace: testNamespace, Name: karpenterName,
			}, dep)).To(Succeed())

			g.Expect(dep.Spec.Template.Spec.Containers[0].Args).To(ContainElement(tc.expectedArg))
		})
	}
}

func TestReconcile_DeploymentSpec(t *testing.T) {
	g := NewWithT(t)
	controller := newTestController(karpenterCR(autoscalingv1alpha1.LogLevelInfo))

	_, err := controller.Reconcile(t.Context(), ctrl.Request{})
	g.Expect(err).NotTo(HaveOccurred())

	dep := &appsv1.Deployment{}
	g.Expect(controller.client.Get(t.Context(), client.ObjectKey{
		Namespace: testNamespace, Name: karpenterName,
	}, dep)).To(Succeed())

	podSpec := dep.Spec.Template.Spec
	g.Expect(podSpec.ServiceAccountName).To(Equal(karpenterName))
	g.Expect(podSpec.PriorityClassName).To(Equal(karpenterPodPriorityClassName))
	g.Expect(podSpec.SecurityContext).NotTo(BeNil())
	g.Expect(*podSpec.SecurityContext.RunAsNonRoot).To(BeTrue())

	container := podSpec.Containers[0]
	g.Expect(container.SecurityContext).NotTo(BeNil())
	g.Expect(*container.SecurityContext.AllowPrivilegeEscalation).To(BeFalse())
	g.Expect(container.LivenessProbe).NotTo(BeNil())
	g.Expect(container.LivenessProbe.HTTPGet.Path).To(Equal("/healthz"))
	g.Expect(container.ReadinessProbe).NotTo(BeNil())
	g.Expect(container.ReadinessProbe.HTTPGet.Path).To(Equal("/readyz"))

	envNames := map[string]bool{}
	for _, e := range container.Env {
		envNames[e.Name] = true
	}
	g.Expect(envNames).To(HaveKey("CLUSTER_NAME"))
	g.Expect(envNames).To(HaveKey("CLUSTER_ENDPOINT"))
	g.Expect(envNames).To(HaveKey("CLOUD_REGION"))

	mountNames := map[string]bool{}
	for _, m := range container.VolumeMounts {
		mountNames[m.Name] = true
	}
	g.Expect(mountNames).To(HaveKey("cloud-creds"))
}
