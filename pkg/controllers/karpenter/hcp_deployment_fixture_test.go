package karpenter

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"
	"github.com/openshift/karpenter-operator/test/pkg/testutil"

	configv1 "github.com/openshift/api/config/v1"
	hyperv1beta1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	appsac "k8s.io/client-go/applyconfigurations/apps/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	monitoringv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
)

func TestHCPDeploymentFixture(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*hyperv1beta1.HostedControlPlane, *HCPControllerConfig)
	}{
		{
			name: "When an HCP uses Karpenter, it should render the expected deployment",
		},
		{
			name: "When HCP deployment inputs change, it should render the updated deployment",
			mutate: func(hcp *hyperv1beta1.HostedControlPlane, cfg *HCPControllerConfig) {
				hcp.Spec.InfraID = "autonode-k8r4p"
				hcp.Status.ControlPlaneVersion.Desired.Version = "4.21.11"
				cfg.KarpenterImage = "quay.io/openshift/origin-aws-karpenter-provider-aws:4.21"
				cfg.TokenMinterImage = "quay.io/openshift/origin-hypershift:4.21"
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			hcp := &hyperv1beta1.HostedControlPlane{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "autonode",
					Namespace: "clusters-autonode",
					UID:       "ea7a5b7d-bd5b-4532-98e4-59a52b52cb74",
				},
				Spec: hyperv1beta1.HostedControlPlaneSpec{
					ReleaseImage: "quay.io/openshift-release-dev/ocp-release:4.21.10-x86_64",
					InfraID:      "autonode-9q7m2",
					AutoNode: hyperv1beta1.AutoNode{
						Provisioner: hyperv1beta1.ProvisionerConfig{Name: hyperv1beta1.ProvisionerKarpenter},
					},
				},
				Status: hyperv1beta1.HostedControlPlaneStatus{
					ControlPlaneVersion: hyperv1beta1.ControlPlaneVersionStatus{
						Desired: configv1.Release{Version: "4.21.10"},
					},
				},
			}
			cfg := &HCPControllerConfig{
				Namespace:        hcp.Namespace,
				KarpenterImage:   "quay.io/openshift/origin-aws-karpenter-provider-aws:latest",
				ClusterName:      "autonode",
				ClusterEndpoint:  "https://api.autonode.hypershift.local:6443",
				TokenMinterImage: "quay.io/openshift/origin-hypershift:latest",
				CloudProvider: &testfake.CloudProvider{
					CloudConfig: common.OperandCloudConfig{
						CredentialsSecretName: "karpenter-credentials",
						Env: []corev1.EnvVar{
							{Name: "AWS_REGION", Value: "us-east-1"},
							{Name: "AWS_SHARED_CREDENTIALS_FILE", Value: "/etc/provider/credentials"},
							{Name: "AWS_SDK_LOAD_CONFIG", Value: "true"},
						},
						Volumes: []corev1.Volume{{
							Name: "provider-creds",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: "karpenter-credentials"},
							},
						}},
						VolumeMounts: []corev1.VolumeMount{{Name: "provider-creds", MountPath: "/etc/provider", ReadOnly: true}},
					},
				},
			}
			if tc.mutate != nil {
				tc.mutate(hcp, cfg)
			}

			scheme := runtime.NewScheme()
			g.Expect(hyperv1beta1.AddToScheme(scheme)).To(Succeed())
			g.Expect(appsv1.AddToScheme(scheme)).To(Succeed())
			g.Expect(corev1.AddToScheme(scheme)).To(Succeed())
			g.Expect(monitoringv1.AddToScheme(scheme)).To(Succeed())

			// Snapshot the exact SSA payload, before API-generated fields such as
			// resourceVersion, managedFields, and status can add noise to the fixture.
			var deployment []byte
			cl := fakeclient.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(hcp).
				WithInterceptorFuncs(interceptor.Funcs{
					Apply: func(ctx context.Context, cl client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
						if dep, ok := obj.(*appsac.DeploymentApplyConfiguration); ok {
							var err error
							deployment, err = yaml.Marshal(dep)
							if err != nil {
								return err
							}
						}
						return cl.Apply(ctx, obj, opts...)
					},
				}).
				Build()
			controller := NewHCPController(cl, cfg)
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(hcp)}

			// Both initial creation and a subsequent reconcile must render the
			// same manifest, without depending on API-assigned metadata.
			for range 2 {
				deployment = nil
				result, err := controller.Reconcile(t.Context(), req)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(result).To(Equal(ctrl.Result{}))
				g.Expect(deployment).NotTo(BeNil())
				testutil.CompareWithFixture(t, deployment)
			}
		})
	}
}
