package aws

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/gomega"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"

	awskarpenterapis "github.com/aws/karpenter-provider-aws/pkg/apis"
	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = machinev1beta1.Install(s)

	awsGV := schema.GroupVersion{Group: awskarpenterapis.Group, Version: "v1"}
	metav1.AddToGroupVersion(s, awsGV)
	s.AddKnownTypes(awsGV, &awskarpenterv1.EC2NodeClass{}, &awskarpenterv1.EC2NodeClassList{})
	return s
}

func newFakeClient(objs ...client.Object) client.Client {
	return fakeclient.NewClientBuilder().
		WithScheme(testScheme()).
		WithObjects(objs...).
		Build()
}

func testMachineSet(providerConfig awsMachineProviderConfig) *machinev1beta1.MachineSet {
	raw, _ := json.Marshal(providerConfig)
	return &machinev1beta1.MachineSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster-worker-us-east-1a",
			Namespace: machineAPINamespace,
		},
		Spec: machinev1beta1.MachineSetSpec{
			Template: machinev1beta1.MachineTemplateSpec{
				ObjectMeta: machinev1beta1.ObjectMeta{
					Labels: map[string]string{
						workerRoleLabel: workerRoleValue,
					},
				},
				Spec: machinev1beta1.MachineSpec{
					ProviderSpec: machinev1beta1.ProviderSpec{
						Value: &runtime.RawExtension{Raw: raw},
					},
				},
			},
		},
	}
}

func testUserDataSecret(name, data string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: machineAPINamespace,
		},
		Data: map[string][]byte{
			"userData": []byte(data),
		},
	}
}

var testProviderConfig = awsMachineProviderConfig{
	AMI: struct {
		ID string `json:"id"`
	}{ID: "ami-0123456789abcdef0"},
	IAMInstanceProfile: struct {
		ID string `json:"id"`
	}{ID: "test-cluster-worker-profile"},
	SecurityGroups: []struct {
		Filters []struct {
			Name   string   `json:"name"`
			Values []string `json:"values"`
		} `json:"filters"`
	}{
		{Filters: []struct {
			Name   string   `json:"name"`
			Values []string `json:"values"`
		}{{Name: "tag:Name", Values: []string{"test-cluster-node"}}}},
		{Filters: []struct {
			Name   string   `json:"name"`
			Values []string `json:"values"`
		}{{Name: "tag:Name", Values: []string{"test-cluster-lb"}}}},
	},
	BlockDevices: []struct {
		EBS struct {
			Encrypted  bool   `json:"encrypted"`
			VolumeSize int64  `json:"volumeSize"`
			VolumeType string `json:"volumeType"`
		} `json:"ebs"`
	}{
		{EBS: struct {
			Encrypted  bool   `json:"encrypted"`
			VolumeSize int64  `json:"volumeSize"`
			VolumeType string `json:"volumeType"`
		}{Encrypted: true, VolumeSize: 120, VolumeType: "gp3"}},
	},
	UserDataSecret: struct {
		Name string `json:"name"`
	}{Name: "worker-user-data"},
}

func TestReconcileNodeClass_DefaultCreation(t *testing.T) {
	g := NewWithT(t)
	c := newFakeClient(
		testMachineSet(testProviderConfig),
		testUserDataSecret("worker-user-data", "ignition-payload"),
	)
	r := NewOCPNodeClassReconciler()

	ctx := t.Context()
	err := r.ReconcileNodeClass(ctx, c, "test-cluster", defaultEC2NodeClassName)
	g.Expect(err).NotTo(HaveOccurred())

	ec2nc := &awskarpenterv1.EC2NodeClass{}
	g.Expect(c.Get(ctx, types.NamespacedName{Name: defaultEC2NodeClassName}, ec2nc)).To(Succeed())

	g.Expect(ptr.Deref(ec2nc.Spec.AMIFamily, "")).To(Equal(awskarpenterv1.AMIFamilyCustom))
	g.Expect(ptr.Deref(ec2nc.Spec.InstanceProfile, "")).To(Equal("test-cluster-worker-profile"))
	g.Expect(ec2nc.Spec.AMISelectorTerms).To(HaveLen(1))
	g.Expect(ec2nc.Spec.AMISelectorTerms[0].ID).To(Equal("ami-0123456789abcdef0"))
	g.Expect(ptr.Deref(ec2nc.Spec.UserData, "")).To(Equal("ignition-payload"))

	g.Expect(ec2nc.Spec.SubnetSelectorTerms).To(HaveLen(1))
	g.Expect(ec2nc.Spec.SubnetSelectorTerms[0].Tags).To(HaveKeyWithValue("kubernetes.io/cluster/test-cluster", "*"))
	g.Expect(ec2nc.Spec.SubnetSelectorTerms[0].Tags).To(HaveKeyWithValue("kubernetes.io/role/internal-elb", "1"))

	g.Expect(ec2nc.Spec.SecurityGroupSelectorTerms).To(HaveLen(2))
	g.Expect(ec2nc.Spec.SecurityGroupSelectorTerms[0].Tags).To(HaveKeyWithValue("Name", "test-cluster-node"))
	g.Expect(ec2nc.Spec.SecurityGroupSelectorTerms[1].Tags).To(HaveKeyWithValue("Name", "test-cluster-lb"))

	g.Expect(ec2nc.Spec.BlockDeviceMappings).To(HaveLen(1))
	g.Expect(ptr.Deref(ec2nc.Spec.BlockDeviceMappings[0].DeviceName, "")).To(Equal(rhcosDeviceName))
}

