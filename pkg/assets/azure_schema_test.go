//go:build envtest

package assets_test

import (
	"testing"

	"github.com/openshift/karpenter-operator/pkg/assets"

	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

func TestAzureCRDs(t *testing.T) {
	t.Run("When the Azure provider schemas are installed, it should accept them", func(t *testing.T) {
		testEnv := &envtest.Environment{
			CRDInstallOptions: envtest.CRDInstallOptions{CRDs: assets.AzureCRDs},
		}
		t.Cleanup(func() {
			if err := testEnv.Stop(); err != nil {
				t.Errorf("stopping envtest: %v", err)
			}
		})
		if _, err := testEnv.Start(); err != nil {
			t.Fatalf("installing Azure provider CRDs: %v", err)
		}
	})
}
