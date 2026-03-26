package machineapprover

import (
	"context"
	"fmt"
	"os"
	"time"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	certificatesv1client "k8s.io/client-go/kubernetes/typed/certificates/v1"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
	karpenterv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	"github.com/openshift/karpenter-operator/pkg/util"
)

const (
	nodeBootstrapperUsername = "system:serviceaccount:openshift-machine-config-operator:node-bootstrapper"
	nodeGroup                = "system:nodes"
)

// CSRAuthorizer is the subset of cloud.CloudProvider the machine approver
// controller needs.
type CSRAuthorizer interface {
	AuthorizeCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, c client.Client) (bool, error)
}

type MachineApproverController struct {
	CloudProvider CSRAuthorizer
	client        client.Client
	certClient    *certificatesv1client.CertificatesV1Client
}

func (r *MachineApproverController) SetupWithManager(mgr ctrl.Manager) error {
	certClient, err := certificatesv1client.NewForConfig(mgr.GetConfig())
	if err != nil {
		return err
	}
	r.certClient = certClient
	r.client = mgr.GetClient()

	c, err := controller.New("karpenter_machine_approver", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return fmt.Errorf("failed to construct karpenter_machine_approver controller: %w", err)
	}

	log := mgr.GetLogger()
	isRelevantCSR := func(csr *certificatesv1.CertificateSigningRequest) bool {
		if !util.IsCertificateRequestPending(csr) {
			return false
		}
		switch csr.Spec.SignerName {
		case certificatesv1.KubeAPIServerClientKubeletSignerName:
			if csr.Spec.Username != nodeBootstrapperUsername {
				log.Info("Ignoring csr because it is not from the node bootstrapper", "csr", csr.Name)
				return false
			}
		case certificatesv1.KubeletServingSignerName:
			if !sets.NewString(csr.Spec.Groups...).Has(nodeGroup) {
				log.Info("Ignoring csr because it does not have the system:nodes group", "csr", csr.Name)
				return false
			}
		default:
			return false
		}
		return true
	}

	if err := c.Watch(source.Kind(
		mgr.GetCache(),
		&certificatesv1.CertificateSigningRequest{},
		&handler.TypedEnqueueRequestForObject[*certificatesv1.CertificateSigningRequest]{},
		predicate.TypedFuncs[*certificatesv1.CertificateSigningRequest]{
			CreateFunc: func(e event.TypedCreateEvent[*certificatesv1.CertificateSigningRequest]) bool {
				return isRelevantCSR(e.Object)
			},
			UpdateFunc: func(e event.TypedUpdateEvent[*certificatesv1.CertificateSigningRequest]) bool {
				return isRelevantCSR(e.ObjectNew)
			},
			DeleteFunc: func(event.TypedDeleteEvent[*certificatesv1.CertificateSigningRequest]) bool {
				return false
			},
			GenericFunc: func(event.TypedGenericEvent[*certificatesv1.CertificateSigningRequest]) bool {
				return false
			},
		},
	)); err != nil {
		return fmt.Errorf("failed to watch CertificateSigningRequest: %v", err)
	}

	return nil
}

func (r *MachineApproverController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	if !credentialsReady() {
		log.Info("Cloud credentials not yet available, requeueing")
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	csr := &certificatesv1.CertificateSigningRequest{}
	if err := r.client.Get(ctx, req.NamespacedName, csr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get csr %s: %v", req.NamespacedName, err)
	}

	if !csr.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	log.Info("Reconciling CSR", "req", req)

	if !util.IsCertificateRequestPending(csr) {
		log.Info("CSR is already processed", "csr", csr.Name)
		return ctrl.Result{}, nil
	}

	// Only process CSRs that could be from Karpenter-provisioned nodes.
	// If no NodeClaims exist, this CSR is from a standard OpenShift node;
	// let the built-in machine-approver handle it.
	nodeClaimList := &karpenterv1.NodeClaimList{}
	if err := r.client.List(ctx, nodeClaimList); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list NodeClaims: %w", err)
	}
	if len(nodeClaimList.Items) == 0 {
		log.Info("No NodeClaims found, skipping CSR (not a Karpenter node)", "csr", csr.Name)
		return ctrl.Result{}, nil
	}

	authorized, err := r.CloudProvider.AuthorizeCSR(ctx, csr, r.client)
	if err != nil {
		return ctrl.Result{}, err
	}

	if authorized {
		log.Info("Attempting to approve CSR", "csr", csr.Name)
		if err := r.approve(ctx, csr); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to approve csr %s: %v", csr.Name, err)
		}
	}

	return ctrl.Result{}, nil
}

func credentialsReady() bool {
	path := os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func (r *MachineApproverController) approve(ctx context.Context, csr *certificatesv1.CertificateSigningRequest) error {
	csr.Status.Conditions = append(csr.Status.Conditions, certificatesv1.CertificateSigningRequestCondition{
		Type:    certificatesv1.CertificateApproved,
		Reason:  "KarpenterCSRApprove",
		Message: "Auto approved by karpenter_machine_approver",
		Status:  corev1.ConditionTrue,
	})

	_, err := r.certClient.CertificateSigningRequests().UpdateApproval(ctx, csr.Name, csr, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("error updating approval for csr: %v", err)
	}

	return nil
}
