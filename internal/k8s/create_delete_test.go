package k8s

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestCreateSecretAndConfigMap(t *testing.T) {
	tests := []struct {
		name     string
		res      Resource
		get      func(*fake.Clientset) ([]byte, string, error)
		wantType string
	}{
		{
			name:     "secret",
			res:      NewEmptySecret("ns", "s", string(corev1.SecretTypeTLS)),
			wantType: string(corev1.SecretTypeTLS),
			get: func(clientset *fake.Clientset) ([]byte, string, error) {
				stored, err := clientset.CoreV1().Secrets("ns").Get(t.Context(), "s", metav1.GetOptions{})
				if err != nil {
					return nil, "", err
				}
				return stored.Data["key"], string(stored.Type), nil
			},
		},
		{
			name: "configmap",
			res:  NewEmptyConfigMap("ns", "cm"),
			get: func(clientset *fake.Clientset) ([]byte, string, error) {
				stored, err := clientset.CoreV1().ConfigMaps("ns").Get(t.Context(), "cm", metav1.GetOptions{})
				if err != nil {
					return nil, "", err
				}
				return []byte(stored.Data["key"]), "", nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.res.Set("key", []byte("value")); err != nil {
				t.Fatal(err)
			}
			clientset := fake.NewClientset()
			client := &Client{Clientset: clientset, Namespace: "ns"}
			result := client.Create(t.Context(), test.res)
			if result.Outcome != SaveSucceeded {
				t.Fatalf("Create() = %+v", result)
			}
			value, resourceType, err := test.get(clientset)
			if err != nil || string(value) != "value" {
				t.Fatalf("stored value = %q, type = %q, err = %v", value, resourceType, err)
			}
			if resourceType != test.wantType {
				t.Fatalf("stored type = %q, want %q", resourceType, test.wantType)
			}
		})
	}
}

func TestCreateAlreadyExists(t *testing.T) {
	clientset := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}})
	client := &Client{Clientset: clientset}
	result := client.Create(t.Context(), NewEmptySecret("ns", "s", ""))
	if result.Outcome != SaveFailed || !strings.Contains(result.Message, "already exists") {
		t.Fatalf("Create() = %+v", result)
	}
}

func TestCreateForbidden(t *testing.T) {
	clientset := fake.NewClientset()
	clientset.PrependReactor("create", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "s", errors.New("denied"))
	})
	result := (&Client{Clientset: clientset}).Create(t.Context(), NewEmptySecret("ns", "s", ""))
	if result.Outcome != SaveForbidden || !strings.Contains(result.Message, "denied") {
		t.Fatalf("Create() = %+v", result)
	}
}

func TestCreateAmbiguousNetwork(t *testing.T) {
	tests := []struct {
		name        string
		seed        *corev1.Secret
		wantOutcome SaveOutcome
		wantMessage string
	}{
		{name: "matching object", seed: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"key": []byte("value")}}, wantOutcome: SaveSucceeded},
		{name: "different object", seed: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"key": []byte("different")}}, wantOutcome: SaveFailed},
		{name: "not found", wantOutcome: SaveFailed, wantMessage: "refresh and retry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := []runtime.Object{}
			if test.seed != nil {
				objects = append(objects, test.seed)
			}
			clientset := fake.NewClientset(objects...)
			clientset.PrependReactor("create", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, context.DeadlineExceeded
			})
			resource := NewEmptySecret("ns", "s", "")
			_ = resource.Set("key", []byte("value"))
			result := (&Client{Clientset: clientset}).Create(t.Context(), resource)
			if result.Outcome != test.wantOutcome || test.wantMessage != "" && !strings.Contains(result.Message, test.wantMessage) {
				t.Fatalf("Create() = %+v", result)
			}
		})
	}
}

func TestDryRunCreate(t *testing.T) {
	tests := []struct {
		name        string
		seed        bool
		err         error
		wantOutcome DryRunOutcome
		wantMessage string
	}{
		{name: "accepted", wantOutcome: DryRunOK},
		{name: "invalid", err: apierrors.NewInvalid(schema.GroupKind{Kind: "Secret"}, "s", field.ErrorList{field.Invalid(field.NewPath("data"), "bad", "denied")}), wantOutcome: DryRunRejected},
		{name: "unsupported", err: apierrors.NewBadRequest("webhook does not support dry run"), wantOutcome: DryRunUnsupported},
		{name: "already exists", seed: true, wantOutcome: DryRunRejected, wantMessage: "s"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var objects []runtime.Object
			if test.seed {
				objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s", Namespace: "ns"}})
			}
			clientset := fake.NewClientset(objects...)
			if test.err != nil {
				clientset.PrependReactor("create", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, test.err
				})
			}
			result := (&Client{Clientset: clientset}).DryRunCreate(t.Context(), NewEmptySecret("ns", "s", ""))
			if result.Outcome != test.wantOutcome || test.wantMessage != "" && !strings.Contains(result.Message, test.wantMessage) {
				t.Fatalf("DryRunCreate() = %+v", result)
			}
		})
	}
}

