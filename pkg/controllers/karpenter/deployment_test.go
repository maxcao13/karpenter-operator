package karpenter

import (
	"testing"

	. "github.com/onsi/gomega"

	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	corev1 "k8s.io/api/core/v1"
	coreac "k8s.io/client-go/applyconfigurations/core/v1"
)

func TestBuildPodSpecLogLevel(t *testing.T) {
	tests := []struct {
		name     string
		logLevel string
	}{
		{
			name: "When no log level is configured, it should preserve the image defaults",
		},
		{
			name:     "When debug logging is configured, it should set LOG_LEVEL without overriding image arguments",
			logLevel: "debug",
		},
		{
			name:     "When info logging is configured, it should set LOG_LEVEL without overriding image arguments",
			logLevel: "info",
		},
		{
			name:     "When error logging is configured, it should set LOG_LEVEL without overriding image arguments",
			logLevel: "error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			cfg := &operandConfig{
				karpenterImage: "quay.io/openshift/origin-aws-karpenter-provider-aws:latest",
				cloudProvider:  &testfake.CloudProvider{},
			}
			if tc.logLevel != "" {
				cfg.additionalEnv = []corev1.EnvVar{{Name: "LOG_LEVEL", Value: tc.logLevel}}
			}
			podSpec, err := buildPodSpec(cfg)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(podSpec.Containers).To(HaveLen(1))
			container := podSpec.Containers[0]
			g.Expect(container.Command).To(BeEmpty())
			g.Expect(container.Args).To(BeEmpty())
			if tc.logLevel != "" {
				g.Expect(container.Env).To(ContainElement(*coreac.EnvVar().WithName("LOG_LEVEL").WithValue(tc.logLevel)))
			} else {
				g.Expect(container.Env).NotTo(ContainElement(HaveField("Name", HaveValue(Equal("LOG_LEVEL")))))
			}
		})
	}
}
