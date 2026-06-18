package crd

import (
	"context"
	"fmt"

	"github.com/openshift/karpenter-operator/pkg/assets"

	appsv1 "k8s.io/api/apps/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

type ControllerConfig struct {
	Namespace string
	CRDs      []*apiextensionsv1.CustomResourceDefinition
}

// Controller reconciles the Karpenter CRDs (NodePool, NodeClaim, EC2NodeClass, etc.)
// so the operand can start its watches and caches.
type Controller struct {
	client client.Client
	config *ControllerConfig
}

func NewController(mgr ctrl.Manager, cfg *ControllerConfig) *Controller {
	return &Controller{
		client: mgr.GetClient(),
		config: cfg,
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
	op, err := controllerutil.CreateOrUpdate(ctx, c.client, crd, func() error {
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
	managedCRDs := crdNames(append(assets.CoreCRDs, c.config.CRDs...))
	reconcileRequest := []ctrl.Request{{NamespacedName: client.ObjectKey{
		Namespace: c.config.Namespace,
		Name:      "karpenter-operator",
	}}}

	return ctrl.NewControllerManagedBy(mgr).
		Named(c.Name()).
		For(&appsv1.Deployment{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(o client.Object) bool {
			return o.GetNamespace() == c.config.Namespace && o.GetName() == "karpenter-operator"
		}))).
		Watches(&apiextensionsv1.CustomResourceDefinition{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, o client.Object) []ctrl.Request {
				if !managedCRDs[o.GetName()] {
					return nil
				}
				return reconcileRequest
			},
		)).
		Complete(c)
}

func crdNames(crds []*apiextensionsv1.CustomResourceDefinition) map[string]bool {
	m := make(map[string]bool, len(crds))
	for _, crd := range crds {
		m[crd.Name] = true
	}
	return m
}
