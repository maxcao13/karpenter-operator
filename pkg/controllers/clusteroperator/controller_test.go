package clusteroperator

import (
	"context"
	"testing"
	"time"

	autoscalingv1alpha1 "github.com/openshift/karpenter-operator/pkg/apis/autoscaling/v1alpha1"

	configv1 "github.com/openshift/api/config/v1"
	configac "github.com/openshift/client-go/config/applyconfigurations/config/v1"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	testNamespace      = "openshift-karpenter"
	testReleaseVersion = "4.19.0"
)

var testConfig = &ControllerConfig{
	Namespace:      testNamespace,
	ReleaseVersion: testReleaseVersion,
}

var testKarpenterCR = &autoscalingv1alpha1.Karpenter{
	ObjectMeta: metav1.ObjectMeta{Name: autoscalingv1alpha1.SingletonName},
}

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = appsv1.AddToScheme(s)
	_ = configv1.Install(s)
	_ = autoscalingv1alpha1.AddToScheme(s)
	return s
}

func newTestController(objs ...client.Object) *Controller {
	return &Controller{
		client: fakeclient.NewClientBuilder().
			WithScheme(testScheme()).
			WithObjects(objs...).
			WithStatusSubresource(&configv1.ClusterOperator{}).
			Build(),
		config: testConfig,
	}
}

