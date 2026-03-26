package aws

import (
	"context"
	"encoding/json"
	"fmt"

	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcppool "github.com/openshift/karpenter-operator/pkg/controllers/machineconfigpool"
)

const (
	defaultEC2NodeClassName = "default"
	machineAPINamespace     = "openshift-machine-api"
	workerRoleLabel         = "machine.openshift.io/cluster-api-machine-role"
	workerRoleValue         = "worker"
	defaultUserDataSecret   = "worker-user-data"
	defaultRootVolumeSize   = "120Gi"
)

var machineSetGVR = schema.GroupVersionResource{
	Group:    "machine.openshift.io",
	Version:  "v1beta1",
	Resource: "machinesets",
}

func (p *Provider) NodeClassObject() client.Object {
	return &awskarpenterv1.EC2NodeClass{}
}

func (p *Provider) ReconcileDefaultNodeClass(ctx context.Context, c client.Client, infraName string) error {
	log := ctrl.LoggerFrom(ctx)

	defaults, err := p.discoverDefaults(ctx, c, infraName)
	if err != nil {
		return fmt.Errorf("failed to discover defaults: %w", err)
	}

	log.Info("discovered defaults",
		"instanceProfile", defaults.instanceProfile,
		"ami", defaults.amiID,
		"securityGroups", len(defaults.securityGroupNames),
		"hasUserData", defaults.userData != "",
	)

	ec2nc := &awskarpenterv1.EC2NodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: defaultEC2NodeClassName,
		},
	}

	_, err = controllerutil.CreateOrUpdate(ctx, c, ec2nc, func() error {
		applyDefaults(ec2nc, defaults, infraName)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to reconcile default EC2NodeClass: %w", err)
	}

	return nil
}

type nodeClassDefaults struct {
	instanceProfile     string
	amiID               string
	securityGroupNames  []string
	blockDeviceMappings []*awskarpenterv1.BlockDeviceMapping
	userData            string
}

func applyDefaults(ec2nc *awskarpenterv1.EC2NodeClass, d *nodeClassDefaults, infraName string) {
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

	if len(d.blockDeviceMappings) > 0 {
		ec2nc.Spec.BlockDeviceMappings = d.blockDeviceMappings
	}

	if d.userData != "" {
		ec2nc.Spec.UserData = ptr.To(d.userData)
	}
}

type discoveredValues struct {
	instanceProfile     string
	amiID               string
	securityGroupNames  []string
	blockDeviceMappings []*awskarpenterv1.BlockDeviceMapping
	userData            string
	userDataSecretName  string
}

// awsMachineProviderConfig is a lightweight projection of the MachineSet's
// providerSpec used to avoid importing machine-api types.
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

func (p *Provider) discoverDefaults(ctx context.Context, c client.Client, infraName string) (*nodeClassDefaults, error) {
	log := ctrl.LoggerFrom(ctx)

	dv, err := discoverFromMachineSet(ctx, c)
	if err != nil {
		log.Info("could not discover from MachineSet, using naming convention fallback", "error", err)
		dv = fallbackDefaults(infraName)
	}

	userData, err := readUserDataSecret(ctx, c, dv.userDataSecretName)
	if err != nil {
		log.Info("could not read userData secret", "error", err, "secretName", dv.userDataSecretName)
	} else {
		mcpName := mcppool.MCPNameForNodeClass(defaultEC2NodeClassName)
		rewritten, err := mcppool.RewriteIgnitionMCSPath(userData, mcpName)
		if err != nil {
			log.Info("could not rewrite MCS path in userData, using original", "error", err)
			dv.userData = userData
		} else {
			dv.userData = rewritten
		}
	}

	return &nodeClassDefaults{
		instanceProfile:     dv.instanceProfile,
		amiID:               dv.amiID,
		securityGroupNames:  dv.securityGroupNames,
		blockDeviceMappings: dv.blockDeviceMappings,
		userData:            dv.userData,
	}, nil
}

func discoverFromMachineSet(ctx context.Context, c client.Client) (*discoveredValues, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   machineSetGVR.Group,
		Version: machineSetGVR.Version,
		Kind:    "MachineSetList",
	})

	if err := c.List(ctx, list, client.InNamespace(machineAPINamespace)); err != nil {
		return nil, fmt.Errorf("failed to list MachineSets: %w", err)
	}

	var ms *unstructured.Unstructured
	for i := range list.Items {
		role, _, _ := unstructured.NestedString(list.Items[i].Object,
			"spec", "template", "metadata", "labels", workerRoleLabel)
		if role == workerRoleValue {
			ms = &list.Items[i]
			break
		}
	}
	if ms == nil {
		return nil, fmt.Errorf("no worker MachineSets found in %s", machineAPINamespace)
	}

	providerSpecRaw, found, err := unstructured.NestedMap(ms.Object, "spec", "template", "spec", "providerSpec", "value")
	if err != nil || !found {
		return nil, fmt.Errorf("failed to extract providerSpec from MachineSet %s: %w", ms.GetName(), err)
	}

	rawJSON, err := json.Marshal(providerSpecRaw)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal providerSpec: %w", err)
	}

	var provider awsMachineProviderConfig
	if err := json.Unmarshal(rawJSON, &provider); err != nil {
		return nil, fmt.Errorf("failed to unmarshal providerSpec: %w", err)
	}

	var sgNames []string
	for _, sg := range provider.SecurityGroups {
		for _, f := range sg.Filters {
			if f.Name == "tag:Name" {
				sgNames = append(sgNames, f.Values...)
			}
		}
	}

	var bdm []*awskarpenterv1.BlockDeviceMapping
	for _, bd := range provider.BlockDevices {
		bdm = append(bdm, &awskarpenterv1.BlockDeviceMapping{
			DeviceName: ptr.To("/dev/xvda"),
			EBS: &awskarpenterv1.BlockDevice{
				VolumeSize: ptr.To(resource.MustParse(fmt.Sprintf("%dGi", bd.EBS.VolumeSize))),
				VolumeType: ptr.To(bd.EBS.VolumeType),
				Encrypted:  ptr.To(bd.EBS.Encrypted),
			},
		})
	}
	if len(bdm) == 0 {
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
	}, nil
}

func fallbackDefaults(infraName string) *discoveredValues {
	return &discoveredValues{
		instanceProfile: fmt.Sprintf("%s-worker-profile", infraName),
		securityGroupNames: []string{
			fmt.Sprintf("%s-node", infraName),
			fmt.Sprintf("%s-lb", infraName),
		},
		blockDeviceMappings: defaultBlockDeviceMappings(),
		userDataSecretName:  defaultUserDataSecret,
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

func defaultBlockDeviceMappings() []*awskarpenterv1.BlockDeviceMapping {
	return []*awskarpenterv1.BlockDeviceMapping{
		{
			DeviceName: ptr.To("/dev/xvda"),
			EBS: &awskarpenterv1.BlockDevice{
				VolumeSize: ptr.To(resource.MustParse(defaultRootVolumeSize)),
				VolumeType: ptr.To("gp3"),
				Encrypted:  ptr.To(true),
			},
		},
	}
}
