package crd

import (
	"testing"

	. "github.com/onsi/gomega"

	testfake "github.com/openshift/karpenter-operator/test/pkg/fake"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testNamespace = "openshift-karpenter"

var testCRDs = []*apiextensionsv1.CustomResourceDefinition{
	{
		ObjectMeta: metav1.ObjectMeta{Name: "nodepools.karpenter.sh"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "karpenter.sh",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "nodepools",
				Singular: "nodepool",
				Kind:     "NodePool",
			},
			Scope:    apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Served: true, Storage: true}},
		},
	},
	{
		ObjectMeta: metav1.ObjectMeta{Name: "nodeclaims.karpenter.sh"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "karpenter.sh",
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "nodeclaims",
				Singular: "nodeclaim",
				Kind:     "NodeClaim",
			},
			Scope:    apiextensionsv1.ClusterScoped,
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1", Served: true, Storage: true}},
		},
	},
}

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = apiextensionsv1.AddToScheme(s)
	return s
}

func TestReconcile(t *testing.T) {
	tests := []struct {
		name           string
		existingCRDs   []client.Object
		configuredCRDs []*apiextensionsv1.CustomResourceDefinition
		hostedCluster  *testfake.Cluster
		expectErr      bool
		expectCRDCount int
		expectVersions map[string]string // crd name -> expected first version
	}{
		{
			name:           "When CRDs do not exist it should create them",
			configuredCRDs: testCRDs,
			expectCRDCount: 2,
		},
		{
			name: "When a CRD already exists it should update it",
			existingCRDs: []client.Object{
				&apiextensionsv1.CustomResourceDefinition{
					ObjectMeta: metav1.ObjectMeta{Name: "nodepools.karpenter.sh"},
					Spec: apiextensionsv1.CustomResourceDefinitionSpec{
						Group: "karpenter.sh",
						Names: apiextensionsv1.CustomResourceDefinitionNames{
							Plural: "nodepools",
							Kind:   "NodePool",
						},
						Scope:    apiextensionsv1.ClusterScoped,
						Versions: []apiextensionsv1.CustomResourceDefinitionVersion{{Name: "v1alpha1", Served: true, Storage: true}},
					},
				},
			},
			configuredCRDs: testCRDs,
			expectCRDCount: 2,
			expectVersions: map[string]string{
				"nodepools.karpenter.sh": "v1",
			},
		},
		{
			name:           "When no CRDs are configured it should be a no-op",
			configuredCRDs: nil,
			expectCRDCount: 0,
		},
		{
			name:           "When HostedCluster is set it should write CRDs to the hosted cluster only",
			configuredCRDs: testCRDs,
			hostedCluster:  &testfake.Cluster{Cl: fakeclient.NewClientBuilder().WithScheme(testScheme()).Build(), Ca: &testfake.Cache{}},
			expectCRDCount: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			targetClient := fakeclient.NewClientBuilder().
				WithScheme(testScheme()).
				WithObjects(tc.existingCRDs...).
				Build()

			var c *Controller
			var managementClient client.Client
			if tc.hostedCluster != nil {
				managementClient = fakeclient.NewClientBuilder().WithScheme(testScheme()).Build()
				mgr := &testfake.Manager{Cl: managementClient, Ca: &testfake.Cache{}}
				tc.hostedCluster.Cl = targetClient
				c = NewController(mgr, &ControllerConfig{
					Namespace:     testNamespace,
					CRDs:          tc.configuredCRDs,
					HostedCluster: tc.hostedCluster,
				})
			} else {
				c = &Controller{
					targetClient: targetClient,
					config: &ControllerConfig{
						Namespace: testNamespace,
						CRDs:      tc.configuredCRDs,
					},
				}
			}

			_, err := c.Reconcile(t.Context(), ctrl.Request{})
			if tc.expectErr {
				g.Expect(err).To(HaveOccurred())
				return
			}
			g.Expect(err).NotTo(HaveOccurred())

			crdList := &apiextensionsv1.CustomResourceDefinitionList{}
			g.Expect(targetClient.List(t.Context(), crdList)).To(Succeed())
			g.Expect(crdList.Items).To(HaveLen(tc.expectCRDCount))

			for crdName, expectedVersion := range tc.expectVersions {
				crd := &apiextensionsv1.CustomResourceDefinition{}
				g.Expect(targetClient.Get(t.Context(), client.ObjectKey{Name: crdName}, crd)).To(Succeed())
				g.Expect(crd.Spec.Versions[0].Name).To(Equal(expectedVersion))
			}

			if tc.hostedCluster != nil {
				mgmtCRDs := &apiextensionsv1.CustomResourceDefinitionList{}
				g.Expect(managementClient.List(t.Context(), mgmtCRDs)).To(Succeed())
				g.Expect(mgmtCRDs.Items).To(BeEmpty())
			}
		})
	}
}
