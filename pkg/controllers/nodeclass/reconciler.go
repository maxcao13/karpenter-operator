package nodeclass

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	defaultNodeClassName    = "default"
	machineAPINamespace     = "openshift-machine-api"
)

// NodeClassReconciler is the subset of cloud.CloudProvider the nodeclass
// controller needs.
type NodeClassReconciler interface {
	ReconcileDefaultNodeClass(ctx context.Context, c client.Client, infraName string) error
	NodeClassObject() client.Object
}

// Reconciler creates and maintains a default provider-specific NodeClass with
// infrastructure defaults discovered from the cluster. The cloud-specific logic
// is delegated to the CloudProvider implementation.
type Reconciler struct {
	Client        client.Client
	InfraName     string
	CloudProvider NodeClassReconciler
}

func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	r.Client = mgr.GetClient()

	c, err := controller.New("default-nodeclass", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return fmt.Errorf("failed to construct default-nodeclass controller: %w", err)
	}

	nodeClassObj := r.CloudProvider.NodeClassObject()
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), nodeClassObj, handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, o client.Object) []ctrl.Request {
			if o.GetName() != defaultNodeClassName {
				return nil
			}
			return []ctrl.Request{{NamespacedName: client.ObjectKeyFromObject(o)}}
		},
	))); err != nil {
		return fmt.Errorf("failed to watch NodeClass: %w", err)
	}

	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), &corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, o client.Object) []ctrl.Request {
			if o.GetNamespace() != machineAPINamespace {
				return nil
			}
			return []ctrl.Request{{NamespacedName: types.NamespacedName{Name: defaultNodeClassName}}}
		},
	))); err != nil {
		return fmt.Errorf("failed to watch Secrets: %w", err)
	}

	initialSync := make(chan event.GenericEvent)
	if err := c.Watch(source.Channel(initialSync, &handler.EnqueueRequestForObject{})); err != nil {
		return fmt.Errorf("failed to watch initial sync channel: %w", err)
	}
	go func() {
		obj := nodeClassObj.DeepCopyObject().(client.Object)
		obj.SetName(defaultNodeClassName)
		initialSync <- event.GenericEvent{Object: obj}
	}()

	return nil
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.Info("Reconciling default NodeClass")

	if err := r.CloudProvider.ReconcileDefaultNodeClass(ctx, r.Client, r.InfraName); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile default NodeClass: %w", err)
	}

	return ctrl.Result{}, nil
}

