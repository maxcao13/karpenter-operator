package deployment

import (
	"context"
	"fmt"
	"os"

	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	appsac "k8s.io/client-go/applyconfigurations/apps/v1"
	coreac "k8s.io/client-go/applyconfigurations/core/v1"
	metaac "k8s.io/client-go/applyconfigurations/meta/v1"
	rbacac "k8s.io/client-go/applyconfigurations/rbac/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	karpenterName          = "karpenter"
	operatorDeploymentName = "karpenter-operator"
	fieldManager           = "karpenter-operator"
)

type ControllerConfig struct {
	Namespace       string
	KarpenterImage  string
	ClusterName     string
	ClusterEndpoint string
	CloudProvider   common.CloudProvider
}

// Controller deploys the karpenter operand (Deployment, ServiceAccount, RBAC).
// All namespace-scoped operand resources are owned by the operator Deployment
// so that Kubernetes garbage collection cleans them up if the operator is removed.
type Controller struct {
	client          client.Client
	config          *ControllerConfig
	imagePullPolicy corev1.PullPolicy
}

func NewController(mgr ctrl.Manager, cfg *ControllerConfig) *Controller {
	return &Controller{
		client:          mgr.GetClient(),
		config:          cfg,
		imagePullPolicy: operandImagePullPolicy(),
	}
}

func (c *Controller) Name() string {
	return "deployment"
}

func (c *Controller) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling karpenter deployment")

	owner, err := c.getOperatorDeployment(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get operator deployment: %w", err)
	}

	if err := c.applyServiceAccount(ctx, owner); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ServiceAccount: %w", err)
	}

	cloudRBAC := c.config.CloudProvider.RBAC()
	if err := c.applyRoles(ctx, owner, append(assets.CoreRBAC.Roles, cloudRBAC.Roles...)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Roles: %w", err)
	}
	if err := c.applyRoleBindings(ctx, owner, append(assets.CoreRBAC.RoleBindings, cloudRBAC.RoleBindings...)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile RoleBindings: %w", err)
	}
	if err := c.applyClusterRoles(ctx, append(assets.CoreRBAC.ClusterRoles, cloudRBAC.ClusterRoles...)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ClusterRoles: %w", err)
	}
	if err := c.applyClusterRoleBindings(ctx, append(assets.CoreRBAC.ClusterRoleBindings, cloudRBAC.ClusterRoleBindings...)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ClusterRoleBindings: %w", err)
	}

	if err := c.applyDeployment(ctx, owner); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Deployment: %w", err)
	}

	return ctrl.Result{}, nil
}

func (c *Controller) getOperatorDeployment(ctx context.Context) (*appsv1.Deployment, error) {
	dep := &appsv1.Deployment{}
	if err := c.client.Get(ctx, client.ObjectKey{
		Namespace: c.config.Namespace,
		Name:      operatorDeploymentName,
	}, dep); err != nil {
		return nil, err
	}
	return dep, nil
}

func (c *Controller) applyServiceAccount(ctx context.Context, owner *appsv1.Deployment) error {
	sa := coreac.ServiceAccount(karpenterName, c.config.Namespace).
		WithOwnerReferences(ownerRef(owner))
	return c.client.Apply(ctx, sa, client.FieldOwner(fieldManager), client.ForceOwnership)
}

