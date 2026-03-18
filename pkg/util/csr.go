package util

import (
	"crypto/x509"
	"encoding/pem"
	"errors"

	certificatesv1 "k8s.io/api/certificates/v1"
	corev1 "k8s.io/api/core/v1"
)

// Copied from k8s core (cannot be imported directly):
// https://github.com/kubernetes/kubernetes/blob/ec5096fa869b801d6eb1bf019819287ca61edc4d/pkg/apis/certificates/v1/helpers.go#L25-L37

// ParseCSR decodes a PEM encoded CSR.
func ParseCSR(pemBytes []byte) (*x509.CertificateRequest, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("PEM block type must be CERTIFICATE REQUEST")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	return csr, nil
}

// Copied from k8s core:
// https://github.com/kubernetes/kubernetes/blob/ec5096fa869b801d6eb1bf019819287ca61edc4d/pkg/controller/certificates/certificate_controller_utils.go#L24-L51

// IsCertificateRequestPending returns true if a certificate request has no "Approved" or "Denied" conditions.
func IsCertificateRequestPending(csr *certificatesv1.CertificateSigningRequest) bool {
	approved, denied := getCertApprovalCondition(&csr.Status)
	return !approved && !denied
}

func getCertApprovalCondition(status *certificatesv1.CertificateSigningRequestStatus) (approved bool, denied bool) {
	for _, c := range status.Conditions {
		if c.Type == certificatesv1.CertificateApproved {
			approved = true
		}
		if c.Type == certificatesv1.CertificateDenied {
			denied = true
		}
	}
	return
}

// GetCertApprovalCondition returns the approval/denial status of a CSR.
func GetCertApprovalCondition(status *certificatesv1.CertificateSigningRequestStatus) (approved bool, denied bool) {
	return getCertApprovalCondition(status)
}

// IsCertificateRequestApproved returns true if a certificate request has the "Approved" condition and no "Denied" conditions.
func IsCertificateRequestApproved(csr *certificatesv1.CertificateSigningRequest) bool {
	approved, denied := getCertApprovalCondition(&csr.Status)
	return approved && !denied
}

// HasTrueCondition returns true if the csr contains a condition of the specified type with a status that is set to True or is empty.
func HasTrueCondition(csr *certificatesv1.CertificateSigningRequest, conditionType certificatesv1.RequestConditionType) bool {
	for _, c := range csr.Status.Conditions {
		if c.Type == conditionType && (len(c.Status) == 0 || c.Status == corev1.ConditionTrue) {
			return true
		}
	}
	return false
}
