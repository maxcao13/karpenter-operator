package aws

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	machineAPINamespace   = "openshift-machine-api"
	workerRoleLabel       = "machine.openshift.io/cluster-api-machine-role"
	workerRoleValue       = "worker"
	defaultUserDataSecret = "worker-user-data"
	nodeClassLabelKey     = "karpenter.k8s.aws/ec2nodeclass"
)

// OCPNodeClassReconciler implements common.NodeClassReconciler for standalone OpenShift.
type OCPNodeClassReconciler struct{}

func NewOCPNodeClassReconciler() *OCPNodeClassReconciler {
	return &OCPNodeClassReconciler{}
}

// --- NodeClassReconciler interface methods ---

func (r *OCPNodeClassReconciler) NodeClassObject() client.Object {
	return &awskarpenterv1.EC2NodeClass{}
}

func (r *OCPNodeClassReconciler) NodeClassLabel() string {
	return nodeClassLabelKey
}

func (r *OCPNodeClassReconciler) AdditionalSources(_ cache.Cache) []source.Source {
	// Returns a one-shot channel source that triggers initial reconciliation
	// of the "default" EC2NodeClass on startup, before any EC2NodeClass objects exist for the
	// Kind watch to fire on.
	initialSync := make(chan event.TypedGenericEvent[client.Object], 1)
	go func() {
		initialSync <- event.TypedGenericEvent[client.Object]{
			Object: &awskarpenterv1.EC2NodeClass{ObjectMeta: metav1.ObjectMeta{Name: defaultEC2NodeClassName}},
		}
	}()
	return []source.Source{
		source.Channel(initialSync, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, _ client.Object) []ctrl.Request {
				return []ctrl.Request{{NamespacedName: types.NamespacedName{Name: defaultEC2NodeClassName}}}
			},
		)),
	}
}

func (r *OCPNodeClassReconciler) ReconcileNodeClass(ctx context.Context, c client.Client, infraName string, name string) error {
	defaults, err := r.discoverDefaults(ctx, c)
	if err != nil {
		return fmt.Errorf("failed to discover defaults: %w", err)
	}

	// TODO(maxcao13): The default NodeClass discovery path below will potentially be deprecated in the future.
	// In Tech Preview+, a better idea might be for the openshift installer to construct a default NodeClass manifest.
	//
	// The "default" NodeClass is a fully operator-managed resource that gives
	// users an out-of-the-box NodeClass so they only need to create a NodePool
	// to start provisioning nodes and not need to worry about their infrastructure settings.
	// If deleted, the operator recreates it with full infrastructure defaults via ensureDefaultNodeClass.
	// If it already exists, only protected fields (AMIFamily, AMI, UserData) are enforced;
	// non-protected fields are preserved to allow user customization after initial creation.
	//
	// Non-default (user-created) NodeClasses only get protected fields enforced
	// (amiFamily, amiSelectorTerms, userData) via applyProtectedFields.
	ec2nc := &awskarpenterv1.EC2NodeClass{}
	err = c.Get(ctx, types.NamespacedName{Name: name}, ec2nc)
	if errors.IsNotFound(err) {
		// If the default NodeClass does not exist, ensure it.
		if name != defaultEC2NodeClassName {
			return nil
		}
		return r.ensureDefaultNodeClass(ctx, c, defaults, infraName)
	} else if err != nil {
		return fmt.Errorf("failed to get EC2NodeClass %q: %w", name, err)
	}

	original := ec2nc.DeepCopy()
	applyProtectedFields(ec2nc, defaults)
	applyDefaults(ec2nc)
	return c.Patch(ctx, ec2nc, client.MergeFromWithOptions(original, client.MergeFromWithOptimisticLock{}))
}

// --- Reconciliation helpers ---

func (r *OCPNodeClassReconciler) ensureDefaultNodeClass(ctx context.Context, c client.Client, defaults *nodeClassDefaults, infraName string) error {
	log := ctrl.LoggerFrom(ctx)
	ec2nc := &awskarpenterv1.EC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: defaultEC2NodeClassName},
	}
	op, err := controllerutil.CreateOrUpdate(ctx, c, ec2nc, func() error {
		applyToDefaultNodeClass(ec2nc, defaults, infraName)
		return nil
	})
	if err != nil {
		return err
	}
	log.Info("reconciled default EC2NodeClass", "operation", op)
	return nil
}

// --- Spec mutation functions ---