func TestReconcileNodeClass_DefaultExistingAndRecreation(t *testing.T) {
	g := NewWithT(t)
	existing := &awskarpenterv1.EC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: defaultEC2NodeClassName},
		Spec: awskarpenterv1.EC2NodeClassSpec{
			AMIFamily:       ptr.To(awskarpenterv1.AMIFamilyCustom),
			InstanceProfile: ptr.To("user-modified-profile"),
			UserData:        ptr.To("old-userdata"),
			AMISelectorTerms: []awskarpenterv1.AMISelectorTerm{
				{ID: "ami-old"},
			},
		},
	}

	c := newFakeClient(
		testMachineSet(testProviderConfig),
		testUserDataSecret("worker-user-data", "new-ignition-payload"),
		existing,
	)
	r := NewOCPNodeClassReconciler()

	ctx := t.Context()
	err := r.ReconcileNodeClass(ctx, c, "test-cluster", defaultEC2NodeClassName)
	g.Expect(err).NotTo(HaveOccurred())

	ec2nc := &awskarpenterv1.EC2NodeClass{}
	g.Expect(c.Get(ctx, types.NamespacedName{Name: defaultEC2NodeClassName}, ec2nc)).To(Succeed())

	// Protected fields overwritten
	g.Expect(ptr.Deref(ec2nc.Spec.AMIFamily, "")).To(Equal(awskarpenterv1.AMIFamilyCustom))
	g.Expect(ptr.Deref(ec2nc.Spec.UserData, "")).To(Equal("new-ignition-payload"))
	g.Expect(ec2nc.Spec.AMISelectorTerms[0].ID).To(Equal("ami-0123456789abcdef0"))

	// Non-protected fields preserved (user can customize the default NodeClass after creation)
	g.Expect(ptr.Deref(ec2nc.Spec.InstanceProfile, "")).To(Equal("user-modified-profile"))

	// Deleting the default NodeClass and reconciling should recreate it with full defaults
	g.Expect(c.Delete(ctx, ec2nc)).To(Succeed())

	err = r.ReconcileNodeClass(ctx, c, "test-cluster", defaultEC2NodeClassName)
	g.Expect(err).NotTo(HaveOccurred())

	recreated := &awskarpenterv1.EC2NodeClass{}
	g.Expect(c.Get(ctx, types.NamespacedName{Name: defaultEC2NodeClassName}, recreated)).To(Succeed())

	g.Expect(ptr.Deref(recreated.Spec.AMIFamily, "")).To(Equal(awskarpenterv1.AMIFamilyCustom))
	g.Expect(ptr.Deref(recreated.Spec.InstanceProfile, "")).To(Equal("test-cluster-worker-profile"))
	g.Expect(recreated.Spec.AMISelectorTerms[0].ID).To(Equal("ami-0123456789abcdef0"))
	g.Expect(ptr.Deref(recreated.Spec.UserData, "")).To(Equal("new-ignition-payload"))
	g.Expect(recreated.Spec.SubnetSelectorTerms).To(HaveLen(1))
	g.Expect(recreated.Spec.SecurityGroupSelectorTerms).To(HaveLen(2))
	g.Expect(recreated.Spec.BlockDeviceMappings).To(HaveLen(1))
}

