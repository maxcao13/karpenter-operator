package controllers

import (
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers/clusteroperator"
	"github.com/openshift/karpenter-operator/pkg/controllers/crd"
	karpenterctrl "github.com/openshift/karpenter-operator/pkg/controllers/karpenter"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

type Controller interface {
	Name() string
	SetupWithManager(ctrl.Manager) error
}

type Config struct {
	Namespace         string
	KarpenterImage    string
	ClusterName       string
	ClusterEndpoint   string
	ReleaseVersion    string
	ManagementCluster bool
	CloudProvider     common.CloudProvider

	// HostedCluster is a secondary cluster.Cluster targeting the hosted cluster where
	// Karpenter CRDs (NodePool, NodeClaim, NodeClass) live. Nil in standalone mode.
	HostedCluster cluster.Cluster
}

func NewControllers(mgr ctrl.Manager, cfg *Config) []Controller {
	// This abstraction makes it simple to turn on/off controllers based on whether the
	// operator is running in management cluster mode (see operator.Options.ManagementCluster).
	var controllers []Controller

	crdCfg := &crd.ControllerConfig{
		Namespace:     cfg.Namespace,
		CRDs:          append(assets.CoreCRDs, cfg.CloudProvider.CRDs()...),
		HostedCluster: cfg.HostedCluster,
	}
	controllers = append(controllers, crd.NewController(mgr, crdCfg))

	if !cfg.ManagementCluster {
		controllers = append(controllers,
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
			}))
	}

	return controllers
}

func Setup(mgr ctrl.Manager, controllers ...Controller) error {
	for _, c := range controllers {
		if err := c.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("failed to setup controller: %w", err)
		}
	}
	return nil
}
