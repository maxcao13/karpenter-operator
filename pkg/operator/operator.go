package operator

import (
	"context"
	"fmt"

	configv1 "github.com/openshift/api/config/v1"

	awskarpenterapis "github.com/aws/karpenter-provider-aws/pkg/apis"
	awskarpenterv1 "github.com/aws/karpenter-provider-aws/pkg/apis/v1"

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

	"github.com/openshift/karpenter-operator/pkg/controllers/deployment"
	"github.com/openshift/karpenter-operator/pkg/controllers/machineapprover"
)

func NewScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)
	_ = configv1.AddToScheme(scheme)

	karpenterGV := schema.GroupVersion{Group: "karpenter.sh", Version: "v1"}
	metav1.AddToGroupVersion(scheme, karpenterGV)
	scheme.AddKnownTypes(karpenterGV, &karpenterv1.NodePool{}, &karpenterv1.NodePoolList{})
	scheme.AddKnownTypes(karpenterGV, &karpenterv1.NodeClaim{}, &karpenterv1.NodeClaimList{})

	awsKarpenterGV := schema.GroupVersion{Group: awskarpenterapis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, awsKarpenterGV)
	scheme.AddKnownTypes(awsKarpenterGV, &awskarpenterv1.EC2NodeClass{}, &awskarpenterv1.EC2NodeClassList{})

	return scheme
}

func Run(ctx context.Context, opts Options) error {
	scheme := NewScheme()
	setupLog := ctrl.Log.WithName("setup")

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

	if err := discoverInfrastructure(ctx, mgr.GetAPIReader(), &opts); err != nil {
		return fmt.Errorf("failed to discover infrastructure: %w", err)
	}
	setupLog.Info("infrastructure", "region", opts.AWSRegion, "clusterName", opts.ClusterName, "clusterEndpoint", opts.ClusterEndpoint)

	deployReconciler := deployment.Reconciler{
		Namespace:       opts.Namespace,
		KarpenterImage:  opts.KarpenterImage,
		AWSRegion:       opts.AWSRegion,
		ClusterName:     opts.ClusterName,
		ClusterEndpoint: opts.ClusterEndpoint,
	}
	if err := deployReconciler.SetupWithManager(ctx, mgr); err != nil {
		return fmt.Errorf("failed to setup karpenter deployment reconciler: %w", err)
	}

	mac := machineapprover.MachineApproverController{}
	if err := mac.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup machine approver controller: %w", err)
	}

	statusCfg := &StatusReporterConfig{
		OperandName:      "karpenter",
		OperandNamespace: opts.Namespace,
		ReleaseVersion:   opts.ReleaseVersion,
		RelatedObjects:   relatedObjects(opts.Namespace),
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

// discoverInfrastructure reads the Infrastructure CR to fill in AWSRegion,
// ClusterName, and ClusterEndpoint when not already set via environment variables.
func discoverInfrastructure(ctx context.Context, r client.Reader, opts *Options) error {
	infra := &configv1.Infrastructure{}
	if err := r.Get(ctx, types.NamespacedName{Name: "cluster"}, infra); err != nil {
		return fmt.Errorf("failed to get Infrastructure CR: %w", err)
	}
	if opts.ClusterName == "" {
		opts.ClusterName = infra.Status.InfrastructureName
	}
	if opts.AWSRegion == "" && infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil {
		opts.AWSRegion = infra.Status.PlatformStatus.AWS.Region
	}
	if opts.ClusterEndpoint == "" {
		opts.ClusterEndpoint = infra.Status.APIServerInternalURL
	}
	return nil
}

func relatedObjects(namespace string) []configv1.ObjectReference {
	return []configv1.ObjectReference{
		{Group: "", Resource: "namespaces", Name: namespace},
		{Group: "karpenter.k8s.aws", Resource: "ec2nodeclasses"},
		{Group: "karpenter.sh", Resource: "nodepools"},
		{Group: "karpenter.sh", Resource: "nodeclaims"},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterroles", Name: "karpenter-operator"},
		{Group: "rbac.authorization.k8s.io", Resource: "clusterrolebindings", Name: "karpenter-operator"},
	}
}