func TestReconcile(t *testing.T) { //nolint:gocyclo
	testCases := []struct {
		name               string
		objs               []client.Object
		expectAvailable    configv1.ConditionStatus
		expectProgressing  configv1.ConditionStatus
		expectDegraded     configv1.ConditionStatus
		expectUpgradeable  configv1.ConditionStatus
		expectMessageOn    configv1.ClusterStatusConditionType
		expectMessage      string
		expectVersion      string
		expectRelatedObjCt int
	}{
		{
			name:               "no Karpenter CR — reports Available",
			objs:               nil,
			expectAvailable:    configv1.ConditionTrue,
			expectProgressing:  configv1.ConditionFalse,
			expectDegraded:     configv1.ConditionFalse,
			expectUpgradeable:  configv1.ConditionTrue,
			expectMessageOn:    configv1.OperatorAvailable,
			expectMessage:      "at version " + testReleaseVersion,
			expectVersion:      testReleaseVersion,
			expectRelatedObjCt: 6,
		},
		{
			name:               "Karpenter CR exists, Deployment not found — reports Progressing",
			objs:               []client.Object{testKarpenterCR},
			expectAvailable:    configv1.ConditionTrue,
			expectProgressing:  configv1.ConditionTrue,
			expectDegraded:     configv1.ConditionFalse,
			expectUpgradeable:  configv1.ConditionTrue,
			expectMessageOn:    configv1.OperatorProgressing,
			expectMessage:      "Waiting for karpenter Deployment to be created",
			expectVersion:      testReleaseVersion,
			expectRelatedObjCt: 6,
		},
		{
			name: "operand Deployment not ready — reports Progressing",
			objs: []client.Object{
				testKarpenterCR,
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "karpenter", Namespace: testNamespace},
					Status:     appsv1.DeploymentStatus{Replicas: 1, AvailableReplicas: 0},
				},
			},
			expectAvailable:    configv1.ConditionTrue,
			expectProgressing:  configv1.ConditionTrue,
			expectDegraded:     configv1.ConditionFalse,
			expectUpgradeable:  configv1.ConditionTrue,
			expectMessageOn:    configv1.OperatorProgressing,
			expectMessage:      "Waiting for karpenter Deployment to become available",
			expectVersion:      testReleaseVersion,
			expectRelatedObjCt: 6,
		},
		{
			name: "operand Deployment rolling out — reports Progressing",
			objs: []client.Object{
				testKarpenterCR,
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "karpenter", Namespace: testNamespace},
					Status:     appsv1.DeploymentStatus{Replicas: 2, AvailableReplicas: 1, UpdatedReplicas: 1},
				},
			},
			expectAvailable:    configv1.ConditionTrue,
			expectProgressing:  configv1.ConditionTrue,
			expectDegraded:     configv1.ConditionFalse,
			expectUpgradeable:  configv1.ConditionTrue,
			expectMessageOn:    configv1.OperatorProgressing,
			expectMessage:      "Karpenter Deployment is rolling out",
			expectVersion:      testReleaseVersion,
			expectRelatedObjCt: 6,
		},
		{
			name: "operand Deployment healthy — reports Available",
			objs: []client.Object{
				testKarpenterCR,
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "karpenter", Namespace: testNamespace},
					Status:     appsv1.DeploymentStatus{Replicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1},
				},
			},
			expectAvailable:    configv1.ConditionTrue,
			expectProgressing:  configv1.ConditionFalse,
			expectDegraded:     configv1.ConditionFalse,
			expectUpgradeable:  configv1.ConditionTrue,
			expectMessageOn:    configv1.OperatorAvailable,
			expectMessage:      "at version " + testReleaseVersion,
			expectVersion:      testReleaseVersion,
			expectRelatedObjCt: 6,
		},
		{
			name: "updates existing ClusterOperator",
			objs: []client.Object{
				testKarpenterCR,
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "karpenter", Namespace: testNamespace},
					Status:     appsv1.DeploymentStatus{Replicas: 1, AvailableReplicas: 1, UpdatedReplicas: 1},
				},
				&configv1.ClusterOperator{
					ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName},
					Status: configv1.ClusterOperatorStatus{
						Conditions: []configv1.ClusterOperatorStatusCondition{
							{
								Type:               configv1.OperatorAvailable,
								Status:             configv1.ConditionFalse,
								LastTransitionTime: metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
							},
							{
								Type:               configv1.OperatorDegraded,
								Status:             configv1.ConditionTrue,
								Reason:             "SomePreviousError",
								LastTransitionTime: metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
							},
							{
								// Upgradeable=True matches what reconcile will produce,
								// so LastTransitionTime must be preserved.
								Type:               configv1.OperatorUpgradeable,
								Status:             configv1.ConditionTrue,
								Reason:             "AsExpected",
								LastTransitionTime: metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
							},
						},
					},
				},
			},
			expectAvailable:    configv1.ConditionTrue,
			expectProgressing:  configv1.ConditionFalse,
			expectDegraded:     configv1.ConditionFalse,
			expectUpgradeable:  configv1.ConditionTrue,
			expectMessageOn:    configv1.OperatorAvailable,
			expectMessage:      "at version " + testReleaseVersion,
			expectVersion:      testReleaseVersion,
			expectRelatedObjCt: 6,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sc := newTestController(tc.objs...)

			if _, err := sc.Reconcile(context.Background(), ctrl.Request{}); err != nil {
				t.Fatalf("Reconcile() returned error: %v", err)
			}

			co := &configv1.ClusterOperator{}
			if err := sc.client.Get(context.Background(), client.ObjectKey{Name: clusterOperatorName}, co); err != nil {
				t.Fatalf("failed to get ClusterOperator: %v", err)
			}

			assertCondition(t, co, configv1.OperatorAvailable, tc.expectAvailable)
			assertCondition(t, co, configv1.OperatorProgressing, tc.expectProgressing)
			assertCondition(t, co, configv1.OperatorDegraded, tc.expectDegraded)
			assertCondition(t, co, configv1.OperatorUpgradeable, tc.expectUpgradeable)

			if cond := findCondition(co.Status.Conditions, tc.expectMessageOn); cond == nil {
				t.Errorf("condition %s not found for message check", tc.expectMessageOn)
			} else if cond.Message != tc.expectMessage {
				t.Errorf("expected %s message %q, got %q", tc.expectMessageOn, tc.expectMessage, cond.Message)
			}

			if len(co.Status.Versions) != 1 || co.Status.Versions[0].Version != tc.expectVersion {
				t.Errorf("expected version %q, got %+v", tc.expectVersion, co.Status.Versions)
			}

			if len(co.Status.RelatedObjects) != tc.expectRelatedObjCt {
				t.Errorf("expected %d related objects, got %d", tc.expectRelatedObjCt, len(co.Status.RelatedObjects))
			}

			if tc.name == "updates existing ClusterOperator" {
				seeded := metav1.NewTime(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

				// Available changed False→True: timestamp must advance.
				if cond := findCondition(co.Status.Conditions, configv1.OperatorAvailable); cond != nil {
					if !cond.LastTransitionTime.After(seeded.Time) {
						t.Errorf("Available changed status but LastTransitionTime was not updated: got %v", cond.LastTransitionTime)
					}
				}

				// Upgradeable stayed True→True: timestamp must be preserved.
				if cond := findCondition(co.Status.Conditions, configv1.OperatorUpgradeable); cond != nil {
					if !cond.LastTransitionTime.Equal(&seeded) {
						t.Errorf("Upgradeable status unchanged but LastTransitionTime changed: got %v, want %v", cond.LastTransitionTime, seeded)
					}
				}
			}
		})
	}
}

