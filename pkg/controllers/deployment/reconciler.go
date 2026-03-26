package deployment

import (
	"context"
	"fmt"
	"os"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/types"
)

const (
	karpenterName = "karpenter"
)

// CloudConfigProvider is the subset of cloud.CloudProvider the deployment
// reconciler needs. Defined here to avoid importing the full cloud package.
type CloudConfigProvider interface {
	OperandConfig() types.OperandCloudConfig
}

// Reconciler deploys karpenter core (Deployment, ServiceAccount, Role, RoleBinding).
// All operand resources are owned by the operator Deployment so that
// Kubernetes garbage collection cleans them up if the operator is removed.
type Reconciler struct {
	Client          client.Client
	Scheme          *runtime.Scheme
	Namespace       string
	KarpenterImage  string
	ClusterName     string
	ClusterEndpoint string
	CloudProvider   CloudConfigProvider
}

func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	r.Client = mgr.GetClient()
	r.Scheme = mgr.GetScheme()

	c, err := controller.New("karpenter-deployment", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return fmt.Errorf("failed to construct karpenter-deployment controller: %w", err)
	}

	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, o client.Object) []ctrl.Request {
			if o.GetNamespace() != r.Namespace || o.GetName() != karpenterName {
				return nil
			}
			return []ctrl.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
		},
	))); err != nil {
		return fmt.Errorf("failed to watch Deployment: %w", err)
	}

	cloudCfg := r.CloudProvider.OperandConfig()
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, o client.Object) []ctrl.Request {
			if o.GetNamespace() != r.Namespace || o.GetName() != cloudCfg.CredentialsSecretName {
				return nil
			}
			return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: r.Namespace, Name: karpenterName}}}
		},
	))); err != nil {
		return fmt.Errorf("failed to watch credentials Secret: %w", err)
	}

	initialSync := make(chan event.GenericEvent)
	if err := c.Watch(source.Channel(initialSync, &handler.EnqueueRequestForObject{})); err != nil {
		return fmt.Errorf("failed to watch initial sync channel: %w", err)
	}
	go func() {
		initialSync <- event.GenericEvent{Object: &appsv1.Deployment{}}
	}()

	return nil
}

const operatorDeploymentName = "karpenter-operator"

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Reconciling karpenter deployment", "req", req)

	owner, err := r.getOperatorDeployment(ctx)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get operator deployment: %w", err)
	}

	if err := r.reconcileServiceAccount(ctx, owner); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ServiceAccount: %w", err)
	}

	if err := r.reconcileRole(ctx, owner); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Role: %w", err)
	}

	if err := r.reconcileRoleBinding(ctx, owner); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile RoleBinding: %w", err)
	}

	if err := r.reconcileClusterRole(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ClusterRole: %w", err)
	}

	if err := r.reconcileClusterRoleBinding(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile ClusterRoleBinding: %w", err)
	}

	cloudCfg := r.CloudProvider.OperandConfig()
	if cloudCfg.CredentialsSecretName != "" {
		secret := &corev1.Secret{}
		err := r.Client.Get(ctx, client.ObjectKey{Namespace: r.Namespace, Name: cloudCfg.CredentialsSecretName}, secret)
		if errors.IsNotFound(err) {
			log.Info("Waiting for cloud credentials secret before creating operand", "secret", cloudCfg.CredentialsSecretName)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to check credentials secret: %w", err)
		}
	}

	if err := r.reconcileDeployment(ctx, owner); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Deployment: %w", err)
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) getOperatorDeployment(ctx context.Context) (*appsv1.Deployment, error) {
	dep := &appsv1.Deployment{}
	if err := r.Client.Get(ctx, client.ObjectKey{
		Namespace: r.Namespace,
		Name:      operatorDeploymentName,
	}, dep); err != nil {
		return nil, err
	}
	return dep, nil
}

func (r *Reconciler) reconcileServiceAccount(ctx context.Context, owner *appsv1.Deployment) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      karpenterName,
			Namespace: r.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		return controllerutil.SetControllerReference(owner, sa, r.Scheme)
	})
	return err
}

func (r *Reconciler) reconcileRole(ctx context.Context, owner *appsv1.Deployment) error {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      karpenterName,
			Namespace: r.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, role, func() error {
		role.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"coordination.k8s.io"},
				Resources: []string{"leases"},
				Verbs:     []string{"get", "watch", "create"},
			},
			{
				APIGroups:     []string{"coordination.k8s.io"},
				Resources:     []string{"leases"},
				Verbs:         []string{"patch", "update"},
				ResourceNames: []string{"karpenter-leader-election"},
			},
		}
		return controllerutil.SetControllerReference(owner, role, r.Scheme)
	})
	return err
}

func (r *Reconciler) reconcileRoleBinding(ctx context.Context, owner *appsv1.Deployment) error {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      karpenterName,
			Namespace: r.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, rb, func() error {
		rb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     karpenterName,
		}
		rb.Subjects = []rbacv1.Subject{
			{
				Kind: "ServiceAccount",
				Name: karpenterName,
			},
		}
		return controllerutil.SetControllerReference(owner, rb, r.Scheme)
	})
	return err
}

