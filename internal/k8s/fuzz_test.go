package k8s

import (
	"bytes"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func FuzzIsBinaryValue(f *testing.F) {
	for _, seed := range [][]byte{nil, []byte("plain ASCII"), []byte("\n\t\r"), []byte("\xef\xbb\xbf"), {0}, {0xff}, {0x7f}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if IsBinaryValue(data) {
			return
		}
		configMap := &corev1.ConfigMap{}
		resource := NewConfigMap(configMap)
		if err := resource.Set("value", data); err != nil {
			t.Fatalf("ConfigMap.Set() error = %v", err)
		}
		stored, ok := configMap.Data["value"]
		if !ok || !bytes.Equal([]byte(stored), data) {
			t.Fatalf("ConfigMap Data value = %q, present %t, want %q", stored, ok, data)
		}
	})
}
