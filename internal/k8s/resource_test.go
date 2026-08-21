package k8s

import (
	"bytes"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestIsBinaryValue(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "empty", data: nil},
		{name: "allowed whitespace", data: []byte("hello\nworld\twith\rCR")},
		{name: "multibyte UTF-8", data: []byte("héllo 日本語")},
		{name: "UTF-8 BOM", data: append([]byte{0xef, 0xbb, 0xbf}, []byte("hello")...)},
		{name: "NUL", data: []byte{0}, want: true},
		{name: "SOH", data: []byte{1}, want: true},
		{name: "DEL", data: []byte{0x7f}, want: true},
		{name: "ESC", data: []byte{0x1b}, want: true},
		{name: "invalid UTF-8", data: []byte{0xff, 0xfe}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsBinaryValue(test.data); got != test.want {
				t.Fatalf("IsBinaryValue(%v) = %v, want %v", test.data, got, test.want)
			}
		})
	}
}

func TestSecretResource(t *testing.T) {
	immutable := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "credentials",
			Namespace:       "production",
			ResourceVersion: "42",
			UID:             types.UID("uid-1"),
		},
		Data: map[string][]byte{
			"z-text":   []byte("hello"),
			"a-binary": {0},
		},
	}
	resource := NewSecret(secret)

	if resource.Kind() != KindSecret || resource.Name() != "credentials" || resource.Namespace() != "production" {
		t.Fatalf("identity accessors = %s/%s/%s", resource.Kind(), resource.Namespace(), resource.Name())
	}
	if resource.ResourceVersion() != "42" || resource.UID() != "uid-1" {
		t.Fatalf("metadata accessors = rv %q uid %q", resource.ResourceVersion(), resource.UID())
	}
	if resource.Type() != "Opaque" {
		t.Fatalf("Type() = %q, want Opaque", resource.Type())
	}
	secret.Type = corev1.SecretTypeTLS
	if resource.Type() != string(corev1.SecretTypeTLS) {
		t.Fatalf("Type() = %q, want %q", resource.Type(), corev1.SecretTypeTLS)
	}
	if resource.Immutable() {
		t.Fatal("Immutable() = true with nil field")
	}
	secret.Immutable = &immutable
	if !resource.Immutable() {
		t.Fatal("Immutable() = false, want true")
	}
	if got, want := resource.Keys(), []string{"a-binary", "z-text"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	value, err := resource.Get("z-text")
	if err != nil || !bytes.Equal(value, []byte("hello")) {
		t.Fatalf("Get(z-text) = %q, %v", value, err)
	}
	if _, err := resource.Get("missing"); err == nil {
		t.Fatal("Get(missing) error = nil")
	}
	if !resource.IsBinary("a-binary") || resource.IsBinary("z-text") || resource.IsBinary("missing") {
		t.Fatal("IsBinary returned unexpected result")
	}
	clone := resource.Clone()
	if err := resource.Set("z-text", []byte("updated")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if !bytes.Equal(secret.Data["z-text"], []byte("updated")) {
		t.Fatalf("Set() data = %q", secret.Data["z-text"])
	}
	clonedValue, err := clone.Get("z-text")
	if err != nil || !bytes.Equal(clonedValue, []byte("hello")) {
		t.Fatalf("Clone().Get(z-text) = %q, %v", clonedValue, err)
	}
	if err := resource.Delete("a-binary"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok := secret.Data["a-binary"]; ok {
		t.Fatal("Delete() left key in Data")
	}
	if err := resource.Delete("missing"); err == nil {
		t.Fatal("Delete(missing) error = nil")
	}
}

func TestConfigMapResource(t *testing.T) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "default"},
		Data: map[string]string{
			"z-text": "hello",
			"shared": "text wins",
		},
		BinaryData: map[string][]byte{
			"a-binary": {0xff},
			"shared":   []byte("binary twin"),
		},
	}
	resource := NewConfigMap(configMap)

	if resource.Type() != "" {
		t.Fatalf("Type() = %q, want empty", resource.Type())
	}
	if got, want := resource.Keys(), []string{"a-binary", "shared", "z-text"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	text, err := resource.Get("z-text")
	if err != nil || !bytes.Equal(text, []byte("hello")) {
		t.Fatalf("Get(z-text) = %q, %v", text, err)
	}
	binary, err := resource.Get("a-binary")
	if err != nil || !bytes.Equal(binary, []byte{0xff}) {
		t.Fatalf("Get(a-binary) = %v, %v", binary, err)
	}
	shared, err := resource.Get("shared")
	if err != nil || string(shared) != "text wins" {
		t.Fatalf("Get(shared) = %q, %v", shared, err)
	}
	if err := resource.Set("route", []byte("text")); err != nil {
		t.Fatalf("Set(text) error = %v", err)
	}
	clone := resource.Clone()
	configMap.BinaryData["route"] = []byte{0}
	if err := resource.Set("route", []byte("new text")); err != nil {
		t.Fatalf("Set(text overwrite) error = %v", err)
	}
	if configMap.Data["route"] != "new text" {
		t.Fatalf("text route = %q", configMap.Data["route"])
	}
	if _, ok := configMap.BinaryData["route"]; ok {
		t.Fatal("text Set left BinaryData twin")
	}
	clonedValue, err := clone.Get("route")
	if err != nil || !bytes.Equal(clonedValue, []byte("text")) {
		t.Fatalf("Clone().Get(route) = %q, %v", clonedValue, err)
	}
	if err := resource.Set("route", []byte{0}); err != nil {
		t.Fatalf("Set(binary) error = %v", err)
	}
	if _, ok := configMap.Data["route"]; ok {
		t.Fatal("binary Set left Data twin")
	}
	if !bytes.Equal(configMap.BinaryData["route"], []byte{0}) {
		t.Fatalf("binary route = %v", configMap.BinaryData["route"])
	}
	if err := resource.Delete("z-text"); err != nil {
		t.Fatalf("Delete(Data) error = %v", err)
	}
	if err := resource.Delete("a-binary"); err != nil {
		t.Fatalf("Delete(BinaryData) error = %v", err)
	}
	if err := resource.Delete("missing"); err == nil {
		t.Fatal("Delete(missing) error = nil")
	}
}

func TestKeysOrderNumberedKeysNaturally(t *testing.T) {
	names := []string{"URL_0", "URL_1", "URL_10", "URL_11", "URL_2", "URL_9", "other"}
	want := []string{"URL_0", "URL_1", "URL_2", "URL_9", "URL_10", "URL_11", "other"}

	secretData := map[string][]byte{}
	configMapData := map[string]string{}
	for _, name := range names {
		secretData[name] = []byte("v")
		configMapData[name] = "v"
	}

	secret := NewSecret(&corev1.Secret{Data: secretData})
	if got := secret.Keys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Secret Keys() = %v, want %v", got, want)
	}
	configMap := NewConfigMap(&corev1.ConfigMap{Data: configMapData})
	if got := configMap.Keys(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfigMap Keys() = %v, want %v", got, want)
	}
}