func (r *Reconciler) reconcileClusterRole(ctx context.Context) error {
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: karpenterName,
		},
	}

	cloudCfg := r.CloudProvider.OperandConfig()

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cr, func() error {
		rules := []rbacv1.PolicyRule{
			{
				APIGroups: []string{"karpenter.sh"},
				Resources: []string{"nodepools", "nodepools/status", "nodeclaims", "nodeclaims/status"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "nodes", "persistentvolumes", "persistentvolumeclaims", "replicationcontrollers", "namespaces"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"storageclasses", "csinodes", "volumeattachments"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{"daemonsets", "deployments", "replicasets", "statefulsets"},
				Verbs:     []string{"list", "watch"},
			},
			{
				APIGroups: []string{"policy"},
				Resources: []string{"poddisruptionbudgets"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"get", "list", "watch", "create", "patch"},
			},
			{
				APIGroups: []string{"karpenter.sh"},
				Resources: []string{"nodeclaims", "nodeclaims/status", "nodeclaims/finalizers"},
				Verbs:     []string{"create", "delete", "update", "patch"},
			},
			{
				APIGroups: []string{"karpenter.sh"},
				Resources: []string{"nodepools", "nodepools/status", "nodepools/finalizers"},
				Verbs:     []string{"update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"nodes", "nodes/finalizers"},
				Verbs:     []string{"patch", "delete", "update"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods/eviction"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"services"},
				Verbs:     []string{"get", "list"},
			},
		}
		rules = append(rules, cloudCfg.RBACRules...)
		cr.Rules = rules
		return nil
	})
	return err
}

func (r *Reconciler) reconcileClusterRoleBinding(ctx context.Context) error {
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: karpenterName,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, crb, func() error {
		crb.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     karpenterName,
		}
		crb.Subjects = []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      karpenterName,
				Namespace: r.Namespace,
			},
		}
		return nil
	})
	return err
}

func (r *Reconciler) reconcileDeployment(ctx context.Context, owner *appsv1.Deployment) error {
	labels := map[string]string{
		"app": karpenterName,
	}

	operatorImage := owner.Spec.Template.Spec.Containers[0].Image

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      karpenterName,
			Namespace: r.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		deployment.Spec = appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: r.karpenterPodSpec(operatorImage),
			},
		}
		return controllerutil.SetControllerReference(owner, deployment, r.Scheme)
	})
	return err
}

func (r *Reconciler) karpenterPodSpec(operatorImage string) corev1.PodSpec {
	cloudCfg := r.CloudProvider.OperandConfig()

	return corev1.PodSpec{
		ServiceAccountName:            karpenterName,
		TerminationGracePeriodSeconds: ptr.To(int64(10)),
		InitContainers: []corev1.Container{
			{
				Name:            "check-credentials",
				Image:           operatorImage,
				ImagePullPolicy: operandImagePullPolicy(),
				Command:         []string{"/usr/bin/karpenter-operator", "check-credentials"},
				Env:             cloudCfg.Env,
				VolumeMounts:    cloudCfg.VolumeMounts,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("10m"),
						corev1.ResourceMemory: resource.MustParse("30Mi"),
					},
				},
			},
		},
		Containers: []corev1.Container{
			{
				Name:            karpenterName,
				Image:           r.KarpenterImage,
				ImagePullPolicy: operandImagePullPolicy(),
				Args:            []string{"--log-level=debug"},
				Env:             r.karpenterEnv(cloudCfg),
				Ports:           karpenterPorts(),
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
				LivenessProbe:  karpenterLivenessProbe(),
				ReadinessProbe: karpenterReadinessProbe(),
				VolumeMounts:   cloudCfg.VolumeMounts,
			},
		},
		Volumes: cloudCfg.Volumes,
	}
}

func (r *Reconciler) karpenterEnv(cloudCfg types.OperandCloudConfig) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{
			Name: "SYSTEM_NAMESPACE",
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					FieldPath: "metadata.namespace",
				},
			},
		},
		{Name: "CLUSTER_NAME", Value: r.ClusterName},
		{Name: "CLUSTER_ENDPOINT", Value: r.ClusterEndpoint},
		{Name: "DISABLE_WEBHOOK", Value: "true"},
		{Name: "FEATURE_GATES", Value: "Drift=true"},
		{Name: "HEALTH_PROBE_PORT", Value: "8081"},
	}
	env = append(env, cloudCfg.Env...)
	return env
}

func karpenterPorts() []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{Name: "metrics", ContainerPort: 8080},
		{Name: "http", ContainerPort: 8081, Protocol: corev1.ProtocolTCP},
	}
}

func karpenterLivenessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/healthz",
				Port: intstr.FromInt32(8081),
			},
		},
		InitialDelaySeconds: 30,
		TimeoutSeconds:      30,
	}
}

func karpenterReadinessProbe() *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/readyz",
				Port: intstr.FromInt32(8081),
			},
		},
		InitialDelaySeconds: 5,
		TimeoutSeconds:      30,
	}
}

// TODO(dev): remove before GA — this is only for dev/test iteration with :latest tags.
func operandImagePullPolicy() corev1.PullPolicy {
	if v := os.Getenv("DEV_IMAGE_PULL_POLICY"); v == "Always" {
		return corev1.PullAlways
	}
	return corev1.PullIfNotPresent
}
