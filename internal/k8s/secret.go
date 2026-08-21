package k8s

import (
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NoahHakansson/sk64/internal/natsort"
)

type secretResource struct {
	secret *corev1.Secret
}

// NewSecret wraps secret without copying it.
func NewSecret(secret *corev1.Secret) Resource {
	return &secretResource{secret: secret}
}

// NewEmptySecret returns an in-memory Secret with no keys, ready for creation.
func NewEmptySecret(namespace, name, secretType string) Resource {
	if secretType == "" {
		secretType = string(corev1.SecretTypeOpaque)
	}
	return NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretType(secretType),
		Data:       map[string][]byte{},
	})
}

func (r *secretResource) Clone() Resource         { return NewSecret(r.secret.DeepCopy()) }
func (r *secretResource) Kind() string            { return KindSecret }
func (r *secretResource) Name() string            { return r.secret.Name }
func (r *secretResource) Namespace() string       { return r.secret.Namespace }
func (r *secretResource) ResourceVersion() string { return r.secret.ResourceVersion }
func (r *secretResource) UID() string             { return string(r.secret.UID) }

func (r *secretResource) Type() string {
	if r.secret.Type == "" {
		return string(corev1.SecretTypeOpaque)
	}
	return string(r.secret.Type)
}

func (r *secretResource) Immutable() bool {
	return r.secret.Immutable != nil && *r.secret.Immutable
}

func (r *secretResource) Keys() []string {
	return slices.SortedFunc(maps.Keys(r.secret.Data), natsort.Compare)
}

func (r *secretResource) Get(key string) ([]byte, error) {
	value, ok := r.secret.Data[key]
	if !ok {
		return nil, fmt.Errorf("secret %q has no key %q", r.secret.Name, key)
	}
	return value, nil
}

func (r *secretResource) Set(key string, value []byte) error {
	if r.secret.Data == nil {
		r.secret.Data = make(map[string][]byte)
	}
	r.secret.Data[key] = value
	return nil
}

func (r *secretResource) Delete(key string) error {
	if _, ok := r.secret.Data[key]; !ok {
		return fmt.Errorf("secret %q has no key %q", r.secret.Name, key)
	}
	delete(r.secret.Data, key)
	return nil
}

func (r *secretResource) IsBinary(key string) bool {
	value, ok := r.secret.Data[key]
	return ok && IsBinaryValue(value)
}
