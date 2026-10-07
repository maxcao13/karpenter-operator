package testutil

import (
	"path/filepath"
	"testing"

	. "github.com/onsi/gomega"
)

func TestMarshalFixture(t *testing.T) {
	tests := []struct {
		name     string
		output   any
		expected string
		wantErr  bool
	}{
		{
			name:     "When output is a string, it should preserve the contents",
			output:   "kind: Deployment\n",
			expected: "kind: Deployment\n",
		},
		{
			name:     "When output is bytes, it should preserve the contents",
			output:   []byte("kind: Deployment\n"),
			expected: "kind: Deployment\n",
		},
		{
			name:     "When output is an object, it should marshal deterministic YAML",
			output:   map[string]string{"kind": "Deployment", "apiVersion": "apps/v1"},
			expected: "apiVersion: apps/v1\nkind: Deployment\n",
		},
		{
			name:    "When output cannot be marshaled, it should return an error",
			output:  make(chan int),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			actual, err := marshalFixture(tc.output)
			if tc.wantErr {
				g.Expect(err).To(HaveOccurred())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(string(actual)).To(Equal(tc.expected))
		})
	}
}

func TestFixturePath(t *testing.T) {
	tests := []struct {
		name     string
		testName string
		filename string
	}{
		{
			name:     "When the test is top level, it should use its name",
			testName: "TestHCPDeploymentFixture",
			filename: "zz_fixture_TestHCPDeploymentFixture.yaml",
		},
		{
			name:     "When the test is nested, it should sanitize separators and punctuation",
			testName: "TestHCPDeploymentFixture/When HCP uses Karpenter, it should render YAML",
			filename: "zz_fixture_TestHCPDeploymentFixture_When_HCP_uses_Karpenter_it_should_render_YAML.yaml",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(fixturePath(tc.testName)).To(Equal(filepath.Join("testdata", tc.filename)))
		})
	}
}