// applyToDefaultNodeClass force-reconciles all operator-managed fields on the "default" EC2NodeClass.
func applyToDefaultNodeClass(ec2nc *awskarpenterv1.EC2NodeClass, d *nodeClassDefaults, infraName string) {
	ec2nc.Spec.AMIFamily = ptr.To(awskarpenterv1.AMIFamilyCustom)

	if d.instanceProfile != "" {
		ec2nc.Spec.InstanceProfile = ptr.To(d.instanceProfile)
	}

	if d.amiID != "" {
		ec2nc.Spec.AMISelectorTerms = []awskarpenterv1.AMISelectorTerm{
			{ID: d.amiID},
		}
	}

	ec2nc.Spec.SubnetSelectorTerms = []awskarpenterv1.SubnetSelectorTerm{
		{
			Tags: map[string]string{
				fmt.Sprintf("kubernetes.io/cluster/%s", infraName): "*",
				"kubernetes.io/role/internal-elb":                  "1",
			},
		},
	}

	if len(d.securityGroupNames) > 0 {
		var sgTerms []awskarpenterv1.SecurityGroupSelectorTerm
		for _, name := range d.securityGroupNames {
			sgTerms = append(sgTerms, awskarpenterv1.SecurityGroupSelectorTerm{
				Tags: map[string]string{"Name": name},
			})
		}
		ec2nc.Spec.SecurityGroupSelectorTerms = sgTerms
	}

	ec2nc.Spec.BlockDeviceMappings = d.blockDeviceMappings
	if d.userData != "" {
		ec2nc.Spec.UserData = ptr.To(d.userData)
	}
}

// applyProtectedFields force-reconciles fields that must only be set by karpenter-operator and the OpenShift platform.
// For OpenShift/RHCOS nodes, the AMI and userData are tightly coupled to the OpenShift release image and should not be overridden by users.
// TODO(maxcao13): After DevPreview, we shouldn't be discovering these values from a MachineSet.
// Instead, userData should be created programmatically by reconciling a per-NodeClass MachineConfig, which the MCO will generate a userData secret from.
// The AMI should come from the coreos-bootimages ConfigMap in openshift-machine-config-operator, keyed by region and architecture.
func applyProtectedFields(ec2nc *awskarpenterv1.EC2NodeClass, d *nodeClassDefaults) {
	ec2nc.Spec.AMIFamily = ptr.To(awskarpenterv1.AMIFamilyCustom)
	ec2nc.Spec.AMISelectorTerms = []awskarpenterv1.AMISelectorTerm{
		{ID: d.amiID},
	}
	ec2nc.Spec.UserData = ptr.To(d.userData)
}

// applyDefaults fills in default values for optional fields that the user has not set.
// TODO(maxcao13): Ideally we would enforce this at the CRD level, but that would mean
// we have to manually mangle our version of the EC2NodeClass CRD. For now we enforce this at the operator level.
func applyDefaults(ec2nc *awskarpenterv1.EC2NodeClass) {
	if ec2nc.Spec.BlockDeviceMappings == nil {
		ec2nc.Spec.BlockDeviceMappings = defaultBlockDeviceMappings()
	}
}

// --- Infrastructure discovery ---

type nodeClassDefaults struct {
	instanceProfile     string
	amiID               string
	securityGroupNames  []string
	blockDeviceMappings []*awskarpenterv1.BlockDeviceMapping
	userData            string
}

type discoveredValues struct {
	instanceProfile     string
	amiID               string
	securityGroupNames  []string
	blockDeviceMappings []*awskarpenterv1.BlockDeviceMapping
	userDataSecretName  string
}

func (r *OCPNodeClassReconciler) discoverDefaults(ctx context.Context, c client.Client) (*nodeClassDefaults, error) {
	dv, err := r.discoverFromMachineSet(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("failed to discover infrastructure from MachineSet: %w", err)
	}

	userData, err := readUserDataSecret(ctx, c, dv.userDataSecretName)
	if err != nil {
		return nil, fmt.Errorf("failed to read userData secret %q: %w", dv.userDataSecretName, err)
	}

	return &nodeClassDefaults{
		instanceProfile:     dv.instanceProfile,
		amiID:               dv.amiID,
		securityGroupNames:  dv.securityGroupNames,
		blockDeviceMappings: dv.blockDeviceMappings,
		userData:            userData,
	}, nil
}

