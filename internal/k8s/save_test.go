package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

var secretResourceGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

func TestDryRunSaveOK(t *testing.T) {
	client, clientset, resource := saveFixture(t)
	clientset.PrependReactor("update", "secrets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		update := action.(clienttesting.UpdateActionImpl)
		if len(update.UpdateOptions.DryRun) != 1 || update.UpdateOptions.DryRun[0] != metav1.DryRunAll {
			t.Fatalf("DryRun = %v, want [All]", update.UpdateOptions.DryRun)
		}
		secret := update.Object.(*corev1.Secret)
		if secret.StringData != nil || string(secret.Data["password"]) != "new" {
			t.Fatalf("sent data = %q, StringData = %v", secret.Data["password"], secret.StringData)
		}
		if secret.Labels["app"] != "demo" || secret.Annotations["note"] != "keep" || secret.Type != corev1.SecretTypeBasicAuth {
			t.Fatalf("metadata or type not preserved: %+v", secret)
		}
		return true, secret.DeepCopy(), nil
	})
	result := client.DryRunSave(t.Context(), resource)
	if result.Outcome != DryRunOK {
		t.Fatalf("DryRunSave() = %+v", result)
	}
	stored, err := clientset.CoreV1().Secrets("default").Get(t.Context(), "s1", metav1.GetOptions{})
	if err != nil || string(stored.Data["password"]) != "old" {
		t.Fatalf("tracker data = %q, err = %v", stored.Data["password"], err)
	}
}

func TestDryRunSaveRejected(t *testing.T) {
	client, clientset, resource := saveFixture(t)
	clientset.PrependReactor("update", "secrets", dryRunErrorReactor(apierrors.NewInvalid(
		schema.GroupKind{Kind: "Secret"}, "s1", field.ErrorList{field.Invalid(field.NewPath("data"), "bad", "denied")},
	)))
	result := client.DryRunSave(t.Context(), resource)
	if result.Outcome != DryRunRejected || result.Message == "" {
		t.Fatalf("DryRunSave() = %+v", result)
	}
}

func TestDryRunSaveUnsupported(t *testing.T) {
	client, clientset, resource := saveFixture(t)
	clientset.PrependReactor("update", "secrets", dryRunErrorReactor(apierrors.NewBadRequest(`admission webhook "x" does not support dry run`)))
	result := client.DryRunSave(t.Context(), resource)
	if result.Outcome != DryRunUnsupported {
		t.Fatalf("DryRunSave() = %+v", result)
	}
}

func TestDryRunSaveConflict(t *testing.T) {
	current := baseSecret()
	current.ResourceVersion = "11"
	current.Data["password"] = []byte("cluster")
	clientset := fake.NewClientset(current)
	client := &Client{Clientset: clientset, Context: "test-ctx", Namespace: "default"}
	edited := baseSecret()
	resource := NewSecret(edited)
	_ = resource.Set("password", []byte("new"))
	clientset.PrependReactor("update", "secrets", dryRunErrorReactor(apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "s1", errors.New("changed"))))
	result := client.DryRunSave(t.Context(), resource)
	if result.Outcome != DryRunConflict || result.Cluster == nil || result.Cluster.ResourceVersion() != "11" {
		t.Fatalf("DryRunSave() = %+v", result)
	}
}

func TestDryRunSaveTransportError(t *testing.T) {
	client, clientset, resource := saveFixture(t)
	clientset.PrependReactor("update", "secrets", dryRunErrorReactor(errors.New("boom")))
	if result := client.DryRunSave(t.Context(), resource); result.Outcome != DryRunFailed {
		t.Fatalf("DryRunSave() = %+v", result)
	}
}

func TestSaveSucceeds(t *testing.T) {
	client, clientset, resource := saveFixture(t)
	result := client.Save(t.Context(), resource)
	if result.Outcome != SaveSucceeded {
		t.Fatalf("Save() = %+v", result)
	}
	stored, err := clientset.CoreV1().Secrets("default").Get(t.Context(), "s1", metav1.GetOptions{})
	if err != nil || string(stored.Data["password"]) != "new" {
		t.Fatalf("tracker data = %q, err = %v", stored.Data["password"], err)
	}
}

