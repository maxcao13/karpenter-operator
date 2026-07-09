package controllers

import (
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers/clusteroperator"
	"github.com/openshift/karpenter-operator/pkg/controllers/crd"
	"github.com/openshift/karpenter-operator/pkg/controllers/deployment"
	"github.com/openshift/karpenter-operator/pkg/controllers/nodeclass"

	configv1 "github.com/openshift/api/config/v1"

	ctrl "sigs.k8s.io/controller-runtime"
)

type Controller interface {
	SetupWithManager(ctrl.Manager) error
}

type Config struct {
	Namespace       string
	KarpenterImage  string
	ClusterName     string
	ClusterEndpoint string
	InfraName       string
	ReleaseVersion  string
	TopologyMode    configv1.TopologyMode
	CloudProvider   common.CloudProvider
}

func NewControllers(mgr ctrl.Manager, cfg *Config) ([]Controller, error) {
	// This abstraction makes it simple to turn on/off commonControllers based on the topology.
	commonControllers := []Controller{
		crd.NewController(mgr, &crd.ControllerConfig{
			Namespace: cfg.Namespace,
			CRDs:      append(assets.CoreCRDs, cfg.CloudProvider.CRDs()...),
		}),
		deployment.NewController(mgr, &deployment.ControllerConfig{
			Namespace:       cfg.Namespace,
			KarpenterImage:  cfg.KarpenterImage,
			ClusterName:     cfg.ClusterName,
			ClusterEndpoint: cfg.ClusterEndpoint,
			CloudProvider:   cfg.CloudProvider,
		}),
		nodeclass.NewController(mgr, &nodeclass.ControllerConfig{
			InfraName:  cfg.InfraName,
			Reconciler: cfg.CloudProvider.NodeClass(),
		}),
	}

	switch cfg.TopologyMode {
	case configv1.HighlyAvailableTopologyMode, configv1.SingleReplicaTopologyMode:
		// OpenShift Standalone
		commonControllers = append(commonControllers,
			clusteroperator.NewController(mgr, &clusteroperator.ControllerConfig{
				Namespace:                cfg.Namespace,
				ReleaseVersion:           cfg.ReleaseVersion,
				AdditionalRelatedObjects: cfg.CloudProvider.RelatedObjects(),
			}),
		)
	case configv1.ExternalTopologyMode:
		// HyperShift
	default:
		return nil, fmt.Errorf("unknown/unsupported topology mode: %s", cfg.TopologyMode)
	}
	return commonControllers, nil
}

func Setup(mgr ctrl.Manager, controllers ...Controller) error {
	for _, c := range controllers {
		if err := c.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("failed to setup controller: %w", err)
		}
	}
	return nil
}
