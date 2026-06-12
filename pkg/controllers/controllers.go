package controllers

import (
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers/deployment"

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
	return []Controller{
		deployment.NewController(mgr, &deployment.ControllerConfig{
			Namespace:       cfg.Namespace,
			KarpenterImage:  cfg.KarpenterImage,
			ClusterName:     cfg.ClusterName,
			ClusterEndpoint: cfg.ClusterEndpoint,
			CloudProvider:   cfg.CloudProvider,
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
