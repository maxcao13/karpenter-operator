package operator

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	fakeconfigclient "github.com/openshift/client-go/config/clientset/versioned/fake"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testReleaseVersion = "4.19.0"

var testConfig = &ClusterOperatorControllerConfig{
	Namespace:      "openshift-karpenter",
	ReleaseVersion: testReleaseVersion,
}

func newTestController(configObjs ...runtime.Object) *ClusterOperatorController {
	return &ClusterOperatorController{
		client:       fakeclient.NewClientBuilder().Build(),
		configClient: fakeconfigclient.NewSimpleClientset(configObjs...),
		config:       testConfig,
	}
}

func TestReconcile(t *testing.T) {
	testCases := []struct {
		name               string
		existingCO         *configv1.ClusterOperator
		expectAvailable    configv1.ConditionStatus
		expectProgressing  configv1.ConditionStatus
		expectDegraded     configv1.ConditionStatus
		expectUpgradeable  configv1.ConditionStatus
		expectMessage      string
		expectVersion      string
		expectRelatedObjCt int
	}{
		{
			name:               "creates ClusterOperator and sets Available",
			existingCO:         nil,
			expectAvailable:    configv1.ConditionTrue,
			expectProgressing:  configv1.ConditionFalse,
			expectDegraded:     configv1.ConditionFalse,
			expectUpgradeable:  configv1.ConditionTrue,
			expectMessage:      "at version " + testReleaseVersion,
			expectVersion:      testReleaseVersion,
			expectRelatedObjCt: 4,
		},
		{
			name: "updates existing ClusterOperator to Available",
			existingCO: &configv1.ClusterOperator{
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
					},
				},
			},
			expectAvailable:    configv1.ConditionTrue,
			expectProgressing:  configv1.ConditionFalse,
			expectDegraded:     configv1.ConditionFalse,
			expectUpgradeable:  configv1.ConditionTrue,
			expectMessage:      "at version " + testReleaseVersion,
			expectVersion:      testReleaseVersion,
			expectRelatedObjCt: 4,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []runtime.Object
			if tc.existingCO != nil {
				objs = append(objs, tc.existingCO)
			}
			sc := newTestController(objs...)

			if _, err := sc.Reconcile(context.Background(), ctrl.Request{}); err != nil {
				t.Fatalf("Reconcile() returned error: %v", err)
			}

			co, err := sc.configClient.ConfigV1().ClusterOperators().Get(
				context.Background(), clusterOperatorName, metav1.GetOptions{},
			)
			if err != nil {
				t.Fatalf("failed to get ClusterOperator: %v", err)
			}

			assertCondition(t, co, configv1.OperatorAvailable, tc.expectAvailable)
			assertCondition(t, co, configv1.OperatorProgressing, tc.expectProgressing)
			assertCondition(t, co, configv1.OperatorDegraded, tc.expectDegraded)
			assertCondition(t, co, configv1.OperatorUpgradeable, tc.expectUpgradeable)

			if cond := findCondition(co.Status.Conditions, configv1.OperatorAvailable); cond != nil && cond.Message != tc.expectMessage {
				t.Errorf("expected Available message %q, got %q", tc.expectMessage, cond.Message)
			}

			if len(co.Status.Versions) != 1 || co.Status.Versions[0].Version != tc.expectVersion {
				t.Errorf("expected version %q, got %+v", tc.expectVersion, co.Status.Versions)
			}

			if len(co.Status.RelatedObjects) != tc.expectRelatedObjCt {
				t.Errorf("expected %d related objects, got %d", tc.expectRelatedObjCt, len(co.Status.RelatedObjects))
			}
		})
	}
}

func TestGetOrCreateClusterOperator(t *testing.T) {
	testCases := []struct {
		name       string
		existingCO *configv1.ClusterOperator
	}{
		{
			name:       "creates when not found",
			existingCO: nil,
		},
		{
			name: "returns existing",
			existingCO: &configv1.ClusterOperator{
				ObjectMeta: metav1.ObjectMeta{Name: clusterOperatorName},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var objs []runtime.Object
			if tc.existingCO != nil {
				objs = append(objs, tc.existingCO)
			}
			sc := newTestController(objs...)

			co, err := sc.getOrCreateClusterOperator(context.Background())
			if err != nil {
				t.Fatalf("getOrCreateClusterOperator() error: %v", err)
			}
			if co.Name != clusterOperatorName {
				t.Errorf("expected name %q, got %q", clusterOperatorName, co.Name)
			}
		})
	}
}

func TestConditionHelpers(t *testing.T) {
	testCases := []struct {
		name              string
		conditions        []configv1.ClusterOperatorStatusCondition
		expectAvailable   configv1.ConditionStatus
		expectProgressing configv1.ConditionStatus
		expectDegraded    configv1.ConditionStatus
		expectUpgradeable configv1.ConditionStatus
	}{
		{
			name:              "availableConditions",
			conditions:        availableConditions("AsExpected", "all good"),
			expectAvailable:   configv1.ConditionTrue,
			expectProgressing: configv1.ConditionFalse,
			expectDegraded:    configv1.ConditionFalse,
			expectUpgradeable: configv1.ConditionTrue,
		},
		{
			name:              "progressingConditions",
			conditions:        progressingConditions("Rolling", "rolling out"),
			expectAvailable:   configv1.ConditionTrue,
			expectProgressing: configv1.ConditionTrue,
			expectDegraded:    configv1.ConditionFalse,
			expectUpgradeable: configv1.ConditionTrue,
		},
		{
			name:              "degradedConditions",
			conditions:        degradedConditions("Broken", "something failed"),
			expectAvailable:   configv1.ConditionTrue,
			expectProgressing: configv1.ConditionFalse,
			expectDegraded:    configv1.ConditionTrue,
			expectUpgradeable: configv1.ConditionTrue,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			co := &configv1.ClusterOperator{Status: configv1.ClusterOperatorStatus{Conditions: tc.conditions}}
			assertCondition(t, co, configv1.OperatorAvailable, tc.expectAvailable)
			assertCondition(t, co, configv1.OperatorProgressing, tc.expectProgressing)
			assertCondition(t, co, configv1.OperatorDegraded, tc.expectDegraded)
			assertCondition(t, co, configv1.OperatorUpgradeable, tc.expectUpgradeable)
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
