package operator

import (
	"context"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/assets"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider"
	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"
	"github.com/openshift/karpenter-operator/pkg/controllers"
	"github.com/openshift/karpenter-operator/pkg/util"

	configv1 "github.com/openshift/api/config/v1"
	machinev1beta1 "github.com/openshift/api/machine/v1beta1"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var scheme = runtime.NewScheme()

const machineAPINamespace = "openshift-machine-api"

func init() {
	_ = clientgoscheme.AddToScheme(scheme)
	_ = configv1.Install(scheme)
	_ = machinev1beta1.Install(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
}

func Run(ctx context.Context, opts Options) error { //nolint:gocyclo
	setupLog := ctrl.Log.WithName("setup")

	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("failed to load kube config: %w", err)
	}

	setupClient, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	infra, err := discoverInfrastructure(ctx, setupClient)
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
		"karpenterImage", cfg.KarpenterImage,
	)

	if err := provider.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add cloud provider types to scheme: %w", err)
	}

	allCRDs := append(assets.CoreCRDs, provider.CRDs()...)
	if err := ensureCRDs(ctx, setupClient, allCRDs); err != nil {
		return fmt.Errorf("failed to apply CRDs: %w", err)
	}

	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			ByObject: map[client.Object]cache.ByObject{
				&machinev1beta1.MachineSet{}: {Namespaces: map[string]cache.Config{
					machineAPINamespace: {},
				}},
				&corev1.Secret{}: {Namespaces: map[string]cache.Config{
					opts.Namespace:      {},
					machineAPINamespace: {},
				}},
			},
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

	ctrls, err := controllers.NewControllers(mgr, cfg)
	if err != nil {
		return fmt.Errorf("failed to create controllers: %w", err)
	}
	if err := controllers.Setup(mgr, ctrls...); err != nil {
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

func discoverInfrastructure(ctx context.Context, c client.Client) (common.InfrastructureInfo, error) {
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

// ensureCRDs applies all Karpenter CRDs before the manager starts so that
// controller informers can establish watches without racing the CRD controller.
func ensureCRDs(ctx context.Context, c client.Client, crds []*apiextensionsv1.CustomResourceDefinition) error {
	log := ctrl.Log.WithName("setup")
	for _, desired := range crds {
		var op controllerutil.OperationResult
		err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			crd.Name = desired.Name
			var e error
			op, e = controllerutil.CreateOrUpdate(ctx, c, crd, func() error {
				crd.Spec = *desired.Spec.DeepCopy()
				return nil
			})
			return e
		})
		if err != nil {
			return fmt.Errorf("failed to apply CRD %s: %w", desired.Name, err)
		}
		if op != controllerutil.OperationResultNone {
			log.Info("applied CRD", "name", desired.Name, "operation", op)
		}
	}

	for _, desired := range crds {
		if err := wait.PollUntilContextTimeout(ctx, util.CRDEstablishInterval, util.CRDEstablishTimeout, true, func(ctx context.Context) (bool, error) {
			crd := &apiextensionsv1.CustomResourceDefinition{}
			if err := c.Get(ctx, types.NamespacedName{Name: desired.Name}, crd); err != nil {
				return false, nil //nolint:nilerr // retry until CRD appears
			}
			return util.CRDEstablished(crd), nil
		}); err != nil {
			return fmt.Errorf("timed out waiting for CRD %s to become Established: %w", desired.Name, err)
		}
	}
	return nil
}