func TestSaveDoesNotShortenAPIServerRequestBudget(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		if !ok {
			t.Fatal("save request has no deadline, want one bounding a hung connection")
		}
		if budget := time.Until(deadline); budget <= 60*time.Second {
			t.Fatalf("save request budget = %v, want more than the apiserver's own 60s budget", budget)
		}
		body := `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"s1","namespace":"default","resourceVersion":"11"},"data":{"password":"bmV3"}}`
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	clientset, err := kubernetes.NewForConfig(&rest.Config{Host: "https://unit.test", Transport: transport})
	if err != nil {
		t.Fatalf("NewForConfig() error = %v", err)
	}
	client := &Client{Clientset: clientset, Context: "test-ctx", Namespace: "default"}
	resource := NewSecret(baseSecret())
	if err := resource.Set("password", []byte("new")); err != nil {
		t.Fatal(err)
	}

	if result := client.Save(context.Background(), resource); result.Outcome != SaveSucceeded {
		t.Fatalf("Save() = %+v", result)
	}
}

func TestSaveVerificationGetsHaveAttemptDeadline(t *testing.T) {
	tests := []struct {
		name      string
		ambiguous bool
		run       func(*Client, Resource) error
	}{
		{
			name: "dry-run conflict",
			run: func(client *Client, resource Resource) error {
				result := client.DryRunSave(context.Background(), resource)
				if result.Outcome != DryRunConflict {
					return fmt.Errorf("DryRunSave() = %+v", result)
				}
				return nil
			},
		},
		{
			name: "save conflict",
			run: func(client *Client, resource Resource) error {
				result := client.Save(context.Background(), resource)
				if result.Outcome != SaveConflict {
					return fmt.Errorf("Save() = %+v", result)
				}
				return nil
			},
		},
		{
			name:      "ambiguous save",
			ambiguous: true,
			run: func(client *Client, resource Resource) error {
				result := client.Save(context.Background(), resource)
				if result.Outcome != SaveSucceeded {
					return fmt.Errorf("Save() = %+v", result)
				}
				return nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gets := 0
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.Method {
				case http.MethodPut:
					if test.ambiguous {
						return nil, timeoutError{}
					}
					return jsonResponse(request, http.StatusConflict, `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"changed","reason":"Conflict","details":{"name":"s1","kind":"secrets"},"code":409}`), nil
				case http.MethodGet:
					gets++
					deadline, ok := request.Context().Deadline()
					if !ok {
						t.Fatal("verification GET has no deadline")
					}
					if budget := time.Until(deadline); budget <= 0 || budget > attemptDeadline {
						t.Fatalf("verification GET budget = %v, want (0, %v]", budget, attemptDeadline)
					}
					return jsonResponse(request, http.StatusOK, `{"apiVersion":"v1","kind":"Secret","metadata":{"name":"s1","namespace":"default","resourceVersion":"11"},"data":{"password":"bmV3"}}`), nil
				default:
					t.Fatalf("unexpected request method %s", request.Method)
					return nil, nil
				}
			})
			clientset, err := kubernetes.NewForConfig(&rest.Config{Host: "https://unit.test", Transport: transport})
			if err != nil {
				t.Fatalf("NewForConfig() error = %v", err)
			}
			client := &Client{Clientset: clientset, Context: "test-ctx", Namespace: "default"}
			resource := NewSecret(baseSecret())
			if err := resource.Set("password", []byte("new")); err != nil {
				t.Fatal(err)
			}

			if err := test.run(client, resource); err != nil {
				t.Fatal(err)
			}
			if gets != 1 {
				t.Fatalf("verification GETs = %d, want 1", gets)
			}
		})
	}
}

func TestSaveConflict(t *testing.T) {
	withoutBackoff(t)
	current := baseSecret()
	current.ResourceVersion = "11"
	current.Data["password"] = []byte("cluster")
	clientset := fake.NewClientset(current)
	client := &Client{Clientset: clientset, Context: "test-ctx", Namespace: "default"}
	edited := baseSecret()
	resource := NewSecret(edited)
	_ = resource.Set("password", []byte("new"))
	updates := 0
	clientset.PrependReactor("update", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		updates++
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "s1", errors.New("changed"))
	})
	result := client.Save(t.Context(), resource)
	value, _ := result.Cluster.Get("password")
	if result.Outcome != SaveConflict || string(value) != "cluster" || updates != 1 {
		t.Fatalf("Save() = %+v, cluster value = %q, updates = %d", result, value, updates)
	}
}

func TestSaveForbidden(t *testing.T) {
	withoutBackoff(t)
	client, clientset, resource := saveFixture(t)
	updates := 0
	clientset.PrependReactor("update", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		updates++
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "s1", errors.New("denied"))
	})
	result := client.Save(t.Context(), resource)
	if result.Outcome != SaveForbidden || updates != 1 {
		t.Fatalf("Save() = %+v, updates = %d", result, updates)
	}
}