func TestReconcileNodeClass_NonDefaultProtectedFieldsOnly(t *testing.T) {
	g := NewWithT(t)
	existing := &awskarpenterv1.EC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-nodeclass"},
		Spec: awskarpenterv1.EC2NodeClassSpec{
			AMIFamily: ptr.To(awskarpenterv1.AMIFamilyCustom),
			AMISelectorTerms: []awskarpenterv1.AMISelectorTerm{
				{ID: "ami-old"},
			},
			UserData:        ptr.To("user-tampered-userdata"),
			InstanceProfile: ptr.To("user-custom-profile"),
			SubnetSelectorTerms: []awskarpenterv1.SubnetSelectorTerm{
				{Tags: map[string]string{"custom-tag": "custom-value"}},
			},
			SecurityGroupSelectorTerms: []awskarpenterv1.SecurityGroupSelectorTerm{
				{Tags: map[string]string{"Name": "user-chosen-sg"}},
			},
			BlockDeviceMappings: []*awskarpenterv1.BlockDeviceMapping{
				{DeviceName: ptr.To("/dev/sda1")},
			},
		},
	}

	c := newFakeClient(
		testMachineSet(testProviderConfig),
		testUserDataSecret("worker-user-data", "correct-ignition"),
		existing,
	)
	r := NewOCPNodeClassReconciler()

	ctx := t.Context()
	err := r.ReconcileNodeClass(ctx, c, "test-cluster", "custom-nodeclass")
	g.Expect(err).NotTo(HaveOccurred())

	ec2nc := &awskarpenterv1.EC2NodeClass{}
	g.Expect(c.Get(ctx, types.NamespacedName{Name: "custom-nodeclass"}, ec2nc)).To(Succeed())

	// Protected fields overwritten
	g.Expect(ptr.Deref(ec2nc.Spec.AMIFamily, "")).To(Equal(awskarpenterv1.AMIFamilyCustom))
	g.Expect(ec2nc.Spec.AMISelectorTerms[0].ID).To(Equal("ami-0123456789abcdef0"))
	g.Expect(ptr.Deref(ec2nc.Spec.UserData, "")).To(Equal("correct-ignition"))

	// User-customized fields preserved
	g.Expect(ptr.Deref(ec2nc.Spec.InstanceProfile, "")).To(Equal("user-custom-profile"))
	g.Expect(ec2nc.Spec.SubnetSelectorTerms[0].Tags).To(HaveKeyWithValue("custom-tag", "custom-value"))
	g.Expect(ec2nc.Spec.SecurityGroupSelectorTerms[0].Tags).To(HaveKeyWithValue("Name", "user-chosen-sg"))
	g.Expect(ptr.Deref(ec2nc.Spec.BlockDeviceMappings[0].DeviceName, "")).To(Equal("/dev/sda1"))
}

func TestReconcileNodeClass_NonDefaultBlockDeviceDefault(t *testing.T) {
	g := NewWithT(t)
	existing := &awskarpenterv1.EC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "custom-no-bdm"},
		Spec: awskarpenterv1.EC2NodeClassSpec{
			AMIFamily: ptr.To(awskarpenterv1.AMIFamilyCustom),
			AMISelectorTerms: []awskarpenterv1.AMISelectorTerm{
				{ID: "ami-0123456789abcdef0"},
			},
			UserData: ptr.To("ignition-payload"),
		},
	}

	c := newFakeClient(
		testMachineSet(testProviderConfig),
		testUserDataSecret("worker-user-data", "ignition-payload"),
		existing,
	)
	r := NewOCPNodeClassReconciler()

	ctx := t.Context()
	err := r.ReconcileNodeClass(ctx, c, "test-cluster", "custom-no-bdm")
	g.Expect(err).NotTo(HaveOccurred())

	ec2nc := &awskarpenterv1.EC2NodeClass{}
	g.Expect(c.Get(ctx, types.NamespacedName{Name: "custom-no-bdm"}, ec2nc)).To(Succeed())

	g.Expect(ec2nc.Spec.BlockDeviceMappings).NotTo(BeNil())
	g.Expect(ec2nc.Spec.BlockDeviceMappings).To(HaveLen(1))
	g.Expect(ptr.Deref(ec2nc.Spec.BlockDeviceMappings[0].DeviceName, "")).To(Equal(rhcosDeviceName))
}

func TestReconcileNodeClass_NonDefaultIsNotEnsured(t *testing.T) {
	g := NewWithT(t)
	c := newFakeClient(
		testMachineSet(testProviderConfig),
		testUserDataSecret("worker-user-data", "ignition-payload"),
	)
	r := NewOCPNodeClassReconciler()

	ctx := t.Context()
	err := r.ReconcileNodeClass(ctx, c, "test-cluster", "does-not-exist")
	g.Expect(err).NotTo(HaveOccurred())

	ec2nc := &awskarpenterv1.EC2NodeClass{}
	err = c.Get(ctx, types.NamespacedName{Name: "does-not-exist"}, ec2nc)
	g.Expect(err).To(HaveOccurred())
}

