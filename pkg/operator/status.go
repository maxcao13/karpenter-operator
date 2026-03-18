package operator

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configclient "github.com/openshift/client-go/config/clientset/versioned"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	clusterOperatorName = "karpenter"

	ReasonAsExpected       = "AsExpected"
	ReasonSyncing          = "SyncingResources"
	ReasonUnavailable      = "OperandUnavailable"
	ReasonCheckKarpenter   = "UnableToCheckKarpenter"
	DegradedCountThreshold = 3
	statusPollInterval     = 15 * time.Second
)

// StatusReporterConfig holds the settings for a StatusReporter.
type StatusReporterConfig struct {
	OperandName      string
	OperandNamespace string
	ReleaseVersion   string
	RelatedObjects   []configv1.ObjectReference
}

// StatusReporter reports operator status to the CVO via the ClusterOperator CR.
type StatusReporter struct {
	client       client.Client
	configClient configclient.Interface
	config       *StatusReporterConfig

	degradedCount int
}

var _ manager.Runnable = &StatusReporter{}

func NewStatusReporter(mgr manager.Manager, cfg *StatusReporterConfig) (*StatusReporter, error) {
	cc, err := configclient.NewForConfig(mgr.GetConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create config client: %w", err)
	}
	return &StatusReporter{
		client:       mgr.GetClient(),
		configClient: cc,
		config:       cfg,
	}, nil
}

// Start implements manager.Runnable. It periodically checks operand health
// and updates the ClusterOperator status until the context is cancelled.
func (r *StatusReporter) Start(ctx context.Context) error {
	ticker := time.NewTicker(statusPollInterval)
	defer ticker.Stop()

	// Report once immediately before entering the loop.
	r.reportStatus(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.reportStatus(ctx)
		}
	}
}

func (r *StatusReporter) reportStatus(ctx context.Context) {
	ok, err := r.checkKarpenter(ctx)
	if err != nil {
		klog.Errorf("StatusReporter: unable to check karpenter operand: %v", err)
		r.degradedCount++
		if r.degradedCount >= DegradedCountThreshold {
			if e := r.setDegraded(ctx, ReasonCheckKarpenter, fmt.Sprintf("Unable to check operand: %v", err)); e != nil {
				klog.Errorf("StatusReporter: failed to report degraded: %v", e)
			}
		}
		return
	}

	if !ok {
		r.degradedCount = 0
		if err := r.setProgressing(ctx, ReasonSyncing, "Waiting for karpenter operand to become ready"); err != nil {
			klog.Errorf("StatusReporter: failed to report progressing: %v", err)
		}
		return
	}

	r.degradedCount = 0
	if err := r.setAvailable(ctx, ReasonAsExpected, "Karpenter operand is available"); err != nil {
		klog.Errorf("StatusReporter: failed to report available: %v", err)
	}
}

// checkKarpenter returns true when the operand Deployment exists, has the
// expected number of updated and available replicas.
func (r *StatusReporter) checkKarpenter(ctx context.Context) (bool, error) {
	deploy := &appsv1.Deployment{}
	key := types.NamespacedName{
		Namespace: r.config.OperandNamespace,
		Name:      r.config.OperandName,
	}
	if err := r.client.Get(ctx, key, deploy); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	if deploy.Status.UpdatedReplicas != deploy.Status.Replicas {
		return false, nil
	}
	if deploy.Status.AvailableReplicas < 1 {
		return false, nil
	}
	return true, nil
}

func (r *StatusReporter) getOrCreateClusterOperator(ctx context.Context) (*configv1.ClusterOperator, error) {
	co, err := r.configClient.ConfigV1().ClusterOperators().Get(ctx, clusterOperatorName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		co = &configv1.ClusterOperator{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterOperatorName,
			},
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

func (r *StatusReporter) applyStatus(ctx context.Context, conditions []configv1.ClusterOperatorStatusCondition) error {
	co, err := r.getOrCreateClusterOperator(ctx)
	if err != nil {
		return err
	}

	now := metav1.Now()
	desiredVersions := []configv1.OperandVersion{
		{Name: "operator", Version: r.config.ReleaseVersion},
	}

	newConditions := make([]configv1.ClusterOperatorStatusCondition, len(conditions))
	for i, c := range conditions {
		c.LastTransitionTime = now
		existing := findCondition(co.Status.Conditions, c.Type)
		if existing != nil && existing.Status == c.Status {
			c.LastTransitionTime = existing.LastTransitionTime
		}
		newConditions[i] = c
	}

	desiredStatus := configv1.ClusterOperatorStatus{
		Conditions:     newConditions,
		Versions:       desiredVersions,
		RelatedObjects: r.config.RelatedObjects,
	}

	if equality.Semantic.DeepEqual(co.Status, desiredStatus) {
		return nil
	}

	co.Status = desiredStatus
	_, err = r.configClient.ConfigV1().ClusterOperators().UpdateStatus(ctx, co, metav1.UpdateOptions{})
	return err
}

func (r *StatusReporter) setAvailable(ctx context.Context, reason, message string) error {
	return r.applyStatus(ctx, []configv1.ClusterOperatorStatusCondition{
		newCondition(configv1.OperatorAvailable, configv1.ConditionTrue, reason, message),
		newCondition(configv1.OperatorProgressing, configv1.ConditionFalse, reason, message),
		newCondition(configv1.OperatorDegraded, configv1.ConditionFalse, reason, ""),
		newCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected, ""),
	})
}

func (r *StatusReporter) setProgressing(ctx context.Context, reason, message string) error {
	return r.applyStatus(ctx, []configv1.ClusterOperatorStatusCondition{
		newCondition(configv1.OperatorAvailable, configv1.ConditionTrue, ReasonAsExpected, ""),
		newCondition(configv1.OperatorProgressing, configv1.ConditionTrue, reason, message),
		newCondition(configv1.OperatorDegraded, configv1.ConditionFalse, ReasonAsExpected, ""),
		newCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected, ""),
	})
}

func (r *StatusReporter) setDegraded(ctx context.Context, reason, message string) error {
	return r.applyStatus(ctx, []configv1.ClusterOperatorStatusCondition{
		newCondition(configv1.OperatorAvailable, configv1.ConditionTrue, ReasonAsExpected, ""),
		newCondition(configv1.OperatorProgressing, configv1.ConditionFalse, ReasonAsExpected, ""),
		newCondition(configv1.OperatorDegraded, configv1.ConditionTrue, reason, message),
		newCondition(configv1.OperatorUpgradeable, configv1.ConditionTrue, ReasonAsExpected, ""),
	})
}

func newCondition(condType configv1.ClusterStatusConditionType, status configv1.ConditionStatus, reason, message string) configv1.ClusterOperatorStatusCondition {
	return configv1.ClusterOperatorStatusCondition{
		Type:    condType,
		Status:  status,
		Reason:  reason,
		Message: message,
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
