package operator

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const clusterOperatorName = "karpenter"

type ClusterOperatorControllerConfig struct {
	Namespace      string
	ReleaseVersion string
}

type ClusterOperatorController struct {
	client       client.Client
	configClient configclient.Interface
	config       *ClusterOperatorControllerConfig
}

func NewClusterOperatorController(mgr ctrl.Manager, cfg *ClusterOperatorControllerConfig) (*ClusterOperatorController, error) {
	cc, err := configclient.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create config client: %w", err)
	}
	return &ClusterOperatorController{
		client:       mgr.GetClient(),
		configClient: cc,
		config:       cfg,
	}, nil
}

func (r *ClusterOperatorController) Name() string {
	return "clusteroperator"
}

// TODO(maxcao13): For now, we always report Available. We should base it off operand deployment status later.
func (r *ClusterOperatorController) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling ClusterOperator status")

	conditions := availableConditions("AsExpected", fmt.Sprintf("at version %s", r.config.ReleaseVersion))

	if err := r.applyStatus(ctx, conditions); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update ClusterOperator status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *ClusterOperatorController) applyStatus(ctx context.Context, conditions []configv1.ClusterOperatorStatusCondition) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}

	now := metav1.Now()
	for i, c := range conditions {
		conditions[i].LastTransitionTime = now
		if existing := findCondition(co.Status.Conditions, c.Type); existing != nil && existing.Status == c.Status {
			conditions[i].LastTransitionTime = existing.LastTransitionTime
		}
	}

	desired := configv1.ClusterOperatorStatus{
		Conditions: conditions,
		Versions: []configv1.OperandVersion{
			{Name: "operator", Version: r.config.ReleaseVersion},
		},
		RelatedObjects: r.relatedObjects(),
	}

	if equality.Semantic.DeepEqual(co.Status, desired) {
		return nil
	}

	co.Status = desired
	_, err = r.configClient.ConfigV1().ClusterOperators().UpdateStatus(ctx, co, metav1.UpdateOptions{})
	return err
}

func (r *ClusterOperatorController) getOrCreateClusterOperator(ctx context.Context) (*configv1.ClusterOperator, error) {
	co, err := r.configClient.ConfigV1().ClusterOperators().Get(ctx, clusterOperatorName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		co = &configv1.ClusterOperator{
			ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName},
		}
		co, err = r.configClient.ConfigV1().ClusterOperators().Create(ctx, co, metav1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create ClusterOperator: %w", err)
		}
	} else if err != nil {
		return nil, err
	}
	return co, nil
}

func (r *ClusterOperatorController) relatedObjects() []configv1.ObjectReference {
	return []configv1.ObjectReference{
		{Group: "", Resource: "namespaces", Name: r.config.Namespace},
		{Group: "apps", Resource: "deployments", Name: "karpenter-operator", Namespace: r.config.Namespace},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "karpenter-operator"},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "karpenter-operator"},
	}
}

func (r *ClusterOperatorController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named(r.Name()).
		Watches(&configv1.ClusterOperator{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				if o.GetName() != clusterOperatorName {
					return nil
				}
				return []ctrl.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
			},
		)).
		Watches(&appsv1.Deployment{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				if o.GetNamespace() != r.config.Namespace || o.GetName() != "karpenter" {
					return nil
				}
				return []ctrl.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
			},
		)).
		Complete(r)
}

func availableConditions(reason, message string) []configv1.ClusterOperatorStatusCondition {
	return []configv1.ClusterOperatorStatusCondition{
		{Type: configv1.OperatorAvailable, Status: configv1.ConditionTrue, Reason: reason, Message: message},
		{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse, Reason: reason},
		{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse, Reason: reason},
		{Type: configv1.OperatorUpgradeable, Status: configv1.ConditionTrue, Reason: reason},
	}
}

func progressingConditions(reason, message string) []configv1.ClusterOperatorStatusCondition {
	return []configv1.ClusterOperatorStatusCondition{
		{Type: configv1.OperatorAvailable, Status: configv1.ConditionTrue, Reason: "AsExpected"},
		{Type: configv1.OperatorProgressing, Status: configv1.ConditionTrue, Reason: reason, Message: message},
		{Type: configv1.OperatorDegraded, Status: configv1.ConditionFalse, Reason: "AsExpected"},
		{Type: configv1.OperatorUpgradeable, Status: configv1.ConditionTrue, Reason: "AsExpected"},
	}
}

func degradedConditions(reason, message string) []configv1.ClusterOperatorStatusCondition {
	return []configv1.ClusterOperatorStatusCondition{
		{Type: configv1.OperatorAvailable, Status: configv1.ConditionTrue, Reason: "AsExpected"},
		{Type: configv1.OperatorProgressing, Status: configv1.ConditionFalse, Reason: "AsExpected"},
		{Type: configv1.OperatorDegraded, Status: configv1.ConditionTrue, Reason: reason, Message: message},
		{Type: configv1.OperatorUpgradeable, Status: configv1.ConditionTrue, Reason: "AsExpected"},
	}
}

func findCondition(conditions []configv1.ClusterOperatorStatusCondition, condType configv1.ClusterStatusConditionType) *configv1.ClusterOperatorStatusCondition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
