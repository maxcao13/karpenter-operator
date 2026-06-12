package operator

import (
	"context"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers"

	configv1 "github.com/openshift/api/config/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var scheme = runtime.NewScheme()

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = configv1.Install(scheme)
}

func Run(ctx context.Context, opts Options) error {
	setupLog := ctrl.Log.WithName("setup")

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load kube config: %w", err)
	}

	infra, err := discoverInfrastructure(ctx, restCfg)
	if err != nil {
		return fmt.Errorf("failed to discover infrastructure: %w", err)
	}

	provider, err := cloudprovider.GetCloudProvider(ctx, infra)
	if err != nil {
		return fmt.Errorf("failed to initialize cloud provider: %w", err)
	}

	cfg := opts.ResolveControllerConfig(infra, provider)

	setupLog.Info("infrastructure",
		"platform", infra.PlatformType,
		"topologyMode", infra.TopologyMode,
		"region", infra.Region,
		"clusterName", cfg.ClusterName,
		"clusterEndpoint", cfg.ClusterEndpoint,
		"karpenterImage", cfg.KarpenterImage,
	)

	if err := provider.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add cloud provider types to scheme: %w", err)
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				opts.Namespace: {},
			},
		},
		Metrics:                server.Options{BindAddress: opts.MetricsAddr},
		HealthProbeBindAddress: opts.ProbeAddr,
		LeaderElection:         opts.LeaderElect,
		LeaderElectionID:       "karpenter-operator.openshift.io",
	})
	if err != nil {
		return fmt.Errorf("failed to create manager: %w", err)
	}

	if err := controllers.Setup(mgr, controllers.NewControllers(mgr, cfg)...); err != nil {
		return err
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("failed to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("failed to set up ready check: %w", err)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("failed to start manager: %w", err)
	}

	return nil
}

func discoverInfrastructure(ctx context.Context, cfg *rest.Config) (common.InfrastructureInfo, error) {
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return common.InfrastructureInfo{}, fmt.Errorf("failed to create client for infrastructure discovery: %w", err)
	}

	infra := &configv1.Infrastructure{}
	if err := c.Get(ctx, types.NamespacedName{Name: "cluster"}, infra); err != nil {
		return common.InfrastructureInfo{}, fmt.Errorf("failed to get Infrastructure CR: %w", err)
	}
	if infra.Status.PlatformStatus == nil {
		return common.InfrastructureInfo{}, fmt.Errorf("infrastructure status.platformStatus is nil")
	}
	if infra.Status.InfrastructureName == "" {
		return common.InfrastructureInfo{}, fmt.Errorf("infrastructure status.infrastructureName is empty")
	}
	region := ""
	if infra.Status.PlatformStatus.AWS != nil {
		region = infra.Status.PlatformStatus.AWS.Region
	}

	return common.InfrastructureInfo{
		PlatformType:    infra.Status.PlatformStatus.Type,
		PlatformStatus:  *infra.Status.PlatformStatus,
		TopologyMode:    infra.Status.ControlPlaneTopology,
		Region:          region,
		InfraName:       infra.Status.InfrastructureName,
		ClusterEndpoint: infra.Status.APIServerInternalURL,
	}, nil
}
