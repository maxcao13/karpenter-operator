package controllers

import (
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers/clusteroperator"
	"github.com/openshift/karpenter-operator/pkg/controllers/crd"
	karpenterctrl "github.com/openshift/karpenter-operator/pkg/controllers/karpenter"

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
	ReleaseVersion  string
	CloudProvider   common.CloudProvider
}

func NewControllers(mgr ctrl.Manager, cfg *Config) []Controller {
	// This abstraction makes it simple to turn on/off controllers based on the topology.
	// TODO(maxcao13): Remove this TODO once we actually add topology detection and different interfaces for it.
	// e.g., If HCP Topology, append OpenshiftEC2NodeClass controller.
	// e.g., if Standalone Topology, add ClusterOperator controller.
	return []Controller{
		crd.NewController(mgr, &crd.ControllerConfig{
			Namespace: cfg.Namespace,
			CRDs:      append(assets.CoreCRDs, cfg.CloudProvider.CRDs()...),
		}),
		karpenterctrl.NewController(mgr, &karpenterctrl.ControllerConfig{
			Namespace:       cfg.Namespace,
			KarpenterImage:  cfg.KarpenterImage,
			ClusterName:     cfg.ClusterName,
			ClusterEndpoint: cfg.ClusterEndpoint,
			CloudProvider:   cfg.CloudProvider,
		}),
		clusteroperator.NewController(mgr, &clusteroperator.ControllerConfig{
			Namespace:                cfg.Namespace,
			ReleaseVersion:           cfg.ReleaseVersion,
			AdditionalRelatedObjects: cfg.CloudProvider.RelatedObjects(),
		}),
	}
}

func Setup(mgr ctrl.Manager, controllers ...Controller) error {
	for _, c := range controllers {
		if err := c.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("failed to setup controller: %w", err)
		}
	}
	return nil
}