func TestConditionHelpers(t *testing.T) {
	type expectedCondition struct {
		status configv1.ConditionStatus
		reason string
	}

	testCases := []struct {
		name       string
		conditions []*configac.ClusterOperatorStatusConditionApplyConfiguration
		expect     map[configv1.ClusterStatusConditionType]expectedCondition
	}{
		{
			name:       "availableConditions",
			conditions: availableConditions("KarpenterNotFound", "all good"),
			expect: map[configv1.ClusterStatusConditionType]expectedCondition{
				configv1.OperatorAvailable:   {configv1.ConditionTrue, "KarpenterNotFound"},
				configv1.OperatorProgressing: {configv1.ConditionFalse, "AsExpected"},
				configv1.OperatorDegraded:    {configv1.ConditionFalse, "AsExpected"},
				configv1.OperatorUpgradeable: {configv1.ConditionTrue, "AsExpected"},
			},
		},
		{
			name:       "progressingConditions",
			conditions: progressingConditions("Rolling", "rolling out"),
			expect: map[configv1.ClusterStatusConditionType]expectedCondition{
				configv1.OperatorAvailable:   {configv1.ConditionTrue, "AsExpected"},
				configv1.OperatorProgressing: {configv1.ConditionTrue, "Rolling"},
				configv1.OperatorDegraded:    {configv1.ConditionFalse, "AsExpected"},
				configv1.OperatorUpgradeable: {configv1.ConditionTrue, "AsExpected"},
			},
		},
		{
			name:       "degradedConditions",
			conditions: degradedConditions("Broken", "something failed"),
			expect: map[configv1.ClusterStatusConditionType]expectedCondition{
				configv1.OperatorAvailable:   {configv1.ConditionTrue, "AsExpected"},
				configv1.OperatorProgressing: {configv1.ConditionFalse, "AsExpected"},
				configv1.OperatorDegraded:    {configv1.ConditionTrue, "Broken"},
				configv1.OperatorUpgradeable: {configv1.ConditionTrue, "AsExpected"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, c := range tc.conditions {
				condType := *c.Type
				want, ok := tc.expect[condType]
				if !ok {
					t.Fatalf("unexpected condition type: %s", condType)
				}
				if *c.Status != want.status {
					t.Errorf("%s status: got %s, want %s", condType, *c.Status, want.status)
				}
				if *c.Reason != want.reason {
					t.Errorf("%s reason: got %q, want %q", condType, *c.Reason, want.reason)
				}
			}
		})
	}
}

func assertCondition(t *testing.T, co *configv1.ClusterOperator, condType configv1.ClusterStatusConditionType, expected configv1.ConditionStatus) {
	t.Helper()
	cond := findCondition(co.Status.Conditions, condType)
	if cond == nil {
		t.Errorf("condition %s not found", condType)
		return
	}
	if cond.Status != expected {
		t.Errorf("condition %s: got %s, want %s", condType, cond.Status, expected)
	}
}