func TestSaveAmbiguousWriteLanded(t *testing.T) {
	withoutBackoff(t)
	client, clientset, resource := saveFixture(t)
	updates := 0
	clientset.PrependReactor("update", "secrets", func(action clienttesting.Action) (bool, runtime.Object, error) {
		updates++
		landed := action.(clienttesting.UpdateActionImpl).Object.(*corev1.Secret).DeepCopy()
		landed.ResourceVersion = "11"
		if err := clientset.Tracker().Update(secretResourceGVR, landed, "default"); err != nil {
			t.Fatal(err)
		}
		return true, nil, context.DeadlineExceeded
	})
	result := client.Save(t.Context(), resource)
	if result.Outcome != SaveSucceeded || updates != 1 {
		t.Fatalf("Save() = %+v, updates = %d", result, updates)
	}
}

func TestSaveAmbiguousRetryThenSuccess(t *testing.T) {
	withoutBackoff(t)
	client, clientset, resource := saveFixture(t)
	updates := 0
	clientset.PrependReactor("update", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, timeoutError{}
		}
		return false, nil, nil
	})
	result := client.Save(t.Context(), resource)
	if result.Outcome != SaveSucceeded || updates != 2 {
		t.Fatalf("Save() = %+v, updates = %d", result, updates)
	}
}

func TestSaveAmbiguousThenConflict(t *testing.T) {
	withoutBackoff(t)
	client, clientset, resource := saveFixture(t)
	updates := 0
	clientset.PrependReactor("update", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		updates++
		current := baseSecret()
		current.ResourceVersion = "11"
		current.Data["password"] = []byte("other")
		if err := clientset.Tracker().Update(secretResourceGVR, current, "default"); err != nil {
			t.Fatal(err)
		}
		return true, nil, timeoutError{}
	})
	result := client.Save(t.Context(), resource)
	value, _ := result.Cluster.Get("password")
	if result.Outcome != SaveConflict || string(value) != "other" || updates != 1 {
		t.Fatalf("Save() = %+v, value = %q, updates = %d", result, value, updates)
	}
}

func TestSaveAmbiguousExhausted(t *testing.T) {
	withoutBackoff(t)
	client, clientset, resource := saveFixture(t)
	clientset.PrependReactor("update", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, timeoutError{}
	})
	result := client.Save(t.Context(), resource)
	updates, gets := actionCounts(clientset.Actions())
	if result.Outcome != SaveFailed || updates != 3 || gets != 3 {
		t.Fatalf("Save() = %+v, updates = %d, gets = %d", result, updates, gets)
	}
}

func TestSavePreSendRetries(t *testing.T) {
	withoutBackoff(t)
	client, clientset, resource := saveFixture(t)
	updates := 0
	clientset.PrependReactor("update", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		updates++
		if updates == 1 {
			return true, nil, &net.DNSError{Err: "lookup failed", Name: "cluster"}
		}
		return false, nil, nil
	})
	result := client.Save(t.Context(), resource)
	_, gets := actionCounts(clientset.Actions())
	if result.Outcome != SaveSucceeded || updates != 2 || gets != 0 {
		t.Fatalf("Save() = %+v, updates = %d, gets = %d", result, updates, gets)
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		name               string
		err                error
		ambiguous, preSend bool
	}{
		{name: "deadline", err: context.DeadlineExceeded, ambiguous: true},
		{name: "network timeout", err: timeoutError{}, ambiguous: true},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, ambiguous: true},
		{name: "reset errno", err: fmt.Errorf("write: %w", syscall.ECONNRESET), ambiguous: true},
		{name: "connection reset", err: fmt.Errorf("write: %w", errors.New("connection reset by peer")), ambiguous: true},
		{name: "DNS", err: &net.DNSError{Err: "lookup", Name: "cluster"}, preSend: true},
		{name: "refused", err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED), preSend: true},
		{name: "invalid", err: apierrors.NewInvalid(schema.GroupKind{Kind: "Secret"}, "s1", nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isAmbiguousNetworkError(test.err); got != test.ambiguous {
				t.Fatalf("isAmbiguousNetworkError() = %t, want %t", got, test.ambiguous)
			}
			if got := isPreSendError(test.err); got != test.preSend {
				t.Fatalf("isPreSendError() = %t, want %t", got, test.preSend)
			}
		})
	}
}