func TestReconcileNodeClass_ErrorNoWorkerMachineSet(t *testing.T) {
	g := NewWithT(t)
	nonWorkerMS := testMachineSet(testProviderConfig)
	nonWorkerMS.Spec.Template.Labels = map[string]string{
		workerRoleLabel: "infra",
	}

	c := newFakeClient(
		nonWorkerMS,
		testUserDataSecret("worker-user-data", "ignition-payload"),
	)
	r := NewOCPNodeClassReconciler()

	err := r.ReconcileNodeClass(t.Context(), c, "test-cluster", defaultEC2NodeClassName)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("no worker MachineSet"))
}

func TestReconcileNodeClass_ErrorMissingUserDataSecret(t *testing.T) {
	g := NewWithT(t)
	c := newFakeClient(
		testMachineSet(testProviderConfig),
	)
	r := NewOCPNodeClassReconciler()

	err := r.ReconcileNodeClass(t.Context(), c, "test-cluster", defaultEC2NodeClassName)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("userData secret"))
}

// --- discoverFromMachineSet unit tests ---

func TestDiscoverFromMachineSet_MultipleSecurityGroups(t *testing.T) {
	g := NewWithT(t)
	cfg := awsMachineProviderConfig{
		AMI: struct {
			ID string `json:"id"`
		}{ID: "ami-abc"},
		IAMInstanceProfile: struct {
			ID string `json:"id"`
		}{ID: "profile"},
		SecurityGroups: []struct {
			Filters []struct {
				Name   string   `json:"name"`
				Values []string `json:"values"`
			} `json:"filters"`
		}{
			{Filters: []struct {
				Name   string   `json:"name"`
				Values []string `json:"values"`
			}{{Name: "tag:Name", Values: []string{"sg-node", "sg-extra"}}}},
			{Filters: []struct {
				Name   string   `json:"name"`
				Values []string `json:"values"`
			}{{Name: "tag:Name", Values: []string{"sg-lb"}}}},
			{Filters: []struct {
				Name   string   `json:"name"`
				Values []string `json:"values"`
			}{{Name: "group-id", Values: []string{"sg-12345"}}}},
		},
		UserDataSecret: struct {
			Name string `json:"name"`
		}{Name: "worker-user-data"},
	}
	c := newFakeClient(testMachineSet(cfg))
	r := NewOCPNodeClassReconciler()

	dv, err := r.discoverFromMachineSet(t.Context(), c)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(dv.securityGroupNames).To(Equal([]string{"sg-node", "sg-extra", "sg-lb"}))
}

func TestDiscoverFromMachineSet_MultipleBlockDevices(t *testing.T) {
	g := NewWithT(t)
	cfg := awsMachineProviderConfig{
		AMI: struct {
			ID string `json:"id"`
		}{ID: "ami-abc"},
		IAMInstanceProfile: struct {
			ID string `json:"id"`
		}{ID: "profile"},
		BlockDevices: []struct {
			EBS struct {
				Encrypted  bool   `json:"encrypted"`
				VolumeSize int64  `json:"volumeSize"`
				VolumeType string `json:"volumeType"`
			} `json:"ebs"`
		}{
			{EBS: struct {
				Encrypted  bool   `json:"encrypted"`
				VolumeSize int64  `json:"volumeSize"`
				VolumeType string `json:"volumeType"`
			}{Encrypted: true, VolumeSize: 120, VolumeType: "gp3"}},
			{EBS: struct {
				Encrypted  bool   `json:"encrypted"`
				VolumeSize int64  `json:"volumeSize"`
				VolumeType string `json:"volumeType"`
			}{Encrypted: false, VolumeSize: 500, VolumeType: "io1"}},
		},
		UserDataSecret: struct {
			Name string `json:"name"`
		}{Name: "worker-user-data"},
	}
	c := newFakeClient(testMachineSet(cfg))
	r := NewOCPNodeClassReconciler()

	dv, err := r.discoverFromMachineSet(t.Context(), c)
	g.Expect(err).NotTo(HaveOccurred())
	// Only the first (root) block device is used
	g.Expect(dv.blockDeviceMappings).To(HaveLen(1))
	g.Expect(ptr.Deref(dv.blockDeviceMappings[0].EBS.VolumeType, "")).To(Equal("gp3"))
	g.Expect(ptr.Deref(dv.blockDeviceMappings[0].EBS.Encrypted, false)).To(BeTrue())
}