func (c *Controller) applyRoles(ctx context.Context, owner *appsv1.Deployment, roles []*rbacv1.Role) error {
	for _, desired := range roles {
		role := rbacac.Role(desired.Name, c.config.Namespace).
			WithOwnerReferences(ownerRef(owner)).
			WithRules(policyRules(desired.Rules)...)
		if err := c.client.Apply(ctx, role, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) applyRoleBindings(ctx context.Context, owner *appsv1.Deployment, bindings []*rbacv1.RoleBinding) error {
	for _, desired := range bindings {
		rb := rbacac.RoleBinding(desired.Name, c.config.Namespace).
			WithOwnerReferences(ownerRef(owner)).
			WithRoleRef(roleRef(desired.RoleRef)).
			WithSubjects(subjects(desired.Subjects, c.config.Namespace)...)
		if err := c.client.Apply(ctx, rb, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) applyClusterRoles(ctx context.Context, clusterRoles []*rbacv1.ClusterRole) error {
	for _, desired := range clusterRoles {
		cr := rbacac.ClusterRole(desired.Name).
			WithLabels(desired.Labels).
			WithRules(policyRules(desired.Rules)...)
		if desired.AggregationRule != nil {
			selectors := make([]*metaac.LabelSelectorApplyConfiguration, 0, len(desired.AggregationRule.ClusterRoleSelectors))
			for _, sel := range desired.AggregationRule.ClusterRoleSelectors {
				selectors = append(selectors, metaac.LabelSelector().WithMatchLabels(sel.MatchLabels))
			}
			cr = cr.WithAggregationRule(rbacac.AggregationRule().WithClusterRoleSelectors(selectors...))
		}
		if err := c.client.Apply(ctx, cr, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

func (c *Controller) applyClusterRoleBindings(ctx context.Context, bindings []*rbacv1.ClusterRoleBinding) error {
	for _, desired := range bindings {
		crb := rbacac.ClusterRoleBinding(desired.Name).
			WithRoleRef(roleRef(desired.RoleRef)).
			WithSubjects(subjects(desired.Subjects, c.config.Namespace)...)
		if err := c.client.Apply(ctx, crb, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

// TODO(maxcao13): currently if the aws binary doesn't detect valid AWS credentials, it will exit with an error, restarting the pod.
// On HCP, there exists an init container bundled with karpenter to wait until such a credential is mounted to the pod.
// We should consider adding a similar init container for both topologies, maybe by porting the HCP logic and container image here.
func (c *Controller) applyDeployment(ctx context.Context, owner *appsv1.Deployment) error {
	labels := map[string]string{"app": karpenterName}

	dep := appsac.Deployment(karpenterName, c.config.Namespace).
		WithOwnerReferences(ownerRef(owner)).
		WithSpec(appsac.DeploymentSpec().
			WithReplicas(1).
			WithSelector(metaac.LabelSelector().WithMatchLabels(labels)).
			WithTemplate(coreac.PodTemplateSpec().
				WithAnnotations(map[string]string{
					"target.workload.openshift.io/management": "{\"effect\": \"PreferredDuringScheduling\"}",
					"openshift.io/required-scc":               "restricted-v2",
				}).
				WithLabels(labels).
				WithSpec(c.karpenterPodSpec()),
			),
		)
	return c.client.Apply(ctx, dep, client.FieldOwner(fieldManager), client.ForceOwnership)
}

func (c *Controller) karpenterPodSpec() *coreac.PodSpecApplyConfiguration {
	cloudCfg := c.config.CloudProvider.OperandConfig()

	return coreac.PodSpec().
		WithPriorityClassName("system-node-critical").
		WithServiceAccountName(karpenterName).
		WithTerminationGracePeriodSeconds(10).
		WithSecurityContext(coreac.PodSecurityContext().
			WithRunAsNonRoot(true).
			WithSeccompProfile(coreac.SeccompProfile().
				WithType(corev1.SeccompProfileTypeRuntimeDefault)),
		).
		WithContainers(
			coreac.Container().
				WithName(karpenterName).
				WithImage(c.config.KarpenterImage).
				WithImagePullPolicy(c.imagePullPolicy).
				WithArgs("--log-level=debug"). // TODO(maxcao13): have this configurable
				WithEnv(c.karpenterEnv(cloudCfg)...).
				WithPorts(karpenterPorts()...).
				WithResources(coreac.ResourceRequirements().
					// TODO(maxcao13): arbitrary requests taken from upstream helm chart comments defaults
					// https://github.com/aws/karpenter-provider-aws/blob/c3f174308a64e3b96663914fddb74afb1c9f2069/charts/karpenter/values.yaml#L146
					// OpenShift convention says that we should not set limits:
					// https://github.com/openshift/origin/blob/b3b98a0b173664b3556b10a39775d5f97fec80d8/test/extended/operators/resources.go#L20
					// However, we should let admins override this if they so desire.
					WithRequests(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					}),
				).
				WithSecurityContext(coreac.SecurityContext().
					WithAllowPrivilegeEscalation(false).
					WithCapabilities(coreac.Capabilities().WithDrop(corev1.Capability("ALL"))),
				).
				WithTerminationMessagePolicy(corev1.TerminationMessageFallbackToLogsOnError).
				WithLivenessProbe(karpenterLivenessProbe()).
				WithReadinessProbe(karpenterReadinessProbe()).
				WithVolumeMounts(volumeMounts(cloudCfg.VolumeMounts)...),
		).
		WithVolumes(volumes(cloudCfg.Volumes)...)
}

func (c *Controller) karpenterEnv(cloudCfg common.OperandCloudConfig) []*coreac.EnvVarApplyConfiguration {
	env := []*coreac.EnvVarApplyConfiguration{
		coreac.EnvVar().WithName("SYSTEM_NAMESPACE").
			WithValueFrom(coreac.EnvVarSource().
				WithFieldRef(coreac.ObjectFieldSelector().WithFieldPath("metadata.namespace")),
			),
		coreac.EnvVar().WithName("CLUSTER_NAME").WithValue(c.config.ClusterName),
		coreac.EnvVar().WithName("CLUSTER_ENDPOINT").WithValue(c.config.ClusterEndpoint),
		coreac.EnvVar().WithName("DISABLE_WEBHOOK").WithValue("true"),
		// TODO(maxcao13): allow users to specify feature gates through a Karpenter CR.
		coreac.EnvVar().WithName("HEALTH_PROBE_PORT").WithValue("8081"),
	}
	return append(env, envVars(cloudCfg.Env)...)
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	cloudRBAC := c.config.CloudProvider.RBAC()
	managedClusterRoles := namesFromClusterRoles(append(assets.CoreRBAC.ClusterRoles, cloudRBAC.ClusterRoles...))
	managedClusterRoleBindings := namesFromClusterRoleBindings(append(assets.CoreRBAC.ClusterRoleBindings, cloudRBAC.ClusterRoleBindings...))

	reconcileRequest := []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: c.config.Namespace, Name: operatorDeploymentName}}}

	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		// TODO(maxcao13): when we get the Karpenter API object, we should watch that instead of the operator deployment.
		For(&appsv1.Deployment{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetNamespace() == c.config.Namespace && o.GetName() == operatorDeploymentName
		}))).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Watches(&rbacv1.ClusterRole{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				if !managedClusterRoles[o.GetName()] {
					return nil
				}
				return reconcileRequest
			},
		)).
		Watches(&rbacv1.ClusterRoleBinding{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				if !managedClusterRoleBindings[o.GetName()] {
					return nil
				}
				return reconcileRequest
			},
		)).
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				cloudCfg := c.config.CloudProvider.OperandConfig()
				if o.GetNamespace() != c.config.Namespace || o.GetName() != cloudCfg.CredentialsSecretName {
					return nil
				}
				return reconcileRequest
			},
		)).
		Complete(c)
}