func TestDeleteSendsPreconditions(t *testing.T) {
	configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm", Namespace: "ns", UID: "uid-1", ResourceVersion: "12"}}
	clientset := fake.NewClientset(configMap)
	clientset.PrependReactor("delete", "configmaps", func(action clienttesting.Action) (bool, runtime.Object, error) {
		deleteAction := action.(clienttesting.DeleteActionImpl)
		preconditions := deleteAction.DeleteOptions.Preconditions
		if preconditions == nil || preconditions.UID == nil || *preconditions.UID != "uid-1" || preconditions.ResourceVersion == nil || *preconditions.ResourceVersion != "12" {
			t.Fatalf("delete preconditions = %+v", preconditions)
		}
		if err := clientset.Tracker().Delete(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, "ns", "cm"); err != nil {
			t.Fatal(err)
		}
		return true, nil, nil
	})
	result := (&Client{Clientset: clientset}).DeleteResource(t.Context(), NewConfigMap(configMap))
	if result.Outcome != DeleteSucceeded {
		t.Fatalf("DeleteResource() = %+v", result)
	}
	if _, err := clientset.CoreV1().ConfigMaps("ns").Get(t.Context(), "cm", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted object Get() error = %v", err)
	}
}

func TestDeleteOutcomes(t *testing.T) {
	tests := []struct {
		name        string
		res         Resource
		err         error
		wantOutcome DeleteOutcome
		wantMessage string
	}{
		{name: "conflict", res: NewEmptySecret("ns", "s", ""), err: apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "s", errors.New("changed")), wantOutcome: DeleteConflict},
		{name: "forbidden", res: NewEmptySecret("ns", "s", ""), err: apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "s", errors.New("denied")), wantOutcome: DeleteForbidden},
		{name: "already deleted", res: NewEmptySecret("ns", "s", ""), wantOutcome: DeleteSucceeded, wantMessage: "already deleted"},
		{name: "unknown kind", res: stubResource{}, wantOutcome: DeleteFailed},
		{name: "generic failure", res: NewEmptySecret("ns", "s", ""), err: errors.New("boom"), wantOutcome: DeleteFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clientset := fake.NewClientset()
			if test.err != nil {
				clientset.PrependReactor("delete", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, test.err
				})
			}
			result := (&Client{Clientset: clientset}).DeleteResource(t.Context(), test.res)
			if result.Outcome != test.wantOutcome || test.wantMessage != "" && !strings.Contains(result.Message, test.wantMessage) {
				t.Fatalf("DeleteResource() = %+v", result)
			}
		})
	}
}

func TestNewEmptyResources(t *testing.T) {
	secret := NewEmptySecret("ns", "s", "")
	if secret.Kind() != KindSecret || secret.Namespace() != "ns" || secret.Name() != "s" || secret.Type() != string(corev1.SecretTypeOpaque) || len(secret.Keys()) != 0 {
		t.Fatalf("NewEmptySecret() = kind=%q ns=%q name=%q type=%q keys=%v", secret.Kind(), secret.Namespace(), secret.Name(), secret.Type(), secret.Keys())
	}
	tlsSecret := NewEmptySecret("ns", "tls", string(corev1.SecretTypeTLS))
	if warnings := tlsSecret.Validate(); len(warnings) != 2 {
		t.Fatalf("TLS warnings = %v", warnings)
	}
	configMap := NewEmptyConfigMap("ns", "cm")
	if configMap.Kind() != KindConfigMap || configMap.Namespace() != "ns" || configMap.Name() != "cm" || len(configMap.Keys()) != 0 {
		t.Fatalf("NewEmptyConfigMap() = kind=%q ns=%q name=%q keys=%v", configMap.Kind(), configMap.Namespace(), configMap.Name(), configMap.Keys())
	}
}

func TestWellKnownSecretTypes(t *testing.T) {
	want := []string{
		string(corev1.SecretTypeOpaque),
		string(corev1.SecretTypeTLS),
		string(corev1.SecretTypeDockerConfigJson),
		string(corev1.SecretTypeBasicAuth),
		string(corev1.SecretTypeSSHAuth),
	}
	if got := WellKnownSecretTypes(); !slices.Equal(got, want) || slices.Contains(got, string(corev1.SecretTypeServiceAccountToken)) {
		t.Fatalf("WellKnownSecretTypes() = %v, want %v", got, want)
	}
}
