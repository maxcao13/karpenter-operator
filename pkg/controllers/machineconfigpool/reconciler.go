package machineconfigpool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	mcpPrefix      = "karpenter-"
	managedLabel   = "karpenter-operator.openshift.io/managed"
	kubeletCfgName = "set-karpenter-taint"
	mcpGroup       = "machineconfiguration.openshift.io"
	mcpVersion     = "v1"
)

var (
	mcpGVK = schema.GroupVersionKind{Group: mcpGroup, Version: mcpVersion, Kind: "MachineConfigPool"}
	kcGVK  = schema.GroupVersionKind{Group: mcpGroup, Version: mcpVersion, Kind: "KubeletConfig"}
)

// NodeClassLister is the subset of CloudProvider needed by this controller.
type NodeClassLister interface {
	NodeClassObject() client.Object
	NodeClassLabel() string
}

type Reconciler struct {
	Client        client.Client
	CloudProvider NodeClassLister
}

func (r *Reconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	r.Client = mgr.GetClient()

	c, err := controller.New("karpenter-machineconfigpool", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return fmt.Errorf("failed to construct karpenter-machineconfigpool controller: %w", err)
	}

	// Watch the NodeClass type (e.g. EC2NodeClass) to trigger reconciliation.
	ncObj := r.CloudProvider.NodeClassObject()
	if err := c.Watch(source.Kind[client.Object](mgr.GetCache(), ncObj, handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, o client.Object) []ctrl.Request {
			return []ctrl.Request{{NamespacedName: types.NamespacedName{Name: o.GetName()}}}
		},
	))); err != nil {
		return fmt.Errorf("failed to watch NodeClass for MCP controller: %w", err)
	}

	// Fire once at startup to ensure the shared KubeletConfig exists.
	initialSync := make(chan event.GenericEvent)
	if err := c.Watch(source.Channel(initialSync, &handler.EnqueueRequestForObject{})); err != nil {
		return fmt.Errorf("failed to watch initial sync channel: %w", err)
	}
	go func() {
		initialSync <- event.GenericEvent{Object: r.CloudProvider.NodeClassObject()}
	}()

	return nil
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	ncName := req.Name
	if ncName == "" {
		ncName = "default"
	}
	mcpName := mcpPrefix + ncName
	log.Info("Reconciling MachineConfigPool for NodeClass", "nodeclass", ncName, "mcp", mcpName)

	if err := r.reconcileMCP(ctx, mcpName, ncName); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile MachineConfigPool %s: %w", mcpName, err)
	}

	if err := r.reconcileKubeletConfig(ctx); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile KubeletConfig %s: %w", kubeletCfgName, err)
	}

	return ctrl.Result{}, nil
}

// reconcileMCP creates or updates a MachineConfigPool that inherits all worker
// MachineConfigs and selects nodes belonging to the given NodeClass.
func (r *Reconciler) reconcileMCP(ctx context.Context, mcpName, ncName string) error {
	mcp := &unstructured.Unstructured{}
	mcp.SetGroupVersionKind(mcpGVK)

	err := r.Client.Get(ctx, types.NamespacedName{Name: mcpName}, mcp)
	if errors.IsNotFound(err) {
		return r.createMCP(ctx, mcpName, ncName)
	}
	if err != nil {
		return err
	}

	return r.updateMCP(ctx, mcp, mcpName, ncName)
}

func (r *Reconciler) createMCP(ctx context.Context, mcpName, ncName string) error {
	mcp := r.desiredMCP(mcpName, ncName)
	return r.Client.Create(ctx, mcp)
}

func (r *Reconciler) updateMCP(ctx context.Context, existing *unstructured.Unstructured, mcpName, ncName string) error {
	desired := r.desiredMCP(mcpName, ncName)
	desired.SetResourceVersion(existing.GetResourceVersion())
	return r.Client.Update(ctx, desired)
}

func (r *Reconciler) desiredMCP(mcpName, ncName string) *unstructured.Unstructured {
	nodeClassLabelKey := r.CloudProvider.NodeClassLabel()

	mcp := &unstructured.Unstructured{}
	mcp.SetGroupVersionKind(mcpGVK)
	mcp.SetName(mcpName)
	mcp.SetLabels(map[string]string{
		managedLabel: "true",
	})

	mcp.Object["spec"] = map[string]interface{}{
		"nodeSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{
				nodeClassLabelKey: ncName,
			},
		},
		"machineConfigSelector": map[string]interface{}{
			"matchExpressions": []interface{}{
				map[string]interface{}{
					"key":      "machineconfiguration.openshift.io/role",
					"operator": "In",
					"values":   []interface{}{"worker", mcpName},
				},
			},
		},
		"paused": true,
	}

	return mcp
}

