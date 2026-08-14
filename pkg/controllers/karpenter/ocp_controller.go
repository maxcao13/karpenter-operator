package karpenter

import (
	"context"
	"fmt"
	"os"

	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"
	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metaac "k8s.io/client-go/applyconfigurations/meta/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/samber/lo"
)

type OCPControllerConfig struct {
	Namespace       string
	KarpenterImage  string
	ClusterName     string
	ClusterEndpoint string
	CloudProvider   common.CloudProvider
}

// OCPController deploys the karpenter operand on standalone OpenShift clusters.
// Operand resources are owned by the cluster-scoped Karpenter CR.
type OCPController struct {
	client          client.Client
	config          *OCPControllerConfig
	imagePullPolicy corev1.PullPolicy
}

func NewOCPController(mgr ctrl.Manager, cfg *OCPControllerConfig) *OCPController {
	return &OCPController{
		client:          mgr.GetClient(),
		config:          cfg,
		imagePullPolicy: operandImagePullPolicy(),
	}
}

func (c *OCPController) Name() string {
	return "karpenter"
}

func (c *OCPController) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling karpenter deployment")

	karp := &autoscalingv1alpha1.Karpenter{}
	if err := c.client.Get(ctx, client.ObjectKey{Name: autoscalingv1alpha1.SingletonName}, karp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	ref := ocpOwnerRef(karp)
	ns := c.config.Namespace

	if err := ApplyServiceAccount(ctx, c.client, ns, ref); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ServiceAccount: %w", err)
	}

	cloudRBAC := c.config.CloudProvider.RBAC()
	coreRoles := append(assets.CoreRBAC.Roles, cloudRBAC.Roles...)
	coreRoleBindings := append(assets.CoreRBAC.RoleBindings, cloudRBAC.RoleBindings...)

	if err := ApplyClusterRoles(ctx, c.client, ref, append(assets.CoreRBAC.ClusterRoles, cloudRBAC.ClusterRoles...)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ClusterRoles: %w", err)
	}
	if err := ApplyClusterRoleBindings(ctx, c.client, ns, ref, append(assets.CoreRBAC.ClusterRoleBindings, cloudRBAC.ClusterRoleBindings...)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ClusterRoleBindings: %w", err)
	}

	if err := ApplyRoles(ctx, c.client, ns, ref, coreRoles); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Roles: %w", err)
	}
	if err := ApplyRoleBindings(ctx, c.client, ns, ref, coreRoleBindings); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile RoleBindings: %w", err)
	}

	cfg := &OperandConfig{
		Namespace:       c.config.Namespace,
		KarpenterImage:  c.config.KarpenterImage,
		ClusterName:     c.config.ClusterName,
		ClusterEndpoint: c.config.ClusterEndpoint,
		CloudProvider:   c.config.CloudProvider,
		ImagePullPolicy: c.imagePullPolicy,
		LogLevelArg:     karp.Spec.LogLevel.Arg(),
	}
	if err := ApplyDeployment(ctx, c.client, cfg, ref); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Deployment: %w", err)
	}

	return ctrl.Result{}, nil
}

func (c *OCPController) SetupWithManager(mgr ctrl.Manager) error {
	cloudRBAC := c.config.CloudProvider.RBAC()
	managedClusterRoles := lo.KeyBy(append(assets.CoreRBAC.ClusterRoles, cloudRBAC.ClusterRoles...), func(r *rbacv1.ClusterRole) string {
		return r.Name
	})
	managedClusterRoleBindings := lo.KeyBy(append(assets.CoreRBAC.ClusterRoleBindings, cloudRBAC.ClusterRoleBindings...), func(b *rbacv1.ClusterRoleBinding) string {
		return b.Name
	})

	reconcileRequest := []ctrl.Request{{NamespacedName: client.ObjectKey{Name: autoscalingv1alpha1.SingletonName}}}

	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		For(&autoscalingv1alpha1.Karpenter{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				cloudCfg := c.config.CloudProvider.OperandConfig()
				if o.GetNamespace() != c.config.Namespace || o.GetName() != cloudCfg.CredentialsSecretName {
					return nil
				}
				return reconcileRequest
			},
		)).
		Watches(&rbacv1.ClusterRole{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				if _, ok := managedClusterRoles[o.GetName()]; !ok {
					return nil
				}
				return reconcileRequest
			},
		)).
		Watches(&rbacv1.ClusterRoleBinding{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				if _, ok := managedClusterRoleBindings[o.GetName()]; !ok {
					return nil
				}
				return reconcileRequest
			},
		)).
		Complete(c)
}

func ocpOwnerRef(owner *autoscalingv1alpha1.Karpenter) *metaac.OwnerReferenceApplyConfiguration {
	return metaac.OwnerReference().
		WithAPIVersion(autoscalingv1alpha1.SchemeGroupVersion.String()).
		WithKind("Karpenter").
		WithName(owner.Name).
		WithUID(owner.UID).
		WithBlockOwnerDeletion(true).
		WithController(true)
}

// TODO(maxcao13): remove before GA — only for dev/test iteration with :latest tags.
func operandImagePullPolicy() corev1.PullPolicy {
	if v := os.Getenv("DEV_IMAGE_PULL_POLICY"); v == "Always" {
		return corev1.PullAlways
	}
	return corev1.PullIfNotPresent
}