// --- Owner reference helper ---

func ownerRef(owner *appsv1.Deployment) *metaac.OwnerReferenceApplyConfiguration {
	return metaac.OwnerReference().
		WithAPIVersion("apps/v1").
		WithKind("Deployment").
		WithName(owner.Name).
		WithUID(owner.UID).
		WithBlockOwnerDeletion(true).
		WithController(true)
}

// --- RBAC conversion helpers ---

func policyRules(rules []rbacv1.PolicyRule) []*rbacac.PolicyRuleApplyConfiguration {
	out := make([]*rbacac.PolicyRuleApplyConfiguration, len(rules))
	for i, r := range rules {
		out[i] = rbacac.PolicyRule().
			WithVerbs(r.Verbs...).
			WithAPIGroups(r.APIGroups...).
			WithResources(r.Resources...).
			WithResourceNames(r.ResourceNames...).
			WithNonResourceURLs(r.NonResourceURLs...)
	}
	return out
}

func roleRef(ref rbacv1.RoleRef) *rbacac.RoleRefApplyConfiguration {
	return rbacac.RoleRef().
		WithAPIGroup(ref.APIGroup).
		WithKind(ref.Kind).
		WithName(ref.Name)
}

func subjects(subs []rbacv1.Subject, ns string) []*rbacac.SubjectApplyConfiguration {
	out := make([]*rbacac.SubjectApplyConfiguration, len(subs))
	for i, s := range subs {
		sub := rbacac.Subject().
			WithKind(s.Kind).
			WithName(s.Name).
			WithAPIGroup(s.APIGroup)
		if s.Namespace != "" {
			sub.WithNamespace(s.Namespace)
		} else {
			sub.WithNamespace(ns)
		}
		out[i] = sub
	}
	return out
}

