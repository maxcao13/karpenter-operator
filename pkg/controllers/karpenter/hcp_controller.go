package karpenter

import (
	"context"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metaac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	targetKubeconfigVolumeName = "target-kubeconfig"
	targetKubeconfigMountPath  = "/mnt/kubeconfig"
	targetKubeconfigFilePath   = "target-kubeconfig"
	targetKubeconfigSecretKey  = "value"

	serviceAccountTokenVolumeName = "serviceaccount-token"
	serviceAccountTokenMountPath  = "/var/run/secrets/openshift/serviceaccount" // nolint:gosec
	serviceAccountTokenFilePath   = serviceAccountTokenMountPath + "/token"
)

type HCPControllerConfig struct {
	Namespace        string
	KarpenterImage   string
	ClusterName      string
	ClusterEndpoint  string
	CloudProvider    common.CloudProvider
	TokenMinterImage string
}

// HCPController watches HostedControlPlane objects in management cluster mode and
// deploys the karpenter operand in the same namespace as the HCP.
type HCPController struct {
	client          client.Client
	config          *HCPControllerConfig
	imagePullPolicy corev1.PullPolicy
}

func NewHCPController(mgr ctrl.Manager, cfg *HCPControllerConfig) *HCPController {
	return &HCPController{
		client:          mgr.GetClient(),
		config:          cfg,
		imagePullPolicy: corev1.PullIfNotPresent,
	}
}

func (c *HCPController) Name() string {
	return "karpenter"
}

func (c *HCPController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling karpenter deployment on management cluster")

	hcp := &hyperv1.HostedControlPlane{}
	if err := c.client.Get(ctx, req.NamespacedName, hcp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// TODO(maxcao13): for now we always scale up karpenter if an HCP is provisioned (meaning always)
	// In the future, we need to allow scale to zero based on the HCP AutoNode spec.
	// https://redhat.atlassian.net/browse/AUTOSCALE-520
	if hcp.Spec.AutoNode.Provisioner.Name != "Karpenter" {
		log.FromContext(ctx).V(1).Info("HCP does not use Karpenter provisioner, skipping")
		return ctrl.Result{}, nil
	}

	ref := hcpOwnerRef(hcp)

	if err := ApplyServiceAccount(ctx, c.client, c.config.Namespace, ref); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ServiceAccount: %w", err)
	}

	cfg := &OperandConfig{
		Namespace:       c.config.Namespace,
		KarpenterImage:  c.config.KarpenterImage,
		ClusterName:     c.config.ClusterName,
		ClusterEndpoint: c.config.ClusterEndpoint,
		CloudProvider:   c.config.CloudProvider,
		ImagePullPolicy: c.imagePullPolicy,
		LogLevelArg:     "--log-level=debug", // TODO(maxcao13): make this configurable
		AdditionalEnv: []corev1.EnvVar{
			{Name: common.KubeconfigEnvName, Value: targetKubeconfigMountPath + "/" + targetKubeconfigFilePath},
			{Name: common.DisableLeaderElectionEnvName, Value: "true"},
		},
		AdditionalVolumeMounts: []corev1.VolumeMount{
			{Name: targetKubeconfigVolumeName, MountPath: targetKubeconfigMountPath, ReadOnly: true},
			{Name: serviceAccountTokenVolumeName, MountPath: serviceAccountTokenMountPath},
		},
		AdditionalVolumes: []corev1.Volume{
			{
				Name: targetKubeconfigVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  hcp.Spec.InfraID + "-kubeconfig",
						DefaultMode: ptr.To(int32(0640)),
						Items: []corev1.KeyToPath{
							{Key: targetKubeconfigSecretKey, Path: targetKubeconfigFilePath},
						},
					},
				},
			},
			{
				Name:         serviceAccountTokenVolumeName,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			},
		},
		AdditionalInitContainers: []corev1.Container{tokenMinterContainer(c.config.TokenMinterImage)},
	}
	if err := ApplyDeployment(ctx, c.client, cfg, ref); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Deployment: %w", err)
	}

	return ctrl.Result{}, nil
}

func (c *HCPController) SetupWithManager(mgr ctrl.Manager) error {
	karpenterFilterPredicate := predicate.NewPredicateFuncs(func(o client.Object) bool {
		return o.GetName() == "karpenter"
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		For(&hyperv1.HostedControlPlane{}).
		Owns(&appsv1.Deployment{}, builder.WithPredicates(karpenterFilterPredicate)).
		Owns(&corev1.ServiceAccount{}, builder.WithPredicates(karpenterFilterPredicate)).
		Complete(c)
}

func hcpOwnerRef(hcp *hyperv1.HostedControlPlane) *metaac.OwnerReferenceApplyConfiguration {
	return metaac.OwnerReference().
		WithAPIVersion(hyperv1.GroupVersion.String()).
		WithKind("HostedControlPlane").
		WithName(hcp.Name).
		WithUID(hcp.UID).
		WithBlockOwnerDeletion(true).
		WithController(true)
}

func tokenMinterContainer(image string) corev1.Container {
	return corev1.Container{
		Name:    "token-minter",
		Image:   image,
		Command: []string{"/usr/bin/control-plane-operator", "token-minter"},
		Args: []string{
			"--service-account-namespace=kube-system",
			"--service-account-name=karpenter",
			"--token-file=" + serviceAccountTokenFilePath,
			"--kubeconfig=" + targetKubeconfigMountPath + "/" + targetKubeconfigFilePath,
		},
		ImagePullPolicy: corev1.PullIfNotPresent,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("30Mi"),
			},
		},
		RestartPolicy: ptr.To(corev1.ContainerRestartPolicyAlways),
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{
					Command: []string{"cat", serviceAccountTokenFilePath},
				},
			},
			FailureThreshold: 10,
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: targetKubeconfigVolumeName, MountPath: targetKubeconfigMountPath},
			{Name: serviceAccountTokenVolumeName, MountPath: serviceAccountTokenMountPath},
		},
	}
}
