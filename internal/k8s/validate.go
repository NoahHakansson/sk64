package k8s

import (
	"crypto/tls"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

func (r *secretResource) Validate() []Warning {
	switch corev1.SecretType(r.Type()) {
	case corev1.SecretTypeTLS:
		return r.validateTLS()
	case corev1.SecretTypeDockerConfigJson:
		return r.validateDockerConfigJSON()
	case corev1.SecretTypeBasicAuth:
		return r.validateRequiredKeys(corev1.BasicAuthUsernameKey, corev1.BasicAuthPasswordKey)
	case corev1.SecretTypeSSHAuth:
		return r.validateRequiredKeys(corev1.SSHAuthPrivateKey)
	case corev1.SecretTypeServiceAccountToken:
		return []Warning{"kubernetes.io/service-account-token secrets are controller-managed — edits may break the token or be overwritten"}
	default:
		return nil
	}
}

func (r *secretResource) validateTLS() []Warning {
	warnings := r.validateRequiredKeys(corev1.TLSCertKey, corev1.TLSPrivateKeyKey)
	if len(warnings) != 0 {
		return warnings
	}
	if _, err := tls.X509KeyPair(r.secret.Data[corev1.TLSCertKey], r.secret.Data[corev1.TLSPrivateKeyKey]); err != nil {
		return []Warning{Warning("tls.crt/tls.key pair invalid: " + err.Error())}
	}
	return nil
}

func (r *secretResource) validateDockerConfigJSON() []Warning {
	value, ok := r.secret.Data[corev1.DockerConfigJsonKey]
	if !ok {
		return []Warning{Warning(fmt.Sprintf("%s requires key %q", corev1.SecretTypeDockerConfigJson, corev1.DockerConfigJsonKey))}
	}
	var config struct {
		Auths map[string]json.RawMessage `json:"auths"`
	}
	if err := json.Unmarshal(value, &config); err != nil {
		return []Warning{Warning(".dockerconfigjson is not valid JSON: " + err.Error())}
	}
	if config.Auths == nil {
		return []Warning{".dockerconfigjson is missing \"auths\" object"}
	}
	return nil
}

func (r *secretResource) validateRequiredKeys(keys ...string) []Warning {
	warnings := make([]Warning, 0, len(keys))
	for _, key := range keys {
		if _, ok := r.secret.Data[key]; !ok {
			warnings = append(warnings, Warning(fmt.Sprintf("%s requires key %q", r.Type(), key)))
		}
	}
	return warnings
}

func (*configMapResource) Validate() []Warning { return nil }
