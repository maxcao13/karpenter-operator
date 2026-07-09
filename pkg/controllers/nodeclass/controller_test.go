package nodeclass

import (
	"context"
	"fmt"
	"testing"

	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type mockReconciler struct {
	calls []mockCall
	err   error
}

type mockCall struct {
	infraName string
	name      string
}

func (m *mockReconciler) ReconcileNodeClass(_ context.Context, _ client.Client, infraName string, name string) error {
	m.calls = append(m.calls, mockCall{infraName: infraName, name: name})
	return m.err
}

func (m *mockReconciler) NodeClassObject() client.Object {
	return &corev1.ConfigMap{}
}

func (m *mockReconciler) AdditionalSources(_ cache.Cache) []source.Source {
	return nil
}

func TestReconcile(t *testing.T) {
	testCases := []struct {
		name         string
		infraName    string
		request      ctrl.Request
		reconcileErr error
		expectErr    bool
		expectCalls  []mockCall
	}{
		{
			name:      "successful reconcile passes infraName and request name",
			infraName: "my-cluster-abc123",
			request:   ctrl.Request{NamespacedName: client.ObjectKey{Name: "default"}},
			expectErr: false,
			expectCalls: []mockCall{
				{infraName: "my-cluster-abc123", name: "default"},
			},
		},
		{
			name:         "error from reconciler is propagated",
			infraName:    "my-cluster-abc123",
			request:      ctrl.Request{NamespacedName: client.ObjectKey{Name: "custom-nc"}},
			reconcileErr: fmt.Errorf("discovery failed"),
			expectErr:    true,
			expectCalls: []mockCall{
				{infraName: "my-cluster-abc123", name: "custom-nc"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			mock := &mockReconciler{err: tc.reconcileErr}
			c := &Controller{
				client:     fakeclient.NewClientBuilder().Build(),
				infraName:  tc.infraName,
				reconciler: mock,
			}

			_, err := c.Reconcile(t.Context(), tc.request)

			if tc.expectErr {
				g.Expect(err).To(HaveOccurred())
			} else {
				g.Expect(err).NotTo(HaveOccurred())
			}

			g.Expect(mock.calls).To(HaveLen(len(tc.expectCalls)))
			for i, expected := range tc.expectCalls {
				g.Expect(mock.calls[i].infraName).To(Equal(expected.infraName))
				g.Expect(mock.calls[i].name).To(Equal(expected.name))
			}
		})
	}
}