// --- Cloud config conversion helpers ---

func envVars(vars []corev1.EnvVar) []*coreac.EnvVarApplyConfiguration {
	out := make([]*coreac.EnvVarApplyConfiguration, len(vars))
	for i, e := range vars {
		ev := coreac.EnvVar().WithName(e.Name)
		if e.Value != "" {
			ev.WithValue(e.Value)
		}
		if e.ValueFrom != nil && e.ValueFrom.FieldRef != nil {
			ev.WithValueFrom(coreac.EnvVarSource().
				WithFieldRef(coreac.ObjectFieldSelector().WithFieldPath(e.ValueFrom.FieldRef.FieldPath)))
		}
		out[i] = ev
	}
	return out
}

func volumes(vols []corev1.Volume) []*coreac.VolumeApplyConfiguration {
	out := make([]*coreac.VolumeApplyConfiguration, len(vols))
	for i, v := range vols {
		vol := coreac.Volume().WithName(v.Name)
		if v.Secret != nil {
			vol.WithSecret(coreac.SecretVolumeSource().WithSecretName(v.Secret.SecretName))
		}
		out[i] = vol
	}
	return out
}

func volumeMounts(mounts []corev1.VolumeMount) []*coreac.VolumeMountApplyConfiguration {
	out := make([]*coreac.VolumeMountApplyConfiguration, len(mounts))
	for i, m := range mounts {
		out[i] = coreac.VolumeMount().
			WithName(m.Name).
			WithMountPath(m.MountPath).
			WithReadOnly(m.ReadOnly)
	}
	return out
}

// --- Name set helpers for SetupWithManager ---

func namesFromClusterRoles(roles []*rbacv1.ClusterRole) map[string]bool {
	m := make(map[string]bool, len(roles))
	for _, r := range roles {
		m[r.Name] = true
	}
	return m
}

func namesFromClusterRoleBindings(bindings []*rbacv1.ClusterRoleBinding) map[string]bool {
	m := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		m[b.Name] = true
	}
	return m
}

// --- Operand spec helpers ---

func karpenterPorts() []*coreac.ContainerPortApplyConfiguration {
	return []*coreac.ContainerPortApplyConfiguration{
		coreac.ContainerPort().WithName("metrics").WithContainerPort(8080),
		coreac.ContainerPort().WithName("http").WithContainerPort(8081).WithProtocol(corev1.ProtocolTCP),
	}
}

func karpenterLivenessProbe() *coreac.ProbeApplyConfiguration {
	return coreac.Probe().
		WithHTTPGet(coreac.HTTPGetAction().WithPath("/healthz").WithPort(intstr.FromInt32(8081))).
		WithInitialDelaySeconds(30).
		WithTimeoutSeconds(30)
}

func karpenterReadinessProbe() *coreac.ProbeApplyConfiguration {
	return coreac.Probe().
		WithHTTPGet(coreac.HTTPGetAction().WithPath("/readyz").WithPort(intstr.FromInt32(8081))).
		WithInitialDelaySeconds(5).
		WithTimeoutSeconds(30)
}

// TODO(maxcao13): remove before GA — only for dev/test iteration with :latest tags.
func operandImagePullPolicy() corev1.PullPolicy {
	if v := os.Getenv("DEV_IMAGE_PULL_POLICY"); v == "Always" {
		return corev1.PullAlways
	}
	return corev1.PullIfNotPresent
}
