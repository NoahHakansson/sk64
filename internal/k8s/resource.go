package k8s

import "unicode/utf8"

const (
	// KindNamespace identifies Namespace resources.
	KindNamespace = "Namespace"
	// KindSecret identifies Secret resources.
	KindSecret = "Secret"
	// KindConfigMap identifies ConfigMap resources.
	KindConfigMap = "ConfigMap"
	// KindPod identifies Pod resources.
	KindPod = "Pod"
	// KindServiceAccount identifies ServiceAccount resources.
	KindServiceAccount = "ServiceAccount"
)

// Warning is a human-readable, non-blocking validation finding shown before
// a save with a save-anyway override.
type Warning string

// Resource provides common read access and in-memory mutation for Secrets and ConfigMaps.
type Resource interface {
	Clone() Resource
	Kind() string
	Name() string
	Namespace() string
	Type() string
	ResourceVersion() string
	UID() string
	Immutable() bool
	Keys() []string
	Get(key string) ([]byte, error)
	Set(key string, value []byte) error
	Delete(key string) error
	IsBinary(key string) bool
	Validate() []Warning
}

// IsBinaryValue reports whether data is invalid UTF-8 or contains disallowed control characters.
func IsBinaryValue(data []byte) bool {
	if !utf8.Valid(data) {
		return true
	}

	for _, r := range string(data) {
		if (r < 0x20 && r != '\n' && r != '\t' && r != '\r') || r == 0x7f {
			return true
		}
	}
	return false
}
