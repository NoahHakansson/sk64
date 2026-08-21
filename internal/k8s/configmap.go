package k8s

import (
	"fmt"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/NoahHakansson/sk64/internal/natsort"
)

type configMapResource struct {
	configMap *corev1.ConfigMap
}

// NewConfigMap wraps configMap without copying it.
func NewConfigMap(configMap *corev1.ConfigMap) Resource {
	return &configMapResource{configMap: configMap}
}

// NewEmptyConfigMap returns an in-memory ConfigMap with no keys.
func NewEmptyConfigMap(namespace, name string) Resource {
	return NewConfigMap(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       map[string]string{},
	})
}

func (r *configMapResource) Clone() Resource         { return NewConfigMap(r.configMap.DeepCopy()) }
func (r *configMapResource) Kind() string            { return KindConfigMap }
func (r *configMapResource) Name() string            { return r.configMap.Name }
func (r *configMapResource) Namespace() string       { return r.configMap.Namespace }
func (r *configMapResource) Type() string            { return "" }
func (r *configMapResource) ResourceVersion() string { return r.configMap.ResourceVersion }
func (r *configMapResource) UID() string             { return string(r.configMap.UID) }

func (r *configMapResource) Immutable() bool {
	return r.configMap.Immutable != nil && *r.configMap.Immutable
}

func (r *configMapResource) Keys() []string {
	keys := slices.Collect(maps.Keys(r.configMap.Data))
	keys = append(keys, slices.Collect(maps.Keys(r.configMap.BinaryData))...)
	slices.SortFunc(keys, natsort.Compare)
	return slices.Compact(keys)
}

func (r *configMapResource) Get(key string) ([]byte, error) {
	if value, ok := r.configMap.Data[key]; ok {
		return []byte(value), nil
	}
	if value, ok := r.configMap.BinaryData[key]; ok {
		return value, nil
	}
	return nil, fmt.Errorf("configmap %q has no key %q", r.configMap.Name, key)
}

func (r *configMapResource) Set(key string, value []byte) error {
	if IsBinaryValue(value) {
		if r.configMap.BinaryData == nil {
			r.configMap.BinaryData = make(map[string][]byte)
		}
		r.configMap.BinaryData[key] = value
		delete(r.configMap.Data, key)
		return nil
	}

	if r.configMap.Data == nil {
		r.configMap.Data = make(map[string]string)
	}
	r.configMap.Data[key] = string(value)
	delete(r.configMap.BinaryData, key)
	return nil
}

func (r *configMapResource) Delete(key string) error {
	_, inData := r.configMap.Data[key]
	_, inBinaryData := r.configMap.BinaryData[key]
	if !inData && !inBinaryData {
		return fmt.Errorf("configmap %q has no key %q", r.configMap.Name, key)
	}
	delete(r.configMap.Data, key)
	delete(r.configMap.BinaryData, key)
	return nil
}

func (r *configMapResource) IsBinary(key string) bool {
	value, err := r.Get(key)
	return err == nil && IsBinaryValue(value)
}