func TestSaveRejectsUnknownResourceType(t *testing.T) {
	client := &Client{Clientset: fake.NewClientset(), Context: "test-ctx", Namespace: "default"}
	result := client.Save(t.Context(), stubResource{})
	if result.Outcome != SaveFailed || result.Message == "" {
		t.Fatalf("Save() = %+v", result)
	}
}

func TestSaveErrorBranches(t *testing.T) {
	t.Run("conflict fetch fails", func(t *testing.T) {
		client, clientset, resource := saveFixture(t)
		clientset.PrependReactor("update", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "s1", errors.New("changed"))
		})
		clientset.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("get failed")
		})
		if result := client.Save(t.Context(), resource); result.Outcome != SaveFailed || !strings.Contains(result.Message, "fetch current resource") {
			t.Fatalf("Save() = %+v", result)
		}
	})

	t.Run("ambiguous fetch fails", func(t *testing.T) {
		client, clientset, resource := saveFixture(t)
		clientset.PrependReactor("update", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, context.DeadlineExceeded
		})
		clientset.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("get failed")
		})
		if result := client.Save(t.Context(), resource); result.Outcome != SaveFailed || !strings.Contains(result.Message, "ambiguous") {
			t.Fatalf("Save() = %+v", result)
		}
	})

	t.Run("configmap update fails", func(t *testing.T) {
		configMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm1", Namespace: "default"}, Data: map[string]string{"key": "old"}}
		clientset := fake.NewClientset(configMap)
		client := &Client{Clientset: clientset, Context: "test-ctx", Namespace: "default"}
		resource := NewConfigMap(configMap.DeepCopy())
		if err := resource.Set("key", []byte("new")); err != nil {
			t.Fatal(err)
		}
		clientset.PrependReactor("update", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("update failed")
		})
		if result := client.Save(t.Context(), resource); result.Outcome != SaveFailed || !strings.Contains(result.Message, "update configmap") {
			t.Fatalf("Save() = %+v", result)
		}
	})
}

func baseSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "s1", Namespace: "default", ResourceVersion: "10",
			Labels: map[string]string{"app": "demo"}, Annotations: map[string]string{"note": "keep"},
		},
		Type: corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{"password": []byte("old")},
	}
}

func saveFixture(t *testing.T) (*Client, *fake.Clientset, Resource) {
	t.Helper()
	stored := baseSecret()
	clientset := fake.NewClientset(stored)
	client := &Client{Clientset: clientset, Context: "test-ctx", Namespace: "default"}
	edited := stored.DeepCopy()
	resource := NewSecret(edited)
	if err := resource.Set("password", []byte("new")); err != nil {
		t.Fatal(err)
	}
	return client, clientset, resource
}

func dryRunErrorReactor(err error) clienttesting.ReactionFunc {
	return func(action clienttesting.Action) (bool, runtime.Object, error) {
		update := action.(clienttesting.UpdateActionImpl)
		if len(update.UpdateOptions.DryRun) == 0 {
			return false, nil, nil
		}
		return true, nil, err
	}
}

func withoutBackoff(t *testing.T) {
	t.Helper()
	original := retryBackoff
	retryBackoff = func(context.Context, int) {}
	t.Cleanup(func() { retryBackoff = original })
}

func actionCounts(actions []clienttesting.Action) (updates, gets int) {
	for _, action := range actions {
		switch action.GetVerb() {
		case "update":
			updates++
		case "get":
			gets++
		}
	}
	return updates, gets
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonResponse(request *http.Request, statusCode int, body string) *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: statusCode,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

type stubResource struct{}

func (stubResource) Clone() Resource            { return stubResource{} }
func (stubResource) Kind() string               { return "Stub" }
func (stubResource) Name() string               { return "stub" }
func (stubResource) Namespace() string          { return "default" }
func (stubResource) Type() string               { return "" }
func (stubResource) ResourceVersion() string    { return "1" }
func (stubResource) UID() string                { return "" }
func (stubResource) Immutable() bool            { return false }
func (stubResource) Keys() []string             { return nil }
func (stubResource) Get(string) ([]byte, error) { return nil, errors.New("missing") }
func (stubResource) Set(string, []byte) error   { return nil }
func (stubResource) Delete(string) error        { return nil }
func (stubResource) IsBinary(string) bool       { return false }
func (stubResource) Validate() []Warning        { return nil }