// reconcileKubeletConfig creates or updates the shared KubeletConfig that
// configures registerWithTaints on all karpenter MCPs.
func (r *Reconciler) reconcileKubeletConfig(ctx context.Context) error {
	kc := &unstructured.Unstructured{}
	kc.SetGroupVersionKind(kcGVK)

	err := r.Client.Get(ctx, types.NamespacedName{Name: kubeletCfgName}, kc)
	if errors.IsNotFound(err) {
		return r.createKubeletConfig(ctx)
	}
	if err != nil {
		return err
	}

	return r.updateKubeletConfig(ctx, kc)
}

func (r *Reconciler) createKubeletConfig(ctx context.Context) error {
	kc := desiredKubeletConfig()
	return r.Client.Create(ctx, kc)
}

func (r *Reconciler) updateKubeletConfig(ctx context.Context, existing *unstructured.Unstructured) error {
	kc := desiredKubeletConfig()
	kc.SetResourceVersion(existing.GetResourceVersion())
	return r.Client.Update(ctx, kc)
}

func desiredKubeletConfig() *unstructured.Unstructured {
	kc := &unstructured.Unstructured{}
	kc.SetGroupVersionKind(kcGVK)
	kc.SetName(kubeletCfgName)

	kc.Object["spec"] = map[string]interface{}{
		"machineConfigPoolSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{
				managedLabel: "true",
			},
		},
		"kubeletConfig": map[string]interface{}{
			"registerWithTaints": []interface{}{
				map[string]interface{}{
					"key":    "karpenter.sh/unregistered",
					"value":  "true",
					"effect": "NoExecute",
				},
			},
		},
	}

	return kc
}

// MCPNameForNodeClass returns the MachineConfigPool name that corresponds to a
// given NodeClass name. Exported so the nodeclass controller can rewrite the
// MCS endpoint in the Ignition userData.
func MCPNameForNodeClass(ncName string) string {
	return mcpPrefix + ncName
}

// RewriteIgnitionMCSPath rewrites the MCS config endpoint in an Ignition JSON
// payload from /config/worker to /config/<mcpName>.
func RewriteIgnitionMCSPath(ignitionJSON string, mcpName string) (string, error) {
	var ign map[string]interface{}
	if err := json.Unmarshal([]byte(ignitionJSON), &ign); err != nil {
		return "", fmt.Errorf("failed to parse Ignition JSON: %w", err)
	}

	ignCfg, ok := ign["ignition"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("ignition key missing or invalid")
	}
	cfgBlock, ok := ignCfg["config"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("ignition.config missing or invalid")
	}
	mergeList, ok := cfgBlock["merge"].([]interface{})
	if !ok || len(mergeList) == 0 {
		return "", fmt.Errorf("ignition.config.merge missing or empty")
	}

	for _, item := range mergeList {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		src, ok := entry["source"].(string)
		if !ok {
			continue
		}
		entry["source"] = strings.Replace(src, "/config/worker", "/config/"+mcpName, 1)
	}

	out, err := json.Marshal(ign)
	if err != nil {
		return "", fmt.Errorf("failed to re-marshal Ignition JSON: %w", err)
	}
	return string(out), nil
}

// GetMCPPausedStatus returns whether a MachineConfigPool is paused.
func GetMCPPausedStatus(ctx context.Context, c client.Client, mcpName string) (bool, error) {
	mcp := &unstructured.Unstructured{}
	mcp.SetGroupVersionKind(mcpGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: mcpName}, mcp); err != nil {
		return false, err
	}
	paused, _, _ := unstructured.NestedBool(mcp.Object, "spec", "paused")
	return paused, nil
}

// SetMCPAnnotation sets an annotation on a MachineConfigPool.
func SetMCPAnnotation(ctx context.Context, c client.Client, mcpName, key, value string) error {
	mcp := &unstructured.Unstructured{}
	mcp.SetGroupVersionKind(mcpGVK)
	if err := c.Get(ctx, types.NamespacedName{Name: mcpName}, mcp); err != nil {
		return err
	}

	patch := client.MergeFrom(mcp.DeepCopy())
	annotations := mcp.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[key] = value
	mcp.SetAnnotations(annotations)
	return c.Patch(ctx, mcp, patch)
}

// Ensure the Reconciler satisfies the interface at compile time.
var _ interface {
	Reconcile(context.Context, ctrl.Request) (ctrl.Result, error)
} = &Reconciler{}

