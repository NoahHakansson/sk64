package k8s

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestValidateTLS(t *testing.T) {
	certA, keyA := tlsMaterial(t)
	_, keyB := tlsMaterial(t)
	tests := []struct {
		name string
		data map[string][]byte
		want int
		text string
	}{
		{name: "valid", data: map[string][]byte{corev1.TLSCertKey: certA, corev1.TLSPrivateKeyKey: keyA}},
		{name: "missing key", data: map[string][]byte{corev1.TLSCertKey: certA}, want: 1, text: "tls.key"},
		{name: "missing both", data: map[string][]byte{}, want: 2, text: "tls.crt"},
		{name: "mismatched pair", data: map[string][]byte{corev1.TLSCertKey: certA, corev1.TLSPrivateKeyKey: keyB}, want: 1, text: "pair invalid"},
		{name: "garbage", data: map[string][]byte{corev1.TLSCertKey: []byte("garbage"), corev1.TLSPrivateKeyKey: []byte("garbage")}, want: 1, text: "pair invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			warnings := NewSecret(&corev1.Secret{Type: corev1.SecretTypeTLS, Data: test.data}).Validate()
			if len(warnings) != test.want {
				t.Fatalf("Validate() = %v, want %d warnings", warnings, test.want)
			}
			if test.text != "" && !strings.Contains(string(warnings[0]), test.text) {
				t.Fatalf("Validate() = %v, want text %q", warnings, test.text)
			}
		})
	}
}

func TestValidateDockerConfigJSON(t *testing.T) {
	tests := []struct {
		name string
		data map[string][]byte
		want string
	}{
		{name: "valid", data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{"r":{}}}`)}},
		{name: "missing key", data: map[string][]byte{}, want: corev1.DockerConfigJsonKey},
		{name: "invalid JSON", data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{`)}, want: "not valid JSON"},
		{name: "missing auths", data: map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{}`)}, want: `missing "auths"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			warnings := NewSecret(&corev1.Secret{Type: corev1.SecretTypeDockerConfigJson, Data: test.data}).Validate()
			if test.want == "" && len(warnings) != 0 {
				t.Fatalf("Validate() = %v, want nil", warnings)
			}
			if test.want != "" && (len(warnings) != 1 || !strings.Contains(string(warnings[0]), test.want)) {
				t.Fatalf("Validate() = %v, want %q", warnings, test.want)
			}
		})
	}
}

func TestValidateBasicAuth(t *testing.T) {
	tests := []struct {
		name string
		data map[string][]byte
		want string
	}{
		{name: "valid", data: map[string][]byte{corev1.BasicAuthUsernameKey: {}, corev1.BasicAuthPasswordKey: {}}},
		{name: "missing password", data: map[string][]byte{corev1.BasicAuthUsernameKey: {}}, want: corev1.BasicAuthPasswordKey},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			warnings := NewSecret(&corev1.Secret{Type: corev1.SecretTypeBasicAuth, Data: test.data}).Validate()
			if test.want == "" && len(warnings) != 0 || test.want != "" && (len(warnings) != 1 || !strings.Contains(string(warnings[0]), test.want)) {
				t.Fatalf("Validate() = %v, want %q", warnings, test.want)
			}
		})
	}
}

func TestValidateSSHAuth(t *testing.T) {
	tests := []struct {
		name string
		data map[string][]byte
		want bool
	}{
		{name: "present", data: map[string][]byte{corev1.SSHAuthPrivateKey: {}}},
		{name: "missing", data: map[string][]byte{}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			warnings := NewSecret(&corev1.Secret{Type: corev1.SecretTypeSSHAuth, Data: test.data}).Validate()
			if (len(warnings) != 0) != test.want {
				t.Fatalf("Validate() = %v", warnings)
			}
		})
	}
}

func TestValidateServiceAccountToken(t *testing.T) {
	warnings := NewSecret(&corev1.Secret{Type: corev1.SecretTypeServiceAccountToken}).Validate()
	if len(warnings) != 1 || !strings.Contains(string(warnings[0]), "controller-managed") {
		t.Fatalf("Validate() = %v", warnings)
	}
}

func TestValidateOpaqueAndConfigMap(t *testing.T) {
	tests := []Resource{
		NewSecret(&corev1.Secret{Type: corev1.SecretTypeOpaque}),
		NewConfigMap(&corev1.ConfigMap{}),
	}
	for _, resource := range tests {
		if warnings := resource.Validate(); len(warnings) != 0 {
			t.Fatalf("%s Validate() = %v, want nil", resource.Kind(), warnings)
		}
	}
}

func tlsMaterial(t *testing.T) ([]byte, []byte) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
}
