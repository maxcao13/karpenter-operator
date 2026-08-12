package crd

import (
	"context"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/samber/lo"
)

type ControllerConfig struct {
	Namespace string
	CRDs      []*apiextensionsv1.CustomResourceDefinition

	// HostedCluster targets a remote cluster for CRD writes and watches.
	// When nil, the controller uses the manager's own client and cache (standalone mode).
	HostedCluster cluster.Cluster
}

// Controller reconciles the Karpenter CRDs (NodePool, NodeClaim, EC2NodeClass, etc.)
// so the operand can start its watches and caches.
type Controller struct {
	targetClient client.Client // writes CRDs to the target cluster
	targetCache  cache.Cache   // watches CRDs on the target cluster
	config       *ControllerConfig
}

func NewController(mgr ctrl.Manager, cfg *ControllerConfig) *Controller {
	cl := mgr.GetClient()
	tc := mgr.GetCache()
	if cfg.HostedCluster != nil {
		cl = cfg.HostedCluster.GetClient()
		tc = cfg.HostedCluster.GetCache()
	}
	return &Controller{
		targetClient: cl,
		targetCache:  tc,
		config:       cfg,
	}
}

func (c *Controller) Name() string {
	return "crd"
}

func (c *Controller) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling karpenter CRDs")

	for _, desired := range c.config.CRDs {
		if err := c.applyCRD(ctx, desired); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to apply CRD %s: %w", desired.Name, err)
		}
	}

	return ctrl.Result{}, nil
}

func (c *Controller) applyCRD(ctx context.Context, desired *apiextensionsv1.CustomResourceDefinition) error {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	crd.Name = desired.Name
	op, err := controllerutil.CreateOrUpdate(ctx, c.targetClient, crd, func() error {
		crd.Spec = *desired.Spec.DeepCopy()
		return nil
	})
	if err != nil {
		return err
	}
	if op == controllerutil.OperationResultCreated {
		log.FromContext(ctx).Info("created CRD", "name", desired.Name)
	}
	return nil
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	ctrlr, err := controller.New(c.Name(), mgr, controller.Options{Reconciler: c})
	if err != nil {
		return fmt.Errorf("failed to create controller: %w", err)
	}

	managedCRDs := lo.SliceToMap(c.config.CRDs, func(crd *apiextensionsv1.CustomResourceDefinition) (string, bool) {
		return crd.Name, true
	})

	// Watch CRDs on the target cluster (hosted cluster in HCP mode, in-cluster in standalone).
	if err := ctrlr.Watch(source.Kind(c.targetCache, &apiextensionsv1.CustomResourceDefinition{},
		handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, o *apiextensionsv1.CustomResourceDefinition) []reconcile.Request {
			if !managedCRDs[o.GetName()] {
				return nil
			}
			return []reconcile.Request{{NamespacedName: client.ObjectKey{
				Namespace: c.config.Namespace,
				Name:      "karpenter-operator",
			}}}
		}),
	)); err != nil {
		return fmt.Errorf("failed to watch CRDs: %w", err)
	}

	// Trigger initial reconcile at startup to create CRDs before any watches fire.
	initialSync := make(chan event.GenericEvent, 1)
	if err := ctrlr.Watch(source.Channel(initialSync, &handler.EnqueueRequestForObject{})); err != nil {
		return fmt.Errorf("failed to watch initial sync channel: %w", err)
	}
	go func() {
		initialSync <- event.GenericEvent{Object: &apiextensionsv1.CustomResourceDefinition{}}
	}()

	return nil
}
