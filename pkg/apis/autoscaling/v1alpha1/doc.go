// +k8s:deepcopy-gen=package,register
// +groupName=autoscaling.openshift.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
)

var SchemeGroupVersion = schema.GroupVersion{Group: "autoscaling.openshift.io", Version: "v1alpha1"}

func init() {
	metav1.AddToGroupVersion(scheme.Scheme, SchemeGroupVersion)
	scheme.Scheme.AddKnownTypes(SchemeGroupVersion,
		&Karpenter{},
		&KarpenterList{})
}

// AddToScheme registers the types in this package with the given scheme.
func AddToScheme(s *runtime.Scheme) error {
	metav1.AddToGroupVersion(s, SchemeGroupVersion)
	s.AddKnownTypes(SchemeGroupVersion,
		&Karpenter{},
		&KarpenterList{})
	return nil
}
