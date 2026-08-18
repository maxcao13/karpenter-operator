package karpenter

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	hcpTestNamespace        = "clusters-test-hcp"
	hcpTestHCPName          = "test-hcp"
	hcpTestInfraID          = "test-infra-id"
	hcpTestKarpenterImage   = "quay.io/openshift/karpenter:test"
	hcpTestClusterName      = "test-cluster"
	hcpTestClusterEndpoint  = "https://api.test-cluster.example.com:6443"
	hcpTestTokenMinterImage = "quay.io/openshift/hypershift:test"
)

var hcpTestCloudProvider = &testfake.CloudProvider{
	Image: hcpTestKarpenterImage,
	CloudConfig: common.OperandCloudConfig{
		CredentialsSecretName: "karpenter-credentials",
		Env: []corev1.EnvVar{
			{Name: "AWS_REGION", Value: "us-east-1"},
		},
		Volumes: []corev1.Volume{
			{Name: "provider-creds", VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{SecretName: "karpenter-credentials"},
			}},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "provider-creds", MountPath: "/etc/provider", ReadOnly: true},
		},
	},
}

func hcpTestScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = hyperv1.AddToScheme(s)
	_ = appsv1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

func newHCPTestController(objs ...client.Object) *HCPController {
	c := fakeclient.NewClientBuilder().
		WithScheme(hcpTestScheme()).
		WithObjects(objs...).
		Build()

	return &HCPController{
		client: c,
		config: &HCPControllerConfig{
			Namespace:        hcpTestNamespace,
			KarpenterImage:   hcpTestKarpenterImage,
			ClusterName:      hcpTestClusterName,
			ClusterEndpoint:  hcpTestClusterEndpoint,
			CloudProvider:    hcpTestCloudProvider,
			TokenMinterImage: hcpTestTokenMinterImage,
		},
		imagePullPolicy: corev1.PullIfNotPresent,
	}
}

func hcpReconcileRequest() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{
		Namespace: hcpTestNamespace,
		Name:      hcpTestHCPName,
	}}
}

func hcpWithProvisioner(name hyperv1.Provisioner) *hyperv1.HostedControlPlane {
	return &hyperv1.HostedControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Name:      hcpTestHCPName,
			Namespace: hcpTestNamespace,
			UID:       types.UID("hcp-uid-1234"),
		},
		Spec: hyperv1.HostedControlPlaneSpec{
			InfraID: hcpTestInfraID,
			AutoNode: hyperv1.AutoNode{
				Provisioner: hyperv1.ProvisionerConfig{
					Name: name,
				},
			},
		},
	}
}

func mutateHCPDeployment(ctx context.Context, cl client.Client) error {
	dep := &appsv1.Deployment{}
	key := client.ObjectKey{Namespace: hcpTestNamespace, Name: "karpenter"}
	if err := cl.Get(ctx, key, dep); err != nil {
		return err
	}
	replicas := int32(3)
	dep.Spec.Replicas = &replicas
	dep.Spec.Template.Spec.Containers[0].Image = "quay.io/mutated/karpenter:wrong"
	dep.Spec.Template.Spec.InitContainers = nil
	return cl.Update(ctx, dep)
}

func mutateHCPServiceAccount(ctx context.Context, cl client.Client) error {
	sa := &corev1.ServiceAccount{}
	key := client.ObjectKey{Namespace: hcpTestNamespace, Name: "karpenter"}
	if err := cl.Get(ctx, key, sa); err != nil {
		return err
	}
	sa.OwnerReferences = nil
	return cl.Update(ctx, sa)
}

