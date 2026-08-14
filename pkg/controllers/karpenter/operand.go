package karpenter

import (
	"context"
	"strconv"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	appsac "k8s.io/client-go/applyconfigurations/apps/v1"
	coreac "k8s.io/client-go/applyconfigurations/core/v1"
	metaac "k8s.io/client-go/applyconfigurations/meta/v1"
	rbacac "k8s.io/client-go/applyconfigurations/rbac/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/samber/lo"
)

const (
	karpenterName = "karpenter"
	fieldManager  = "karpenter-operator"
)

const (
	defaultMetricsPort     = 8080
	defaultHealthProbePort = 8081

	metricsPortName     = "metrics"
	healthProbePortName = "http"

	karpenterPodTerminationGracePeriodSeconds = 10
	karpenterPodPriorityClassName             = "system-node-critical"

	// PriorityClassName is the pod priority class used by the karpenter operand.
	PriorityClassName = karpenterPodPriorityClassName
)

var defaultHealthProbePortStr = strconv.Itoa(defaultHealthProbePort)

// OperandConfig holds all parameters needed to build the karpenter operand resources.
// OCPController and HCPController populate this from their respective input objects.
type OperandConfig struct {
	Namespace                string
	KarpenterImage           string
	ClusterName              string
	ClusterEndpoint          string
	CloudProvider            common.CloudProvider
	ImagePullPolicy          corev1.PullPolicy
	LogLevelArg              string
	AdditionalLabels         map[string]string
	AdditionalEnv            []corev1.EnvVar
	AdditionalVolumeMounts   []corev1.VolumeMount
	AdditionalVolumes        []corev1.Volume
	AdditionalInitContainers []corev1.Container
}

// BuildDeployment constructs the karpenter Deployment apply configuration.
func BuildDeployment(cfg *OperandConfig, ownerRef *metaac.OwnerReferenceApplyConfiguration) *appsac.DeploymentApplyConfiguration {
	selectorLabels := map[string]string{"app": karpenterName}
	podLabels := lo.Assign(selectorLabels, cfg.AdditionalLabels)

	return appsac.Deployment(karpenterName, cfg.Namespace).
		WithOwnerReferences(ownerRef).
		WithSpec(appsac.DeploymentSpec().
			WithReplicas(1).
			WithSelector(metaac.LabelSelector().WithMatchLabels(selectorLabels)).
			WithTemplate(coreac.PodTemplateSpec().
				WithAnnotations(map[string]string{
					"target.workload.openshift.io/management": "{\"effect\": \"PreferredDuringScheduling\"}",
					"openshift.io/required-scc":               "restricted-v2",
				}).
				WithLabels(podLabels).
				WithSpec(BuildPodSpec(cfg)),
			),
		)
}