// awsMachineProviderConfig is a lightweight projection of the AWS-specific fields
// inside a MachineSet's providerSpec.value.
type awsMachineProviderConfig struct {
	AMI struct {
		ID string `json:"id"`
	} `json:"ami"`
	IAMInstanceProfile struct {
		ID string `json:"id"`
	} `json:"iamInstanceProfile"`
	SecurityGroups []struct {
		Filters []struct {
			Name   string   `json:"name"`
			Values []string `json:"values"`
		} `json:"filters"`
	} `json:"securityGroups"`
	BlockDevices []struct {
		EBS struct {
			Encrypted  bool   `json:"encrypted"`
			VolumeSize int64  `json:"volumeSize"`
			VolumeType string `json:"volumeType"`
		} `json:"ebs"`
	} `json:"blockDevices"`
	UserDataSecret struct {
		Name string `json:"name"`
	} `json:"userDataSecret"`
}

func (r *OCPNodeClassReconciler) discoverFromMachineSet(ctx context.Context, c client.Client) (*discoveredValues, error) {
	list := &machinev1beta1.MachineSetList{}
	if err := c.List(ctx, list, client.InNamespace(machineAPINamespace)); err != nil {
		return nil, fmt.Errorf("failed to list MachineSets: %w", err)
	}

	slices.SortFunc(list.Items, func(a, b machinev1beta1.MachineSet) int {
		return cmp.Compare(a.Name, b.Name)
	})

	var ms *machinev1beta1.MachineSet
	for i := range list.Items {
		labels := list.Items[i].Spec.Template.Labels
		if labels[workerRoleLabel] == workerRoleValue {
			ms = &list.Items[i]
			break
		}
	}
	if ms == nil {
		return nil, fmt.Errorf("no worker MachineSet found in %s", machineAPINamespace)
	}

	providerSpecValue := ms.Spec.Template.Spec.ProviderSpec.Value
	if providerSpecValue == nil {
		return nil, fmt.Errorf("MachineSet %s has nil providerSpec.value", ms.Name)
	}

	var provider awsMachineProviderConfig
	if err := json.Unmarshal(providerSpecValue.Raw, &provider); err != nil {
		return nil, fmt.Errorf("failed to unmarshal providerSpec from MachineSet %s: %w", ms.Name, err)
	}

	return valuesFromProviderConfig(&provider), nil
}

func valuesFromProviderConfig(provider *awsMachineProviderConfig) *discoveredValues {
	var sgNames []string
	for _, sg := range provider.SecurityGroups {
		for _, f := range sg.Filters {
			if f.Name == "tag:Name" {
				sgNames = append(sgNames, f.Values...)
			}
		}
	}

	// RHCOS MachineSets always define a single root volume block device.
	var bdm []*awskarpenterv1.BlockDeviceMapping
	if len(provider.BlockDevices) > 0 && validBlockDevice(provider.BlockDevices[0]) {
		bd := provider.BlockDevices[0]
		bdm = []*awskarpenterv1.BlockDeviceMapping{
			{
				DeviceName: ptr.To(rhcosDeviceName),
				EBS: &awskarpenterv1.BlockDevice{
					VolumeSize: ptr.To(resource.MustParse(fmt.Sprintf("%dGi", bd.EBS.VolumeSize))),
					VolumeType: ptr.To(bd.EBS.VolumeType),
					Encrypted:  ptr.To(bd.EBS.Encrypted),
				},
			},
		}
	} else {
		bdm = defaultBlockDeviceMappings()
	}

	userDataSecretName := provider.UserDataSecret.Name
	if userDataSecretName == "" {
		userDataSecretName = defaultUserDataSecret
	}

	return &discoveredValues{
		instanceProfile:     provider.IAMInstanceProfile.ID,
		amiID:               provider.AMI.ID,
		securityGroupNames:  sgNames,
		blockDeviceMappings: bdm,
		userDataSecretName:  userDataSecretName,
	}
}

func readUserDataSecret(ctx context.Context, c client.Client, secretName string) (string, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: machineAPINamespace,
		Name:      secretName,
	}, secret); err != nil {
		return "", fmt.Errorf("failed to get secret %s/%s: %w", machineAPINamespace, secretName, err)
	}
	userData, ok := secret.Data["userData"]
	if !ok {
		return "", fmt.Errorf("secret %s/%s does not contain 'userData' key", machineAPINamespace, secretName)
	}
	return string(userData), nil
}

func validBlockDevice(bd struct {
	EBS struct {
		Encrypted  bool   `json:"encrypted"`
		VolumeSize int64  `json:"volumeSize"`
		VolumeType string `json:"volumeType"`
	} `json:"ebs"`
}) bool {
	return bd.EBS.VolumeSize > 0 && bd.EBS.VolumeType != ""
}