func TestDiscoverFromMachineSet_FallbackBlockDevices(t *testing.T) {
	g := NewWithT(t)
	cfg := awsMachineProviderConfig{
		AMI: struct {
			ID string `json:"id"`
		}{ID: "ami-abc"},
		IAMInstanceProfile: struct {
			ID string `json:"id"`
		}{ID: "profile"},
		UserDataSecret: struct {
			Name string `json:"name"`
		}{Name: "worker-user-data"},
	}
	c := newFakeClient(testMachineSet(cfg))
	r := NewOCPNodeClassReconciler()

	dv, err := r.discoverFromMachineSet(t.Context(), c)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(dv.blockDeviceMappings).To(HaveLen(1))
	g.Expect(ptr.Deref(dv.blockDeviceMappings[0].DeviceName, "")).To(Equal(rhcosDeviceName))
	g.Expect(ptr.Deref(dv.blockDeviceMappings[0].EBS.VolumeType, "")).To(Equal(defaultRootVolumeType))
}

func TestDiscoverFromMachineSet_FallbackUserDataSecretName(t *testing.T) {
	g := NewWithT(t)
	cfg := awsMachineProviderConfig{
		AMI: struct {
			ID string `json:"id"`
		}{ID: "ami-abc"},
		IAMInstanceProfile: struct {
			ID string `json:"id"`
		}{ID: "profile"},
	}
	c := newFakeClient(testMachineSet(cfg))
	r := NewOCPNodeClassReconciler()

	dv, err := r.discoverFromMachineSet(t.Context(), c)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(dv.userDataSecretName).To(Equal(defaultUserDataSecret))
}

func TestDiscoverFromMachineSet_NilProviderSpec(t *testing.T) {
	g := NewWithT(t)
	ms := testMachineSet(testProviderConfig)
	ms.Spec.Template.Spec.ProviderSpec.Value = nil
	c := newFakeClient(ms)
	r := NewOCPNodeClassReconciler()

	_, err := r.discoverFromMachineSet(t.Context(), c)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("nil providerSpec.value"))
}

func TestDiscoverFromMachineSet_NoMachineSets(t *testing.T) {
	g := NewWithT(t)
	c := newFakeClient()
	r := NewOCPNodeClassReconciler()

	_, err := r.discoverFromMachineSet(t.Context(), c)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("no worker MachineSet"))
}

func TestDiscoverFromMachineSet_NoWorkerMachineSet(t *testing.T) {
	g := NewWithT(t)
	ms := testMachineSet(testProviderConfig)
	ms.Spec.Template.Labels = map[string]string{
		workerRoleLabel: "infra",
	}
	c := newFakeClient(ms)
	r := NewOCPNodeClassReconciler()

	_, err := r.discoverFromMachineSet(t.Context(), c)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("no worker MachineSet"))
}

func TestDiscoverFromMachineSet_MalformedProviderSpec(t *testing.T) {
	g := NewWithT(t)
	ms := testMachineSet(testProviderConfig)
	ms.Spec.Template.Spec.ProviderSpec.Value = &runtime.RawExtension{Raw: []byte("{}")}
	c := newFakeClient(ms)
	r := NewOCPNodeClassReconciler()

	dv, err := r.discoverFromMachineSet(t.Context(), c)
	g.Expect(err).NotTo(HaveOccurred())
	// All fields should be zero-valued or fallback
	g.Expect(dv.amiID).To(BeEmpty())
	g.Expect(dv.instanceProfile).To(BeEmpty())
	g.Expect(dv.securityGroupNames).To(BeEmpty())
	g.Expect(dv.blockDeviceMappings).To(HaveLen(1)) // fallback defaults
	g.Expect(dv.userDataSecretName).To(Equal(defaultUserDataSecret))
}

func TestValidBlockDevice(t *testing.T) {
	type bd struct {
		EBS struct {
			Encrypted  bool   `json:"encrypted"`
			VolumeSize int64  `json:"volumeSize"`
			VolumeType string `json:"volumeType"`
		} `json:"ebs"`
	}

	tests := []struct {
		name       string
		volumeSize int64
		volumeType string
		expect     bool
	}{
		{name: "valid", volumeSize: 120, volumeType: "gp3", expect: true},
		{name: "zero volume size", volumeSize: 0, volumeType: "gp3", expect: false},
		{name: "empty volume type", volumeSize: 120, volumeType: "", expect: false},
		{name: "both zero/empty", volumeSize: 0, volumeType: "", expect: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			var b bd
			b.EBS.VolumeSize = tt.volumeSize
			b.EBS.VolumeType = tt.volumeType
			g.Expect(validBlockDevice(b)).To(Equal(tt.expect))
		})
	}
}