// BuildPodSpec constructs the karpenter operand pod spec.
func BuildPodSpec(cfg *OperandConfig) *coreac.PodSpecApplyConfiguration {
	cloudCfg := cfg.CloudProvider.OperandConfig()

	env := buildEnv(cfg, cloudCfg)
	mounts := append(volumeMounts(cloudCfg.VolumeMounts), volumeMounts(cfg.AdditionalVolumeMounts)...)
	vols := append(volumes(cloudCfg.Volumes), volumes(cfg.AdditionalVolumes)...)
	env = append(env, envVars(cfg.AdditionalEnv)...)

	return coreac.PodSpec().
		WithPriorityClassName(karpenterPodPriorityClassName).
		WithServiceAccountName(karpenterName).
		WithTerminationGracePeriodSeconds(karpenterPodTerminationGracePeriodSeconds).
		WithSecurityContext(coreac.PodSecurityContext().
			WithRunAsNonRoot(true).
			WithSeccompProfile(coreac.SeccompProfile().
				WithType(corev1.SeccompProfileTypeRuntimeDefault)),
		).
		WithInitContainers(initContainers(cfg.AdditionalInitContainers)...).
		WithContainers(
			coreac.Container().
				WithName(karpenterName).
				WithImage(cfg.KarpenterImage).
				WithImagePullPolicy(cfg.ImagePullPolicy).
				WithArgs(cfg.LogLevelArg).
				WithEnv(env...).
				WithPorts(karpenterPorts()...).
				WithResources(coreac.ResourceRequirements().
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
				WithVolumeMounts(mounts...),
		).
		WithVolumes(vols...)
}

func buildEnv(cfg *OperandConfig, cloudCfg common.OperandCloudConfig) []*coreac.EnvVarApplyConfiguration {
	env := []*coreac.EnvVarApplyConfiguration{
		coreac.EnvVar().WithName(common.SystemNamespaceEnvName).
			WithValueFrom(coreac.EnvVarSource().
				WithFieldRef(coreac.ObjectFieldSelector().WithFieldPath("metadata.namespace")),
			),
		coreac.EnvVar().WithName(common.ClusterNameEnvName).WithValue(cfg.ClusterName),
		coreac.EnvVar().WithName(common.ClusterEndpointEnvName).WithValue(cfg.ClusterEndpoint),
		coreac.EnvVar().WithName(common.DisableWebhookEnvName).WithValue("true"),
		coreac.EnvVar().WithName(common.HealthProbePortEnvName).WithValue(defaultHealthProbePortStr),
	}
	return append(env, envVars(cloudCfg.Env)...)
}

// ApplyServiceAccount applies the karpenter ServiceAccount.
func ApplyServiceAccount(ctx context.Context, cl client.Client, namespace string, ownerRef *metaac.OwnerReferenceApplyConfiguration) error {
	sa := coreac.ServiceAccount(karpenterName, namespace).
		WithOwnerReferences(ownerRef)
	return cl.Apply(ctx, sa, client.FieldOwner(fieldManager), client.ForceOwnership)
}

// ApplyRoles applies the given Roles in the specified namespace.
func ApplyRoles(ctx context.Context, cl client.Client, namespace string, ownerRef *metaac.OwnerReferenceApplyConfiguration, roles []*rbacv1.Role) error {
	for _, desired := range roles {
		role := rbacac.Role(desired.Name, namespace).
			WithOwnerReferences(ownerRef).
			WithRules(policyRules(desired.Rules)...)
		if err := cl.Apply(ctx, role, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

// ApplyRoleBindings applies the given RoleBindings in the specified namespace.
func ApplyRoleBindings(ctx context.Context, cl client.Client, namespace string, ownerRef *metaac.OwnerReferenceApplyConfiguration, bindings []*rbacv1.RoleBinding) error {
	for _, desired := range bindings {
		rb := rbacac.RoleBinding(desired.Name, namespace).
			WithOwnerReferences(ownerRef).
			WithRoleRef(roleRef(desired.RoleRef)).
			WithSubjects(subjects(desired.Subjects, namespace)...)
		if err := cl.Apply(ctx, rb, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

// ApplyClusterRoles applies the given ClusterRoles.
func ApplyClusterRoles(ctx context.Context, cl client.Client, ownerRef *metaac.OwnerReferenceApplyConfiguration, clusterRoles []*rbacv1.ClusterRole) error {
	for _, desired := range clusterRoles {
		cr := rbacac.ClusterRole(desired.Name).
			WithOwnerReferences(ownerRef).
			WithLabels(desired.Labels).
			WithRules(policyRules(desired.Rules)...)
		if desired.AggregationRule != nil {
			selectors := make([]*metaac.LabelSelectorApplyConfiguration, 0, len(desired.AggregationRule.ClusterRoleSelectors))
			for _, sel := range desired.AggregationRule.ClusterRoleSelectors {
				selectors = append(selectors, metaac.LabelSelector().WithMatchLabels(sel.MatchLabels))
			}
			cr = cr.WithAggregationRule(rbacac.AggregationRule().WithClusterRoleSelectors(selectors...))
		}
		if err := cl.Apply(ctx, cr, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

// ApplyClusterRoleBindings applies the given ClusterRoleBindings.
func ApplyClusterRoleBindings(ctx context.Context, cl client.Client, namespace string, ownerRef *metaac.OwnerReferenceApplyConfiguration, bindings []*rbacv1.ClusterRoleBinding) error {
	for _, desired := range bindings {
		crb := rbacac.ClusterRoleBinding(desired.Name).
			WithOwnerReferences(ownerRef).
			WithRoleRef(roleRef(desired.RoleRef)).
			WithSubjects(subjects(desired.Subjects, namespace)...)
		if err := cl.Apply(ctx, crb, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return err
		}
	}
	return nil
}

// ApplyDeployment applies the karpenter Deployment to the cluster.
func ApplyDeployment(ctx context.Context, cl client.Client, cfg *OperandConfig, ownerRef *metaac.OwnerReferenceApplyConfiguration) error {
	dep := BuildDeployment(cfg, ownerRef)
	return cl.Apply(ctx, dep, client.FieldOwner(fieldManager), client.ForceOwnership)
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
			src := coreac.SecretVolumeSource().WithSecretName(v.Secret.SecretName)
			if v.Secret.DefaultMode != nil {
				src.WithDefaultMode(*v.Secret.DefaultMode)
			}
			for _, item := range v.Secret.Items {
				ktp := coreac.KeyToPath().
					WithKey(item.Key).
					WithPath(item.Path)
				if item.Mode != nil {
					ktp.WithMode(*item.Mode)
				}
				src.WithItems(ktp)
			}
			vol.WithSecret(src)
		}
		if v.EmptyDir != nil {
			src := coreac.EmptyDirVolumeSource()
			if v.EmptyDir.Medium != "" {
				src.WithMedium(v.EmptyDir.Medium)
			}
			vol.WithEmptyDir(src)
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

func initContainers(containers []corev1.Container) []*coreac.ContainerApplyConfiguration {
	out := make([]*coreac.ContainerApplyConfiguration, len(containers))
	for i, c := range containers {
		container := coreac.Container().
			WithName(c.Name).
			WithImage(c.Image).
			WithCommand(c.Command...).
			WithArgs(c.Args...).
			WithImagePullPolicy(c.ImagePullPolicy).
			WithVolumeMounts(volumeMounts(c.VolumeMounts)...)
		if c.RestartPolicy != nil {
			container.WithRestartPolicy(*c.RestartPolicy)
		}
		if c.StartupProbe != nil {
			container.WithStartupProbe(containerProbe(c.StartupProbe))
		}
		if len(c.Resources.Requests) > 0 || len(c.Resources.Limits) > 0 {
			reqs := coreac.ResourceRequirements()
			if len(c.Resources.Requests) > 0 {
				reqs.WithRequests(c.Resources.Requests)
			}
			if len(c.Resources.Limits) > 0 {
				reqs.WithLimits(c.Resources.Limits)
			}
			container.WithResources(reqs)
		}
		out[i] = container
	}
	return out
}

func containerProbe(p *corev1.Probe) *coreac.ProbeApplyConfiguration {
	probe := coreac.Probe().
		WithPeriodSeconds(p.PeriodSeconds).
		WithFailureThreshold(p.FailureThreshold).
		WithSuccessThreshold(p.SuccessThreshold).
		WithTimeoutSeconds(p.TimeoutSeconds)
	if p.Exec != nil {
		probe.WithExec(coreac.ExecAction().WithCommand(p.Exec.Command...))
	}
	if p.HTTPGet != nil {
		probe.WithHTTPGet(coreac.HTTPGetAction().
			WithPath(p.HTTPGet.Path).
			WithPort(intstr.FromString(p.HTTPGet.Port.String())).
			WithScheme(p.HTTPGet.Scheme))
	}
	return probe
}

// --- Operand spec helpers ---

func karpenterPorts() []*coreac.ContainerPortApplyConfiguration {
	return []*coreac.ContainerPortApplyConfiguration{
		coreac.ContainerPort().WithName(metricsPortName).WithContainerPort(defaultMetricsPort),
		coreac.ContainerPort().WithName(healthProbePortName).WithContainerPort(defaultHealthProbePort).WithProtocol(corev1.ProtocolTCP),
	}
}

func karpenterLivenessProbe() *coreac.ProbeApplyConfiguration {
	return coreac.Probe().
		WithHTTPGet(coreac.HTTPGetAction().WithPath("/healthz").WithPort(intstr.FromInt(defaultHealthProbePort))).
		WithInitialDelaySeconds(30).
		WithTimeoutSeconds(30)
}

func karpenterReadinessProbe() *coreac.ProbeApplyConfiguration {
	return coreac.Probe().
		WithHTTPGet(coreac.HTTPGetAction().WithPath("/readyz").WithPort(intstr.FromInt(defaultHealthProbePort))).
		WithInitialDelaySeconds(5).
		WithTimeoutSeconds(30)
}
