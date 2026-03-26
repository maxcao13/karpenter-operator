package operator

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider"
	"github.com/openshift/karpenter-operator/pkg/controllers/deployment"
	"github.com/openshift/karpenter-operator/pkg/controllers/machineapprover"
	"github.com/openshift/karpenter-operator/pkg/controllers/machineconfigpool"
	"github.com/openshift/karpenter-operator/pkg/controllers/nodeclass"
)

func newBaseScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = configv1.AddToScheme(scheme)

	karpenterGV := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	metav1.AddToGroupVersion(scheme, karpenterGV)
	scheme.AddKnownTypes(karpenterGV, &karpenterv1.NodePool{}, &karpenterv1.NodePoolList{})
	scheme.AddKnownTypes(karpenterGV, &karpenterv1.NodeClaim{}, &karpenterv1.NodeClaimList{})

	return scheme
}

func Run(ctx context.Context, opts Options) error {
	setupLog := ctrl.Log.WithName("setup")

	// Discover infrastructure first so we can select the cloud provider.
	infra, err := discoverInfrastructure(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to discover infrastructure: %w", err)
	}

	provider, err := cloudprovider.GetCloudProvider(ctx, infra.Status.PlatformStatus, infra.Status.InfrastructureName, infra.Status.APIServerInternalURL)
	if err != nil {
		return fmt.Errorf("failed to initialize cloud provider: %w", err)
	}
	setupLog.Info("infrastructure",
		"platform", infra.Status.PlatformStatus.Type,
		"region", provider.Region(),
		"clusterName", infra.Status.InfrastructureName,
		"clusterEndpoint", infra.Status.APIServerInternalURL,
	)

	scheme := newBaseScheme()
	if err := provider.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to add cloud provider types to scheme: %w", err)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                server.Options{BindAddress: opts.MetricsAddr},
		HealthProbeBindAddress: opts.ProbeAddr,
		LeaderElection:         opts.LeaderElect,
		LeaderElectionID:       "karpenter-operator.openshift.io",
	})
	if err != nil {
		return fmt.Errorf("failed to create manager: %w", err)
	}

	clusterName := opts.ClusterName
	if clusterName == "" {
		clusterName = infra.Status.InfrastructureName
	}
	clusterEndpoint := opts.ClusterEndpoint
	if clusterEndpoint == "" {
		clusterEndpoint = infra.Status.APIServerInternalURL
	}

	deployReconciler := deployment.Reconciler{
		Namespace:       opts.Namespace,
		KarpenterImage:  opts.KarpenterImage,
		ClusterName:     clusterName,
		ClusterEndpoint: clusterEndpoint,
		CloudProvider:   provider,
	}
	if err := deployReconciler.SetupWithManager(ctx, mgr); err != nil {
		return fmt.Errorf("failed to setup karpenter deployment reconciler: %w", err)
	}

	nodeclassReconciler := nodeclass.Reconciler{
		InfraName:     clusterName,
		CloudProvider: provider,
	}
	if err := nodeclassReconciler.SetupWithManager(ctx, mgr); err != nil {
		return fmt.Errorf("failed to setup default nodeclass reconciler: %w", err)
	}

	mac := machineapprover.MachineApproverController{
		CloudProvider: provider,
	}
	if err := mac.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup machine approver controller: %w", err)
	}

	mcpReconciler := machineconfigpool.Reconciler{
		CloudProvider: provider,
	}
	if err := mcpReconciler.SetupWithManager(ctx, mgr); err != nil {
		return fmt.Errorf("failed to setup machineconfigpool reconciler: %w", err)
	}

	relatedObjs := relatedObjects(opts.Namespace)
	relatedObjs = append(relatedObjs, provider.RelatedObjects()...)

	statusCfg := &StatusReporterConfig{
		OperandName:      "karpenter",
		OperandNamespace: opts.Namespace,
		ReleaseVersion:   opts.ReleaseVersion,
		RelatedObjects:   relatedObjs,
	}
	statusReporter, err := NewStatusReporter(mgr, statusCfg)
	if err != nil {
		return fmt.Errorf("failed to create status reporter: %w", err)
	}
	if err := mgr.Add(statusReporter); err != nil {
		return fmt.Errorf("failed to add status reporter: %w", err)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("failed to set up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("failed to set up ready check: %w", err)
	}

	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("failed to start manager: %w", err)
	}

	return nil
}

// discoverInfrastructure reads the Infrastructure CR. Options fields are
// used as overrides; if unset, values come from the CR.
func discoverInfrastructure(ctx context.Context, opts Options) (*configv1.Infrastructure, error) {
	scheme := runtime.NewScheme()
	_ = configv1.AddToScheme(scheme)

	c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create client for infrastructure discovery: %w", err)
	}

	infra := &configv1.Infrastructure{}
	if err := c.Get(ctx, types.NamespacedName{Name: "cluster"}, infra); err != nil {
		return nil, fmt.Errorf("failed to get Infrastructure CR: %w", err)
	}
	return infra, nil
}

func relatedObjects(namespace string) []configv1.ObjectReference {
	return []configv1.ObjectReference{
		{Group: "", Resource: "namespaces", Name: namespace},
		{Group: "karpenter.sh", Resource: "nodepools"},
		{Group: "karpenter.sh", Resource: "nodeclaims"},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "karpenter-operator"},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "karpenter-operator"},
	}
}