func TestHCPReconcile(t *testing.T) {
	tests := []struct {
		name             string
		objects          []client.Object
		expectErr        bool
		expectDeployment bool
		mutate           func(context.Context, client.Client) error
	}{
		{
			name:             "When HostedControlPlane does not exist it should not create resources",
			expectDeployment: false,
		},
		{
			name:             "When provisioner is not Karpenter it should not create resources",
			objects:          []client.Object{hcpWithProvisioner("")},
			expectDeployment: false,
		},
		{
			name:             "When HostedControlPlane uses Karpenter it should create operand resources owned by the HCP",
			objects:          []client.Object{hcpWithProvisioner(hyperv1.ProvisionerKarpenter)},
			expectDeployment: true,
		},
		{
			name:             "When the karpenter Deployment is mutated it should restore the desired spec",
			objects:          []client.Object{hcpWithProvisioner(hyperv1.ProvisionerKarpenter)},
			expectDeployment: true,
			mutate:           mutateHCPDeployment,
		},
		{
			name:             "When the karpenter ServiceAccount is mutated it should restore the desired state",
			objects:          []client.Object{hcpWithProvisioner(hyperv1.ProvisionerKarpenter)},
			expectDeployment: true,
			mutate:           mutateHCPServiceAccount,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			controller := newHCPTestController(tc.objects...)
			ctx := t.Context()
			req := hcpReconcileRequest()

			result, err := controller.Reconcile(ctx, req)
			if tc.expectErr {
				g.Expect(err).To(HaveOccurred())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result).To(Equal(ctrl.Result{}))

			if tc.mutate != nil {
				g.Expect(tc.mutate(ctx, controller.client)).To(Succeed())
				result, err = controller.Reconcile(ctx, req)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result).To(Equal(ctrl.Result{}))
			}

			depList := &appsv1.DeploymentList{}
			g.Expect(controller.client.List(ctx, depList)).To(Succeed())
			if !tc.expectDeployment {
				g.Expect(depList.Items).To(BeEmpty())
				return
			}
			g.Expect(depList.Items).To(HaveLen(1))

			sa := &corev1.ServiceAccount{}
			g.Expect(controller.client.Get(ctx, client.ObjectKey{
				Namespace: hcpTestNamespace, Name: "karpenter",
			}, sa)).To(Succeed())
			g.Expect(sa.OwnerReferences).To(HaveLen(1))
			g.Expect(sa.OwnerReferences[0].Kind).To(Equal("HostedControlPlane"))
			g.Expect(sa.OwnerReferences[0].Name).To(Equal(hcpTestHCPName))

			dep := &appsv1.Deployment{}
			g.Expect(controller.client.Get(ctx, client.ObjectKey{
				Namespace: hcpTestNamespace, Name: "karpenter",
			}, dep)).To(Succeed())
			g.Expect(dep.OwnerReferences).To(HaveLen(1))
			g.Expect(dep.OwnerReferences[0].Kind).To(Equal("HostedControlPlane"))
			expectHCPDeployment(g, dep)
		})
	}
}

func expectHCPDeployment(g Gomega, dep *appsv1.Deployment) {
	g.Expect(*dep.Spec.Replicas).To(Equal(int32(1)))
	g.Expect(dep.Spec.Template.Spec.Containers).To(HaveLen(1))
	g.Expect(dep.Spec.Template.Spec.Containers[0].Name).To(Equal("karpenter"))
	g.Expect(dep.Spec.Template.Spec.Containers[0].Image).To(Equal(hcpTestKarpenterImage))

	podSpec := dep.Spec.Template.Spec
	g.Expect(podSpec.ServiceAccountName).To(Equal("karpenter"))
	g.Expect(podSpec.PriorityClassName).To(Equal(PriorityClassName))

	container := podSpec.Containers[0]
	g.Expect(container.Args).To(ContainElement("--log-level=debug"))

	env := map[string]string{}
	for _, e := range container.Env {
		env[e.Name] = e.Value
	}
	g.Expect(env).To(HaveKeyWithValue(common.KubeconfigEnvName, targetKubeconfigMountPath+"/"+targetKubeconfigFilePath))
	g.Expect(env).To(HaveKeyWithValue(common.DisableLeaderElectionEnvName, "true"))
	g.Expect(env).To(HaveKeyWithValue("AWS_REGION", "us-east-1"))

	mountNames := map[string]bool{}
	for _, m := range container.VolumeMounts {
		mountNames[m.Name] = true
	}
	g.Expect(mountNames).To(HaveKey(targetKubeconfigVolumeName))
	g.Expect(mountNames).To(HaveKey(serviceAccountTokenVolumeName))
	g.Expect(mountNames).To(HaveKey("provider-creds"))

	volumeByName := map[string]corev1.Volume{}
	for _, v := range podSpec.Volumes {
		volumeByName[v.Name] = v
	}
	g.Expect(volumeByName).To(HaveKey(targetKubeconfigVolumeName))
	kubeconfigVol := volumeByName[targetKubeconfigVolumeName]
	g.Expect(kubeconfigVol.Secret).NotTo(BeNil())
	g.Expect(kubeconfigVol.Secret.SecretName).To(Equal(hcpTestInfraID + "-kubeconfig"))
	g.Expect(kubeconfigVol.Secret.DefaultMode).To(Equal(ptr.To(int32(0640))))
	g.Expect(volumeByName).To(HaveKey(serviceAccountTokenVolumeName))
	g.Expect(volumeByName[serviceAccountTokenVolumeName].EmptyDir).NotTo(BeNil())

	g.Expect(podSpec.InitContainers).To(HaveLen(1))
	tokenMinter := podSpec.InitContainers[0]
	g.Expect(tokenMinter.Name).To(Equal("token-minter"))
	g.Expect(tokenMinter.Image).To(Equal(hcpTestTokenMinterImage))
	g.Expect(tokenMinter.Command).To(Equal([]string{"/usr/bin/control-plane-operator", "token-minter"}))
	g.Expect(tokenMinter.Args).To(ContainElements(
		"--token-audience=openshift",
		"--service-account-namespace=kube-system",
		"--service-account-name=karpenter",
		"--token-file="+serviceAccountTokenFilePath,
		"--kubeconfig="+targetKubeconfigMountPath+"/"+targetKubeconfigFilePath,
	))
	g.Expect(tokenMinter.RestartPolicy).NotTo(BeNil())
	g.Expect(*tokenMinter.RestartPolicy).To(Equal(corev1.ContainerRestartPolicyAlways))
	g.Expect(tokenMinter.StartupProbe).NotTo(BeNil())
	g.Expect(tokenMinter.StartupProbe.Exec.Command).To(Equal([]string{"cat", serviceAccountTokenFilePath}))
}

