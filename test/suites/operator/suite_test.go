package operator

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/openshift/karpenter-operator/test/pkg/environment"
)

var env *environment.Environment

func TestOperator(t *testing.T) {
	RegisterFailHandler(Fail)
	BeforeSuite(func() {
		var err error
		env, err = environment.New()
		Expect(err).NotTo(HaveOccurred())
	})
	RunSpecs(t, "Operator Suite")
}
