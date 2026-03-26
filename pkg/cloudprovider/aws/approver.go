package aws

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	certificatesv1 "k8s.io/api/certificates/v1"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift/karpenter-operator/pkg/util"
)

// EC2API defines the subset of EC2 operations used for CSR verification.
type EC2API interface {
	DescribeInstances(ctx context.Context, input *awsec2.DescribeInstancesInput, optFns ...func(*awsec2.Options)) (*awsec2.DescribeInstancesOutput, error)
}

func (p *Provider) GetInstanceDNSNames(ctx context.Context, nodeClaims []karpenterv1.NodeClaim) ([]string, error) {
	var instanceIDs []string
	for _, claim := range nodeClaims {
		providerID := claim.Status.ProviderID
		instanceID := providerID[strings.LastIndex(providerID, "/")+1:]
		instanceIDs = append(instanceIDs, instanceID)
	}

	if len(instanceIDs) == 0 {
		return nil, nil
	}

	output, err := p.ec2Client.DescribeInstances(ctx, &awsec2.DescribeInstancesInput{
		InstanceIds: instanceIDs,
	})
	if err != nil {
		return nil, err
	}

	var dnsNames []string
	for _, reservation := range output.Reservations {
		for _, instance := range reservation.Instances {
			dnsNames = append(dnsNames, aws.ToString(instance.PrivateDnsName))
		}
	}
	return dnsNames, nil
}

func (p *Provider) AuthorizeCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, c client.Client) (bool, error) {
	switch csr.Spec.SignerName {
	case certificatesv1.KubeAPIServerClientKubeletSignerName:
		return p.authorizeClientCSR(ctx, csr, c)
	case certificatesv1.KubeletServingSignerName:
		return p.authorizeServingCSR(ctx, csr, c)
	}
	return false, fmt.Errorf("unrecognized signerName %s", csr.Spec.SignerName)
}

func (p *Provider) authorizeClientCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, c client.Client) (bool, error) {
	x509cr, err := util.ParseCSR(csr.Spec.Request)
	if err != nil {
		return false, err
	}

	nodeName := strings.TrimPrefix(x509cr.Subject.CommonName, "system:node:")
	if len(nodeName) == 0 {
		return false, fmt.Errorf("subject common name does not have a valid node name")
	}

	nodeClaims, err := listNodeClaims(ctx, c)
	if err != nil {
		return false, err
	}

	// For client CSRs, only consider NodeClaims that haven't been assigned
	// a node yet — these are the ones in the process of bootstrapping.
	filteredNodeClaims := slices.DeleteFunc(nodeClaims, func(claim karpenterv1.NodeClaim) bool {
		return claim.Status.NodeName != ""
	})

	dnsNames, err := p.GetInstanceDNSNames(ctx, filteredNodeClaims)
	if err != nil {
		return false, err
	}

	return slices.Contains(dnsNames, nodeName), nil
}

func (p *Provider) authorizeServingCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, c client.Client) (bool, error) {
	nodeName := strings.TrimPrefix(csr.Spec.Username, "system:node:")
	if len(nodeName) == 0 {
		return false, fmt.Errorf("csr username does not have a valid node name")
	}

	nodeClaim, err := findTargetNodeClaim(ctx, c, nodeName)
	if err != nil || nodeClaim == nil {
		return false, err
	}

	dnsNames, err := p.GetInstanceDNSNames(ctx, []karpenterv1.NodeClaim{*nodeClaim})
	if err != nil {
		return false, err
	}

	return slices.Contains(dnsNames, nodeName), nil
}

func listNodeClaims(ctx context.Context, c client.Client) ([]karpenterv1.NodeClaim, error) {
	nodeClaimList := &karpenterv1.NodeClaimList{}
	if err := c.List(ctx, nodeClaimList); err != nil {
		return nil, fmt.Errorf("failed to list NodeClaims: %w", err)
	}
	return nodeClaimList.Items, nil
}

func findTargetNodeClaim(ctx context.Context, c client.Client, nodeName string) (*karpenterv1.NodeClaim, error) {
	nodeClaimList, err := listNodeClaims(ctx, c)
	if err != nil {
		return nil, err
	}

	for _, nodeClaim := range nodeClaimList {
		if nodeClaim.Status.NodeName == nodeName {
			return &nodeClaim, nil
		}
	}
	return nil, nil
}