func TestHCPReconcileAzureOperandConfig(t *testing.T) {
	g := NewWithT(t)

	azureCloudProvider := &testfake.CloudProvider{
		Image: hcpTestKarpenterImage,
		CloudConfig: common.OperandCloudConfig{
			Env: []corev1.EnvVar{
				{Name: "LOCATION", Value: "eastus"},
				{Name: "AZURE_CLIENT_ID", Value: "client-id"},
				{Name: "AZURE_TENANT_ID", Value: "tenant-id"},
				{Name: "AZURE_SUBSCRIPTION_ID", Value: "subscription-id"},
				{Name: "AZURE_FEDERATED_TOKEN_FILE", Value: serviceAccountTokenFilePath},
			},
		},
	}

	c := fakeclient.NewClientBuilder().
		WithScheme(hcpTestScheme()).
		WithObjects(hcpWithProvisioner(hyperv1.ProvisionerKarpenter)).
		Build()
	controller := &HCPController{
		client: c,
		config: &HCPControllerConfig{
			Namespace:        hcpTestNamespace,
			KarpenterImage:   hcpTestKarpenterImage,
			ClusterName:      hcpTestClusterName,
			ClusterEndpoint:  hcpTestClusterEndpoint,
			CloudProvider:    azureCloudProvider,
			TokenMinterImage: hcpTestTokenMinterImage,
		},
		imagePullPolicy: corev1.PullIfNotPresent,
	}

	_, err := controller.Reconcile(t.Context(), hcpReconcileRequest())
	g.Expect(err).NotTo(HaveOccurred())

	dep := &appsv1.Deployment{}
	g.Expect(controller.client.Get(t.Context(), client.ObjectKey{
		Namespace: hcpTestNamespace, Name: "karpenter",
	}, dep)).To(Succeed())

	env := map[string]string{}
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	g.Expect(env).To(HaveKeyWithValue("LOCATION", "eastus"))
	g.Expect(env).To(HaveKeyWithValue("AZURE_CLIENT_ID", "client-id"))
	g.Expect(env).To(HaveKeyWithValue("AZURE_TENANT_ID", "tenant-id"))
	g.Expect(env).To(HaveKeyWithValue("AZURE_SUBSCRIPTION_ID", "subscription-id"))
	g.Expect(env).To(HaveKeyWithValue("AZURE_FEDERATED_TOKEN_FILE", serviceAccountTokenFilePath))
	g.Expect(env).NotTo(HaveKey("AWS_REGION"))

	mountNames := map[string]bool{}
	for _, m := range dep.Spec.Template.Spec.Containers[0].VolumeMounts {
		mountNames[m.Name] = true
	}
	g.Expect(mountNames).To(HaveKey(serviceAccountTokenVolumeName))
	g.Expect(mountNames).NotTo(HaveKey("provider-creds"))
}
