package nodeclass

import (
	"context"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/cloudprovider/common"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type ControllerConfig struct {
	InfraName  string
	Reconciler common.NodeClassReconciler
}

type Controller struct {
	cache      cache.Cache
	client     client.Client
	infraName  string
	reconciler common.NodeClassReconciler
}

func NewController(mgr ctrl.Manager, cfg *ControllerConfig) *Controller {
	return &Controller{
		cache:      mgr.GetCache(),
		client:     mgr.GetClient(),
		infraName:  cfg.InfraName,
		reconciler: cfg.Reconciler,
	}
}

func (c *Controller) Name() string {
	return "nodeclass"
}

func (c *Controller) SetupWithManager(mgr ctrl.Manager) error {
	builder := ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		WatchesRawSource(source.Kind(c.cache, c.reconciler.NodeClassObject(),
			&handler.EnqueueRequestForObject{},
		))

	for _, src := range c.reconciler.AdditionalSources(c.cache) {
		builder = builder.WatchesRawSource(src)
	}

	return builder.Complete(c)
}

func (c *Controller) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log.FromContext(ctx).Info("reconciling NodeClass", "name", req.Name)

	if err := c.reconciler.ReconcileNodeClass(ctx, c.client, c.infraName, req.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile NodeClass %q: %w", req.Name, err)
	}

	return ctrl.Result{}, nil
}
