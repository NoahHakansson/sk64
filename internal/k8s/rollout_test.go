package k8s

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestRestartWorkloadPatch(t *testing.T) {
	fixed := time.Date(2026, 7, 22, 12, 34, 56, 0, time.UTC)
	originalNow := rolloutNow
	rolloutNow = func() time.Time { return fixed }
	t.Cleanup(func() { rolloutNow = originalNow })
	tests := []struct {
		kind, resource string
		object         runtime.Object
	}{
		{KindDeployment, "deployments", &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}}},
		{KindStatefulSet, "statefulsets", &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}}},
		{KindDaemonSet, "daemonsets", &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}}},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			clientset := fake.NewClientset(test.object)
			var captured k8stesting.PatchActionImpl
			clientset.PrependReactor("patch", test.resource, func(action k8stesting.Action) (bool, runtime.Object, error) {
				captured = action.(k8stesting.PatchActionImpl)
				return true, test.object, nil
			})
			client := &Client{Clientset: clientset}
			if err := client.RestartWorkload(t.Context(), test.kind, "ns", "target"); err != nil {
				t.Fatal(err)
			}
			if captured.GetPatchType() != types.StrategicMergePatchType || captured.GetName() != "target" || captured.GetNamespace() != "ns" {
				t.Fatalf("patch action = %#v", captured)
			}
			var patch struct {
				Spec struct {
					Template struct {
						Metadata struct {
							Annotations map[string]string `json:"annotations"`
						} `json:"metadata"`
					} `json:"template"`
				} `json:"spec"`
			}
			if err := json.Unmarshal(captured.GetPatch(), &patch); err != nil {
				t.Fatal(err)
			}
			if got := patch.Spec.Template.Metadata.Annotations[RestartedAtAnnotation]; got != fixed.Format(time.RFC3339) {
				t.Fatalf("restartedAt = %q", got)
			}
		})
	}
}

func TestRestartWorkloadRejectsKind(t *testing.T) {
	clientset := fake.NewClientset()
	client := &Client{Clientset: clientset}
	tests := []struct {
		kind        string
		restartable bool
	}{
		{KindDeployment, true},
		{KindStatefulSet, true},
		{KindDaemonSet, true},
		{KindJob, false},
		{KindCronJob, false},
		{"Widget", false},
	}
	for _, test := range tests {
		if got := RestartableKind(test.kind); got != test.restartable {
			t.Fatalf("RestartableKind(%q) = %t", test.kind, got)
		}
		if !test.restartable {
			if err := client.RestartWorkload(t.Context(), test.kind, "ns", "target"); err == nil {
				t.Fatalf("RestartWorkload(%q) returned nil", test.kind)
			}
		}
	}
	if len(clientset.Actions()) != 0 {
		t.Fatalf("unexpected patch actions = %#v", clientset.Actions())
	}
}

func TestRestartWorkloadForbidden(t *testing.T) {
	clientset := fake.NewClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}})
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "target", errors.New("denied"))
	clientset.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden
	})
	client := &Client{Clientset: clientset}
	if err := client.RestartWorkload(t.Context(), KindDeployment, "ns", "target"); !apierrors.IsForbidden(err) {
		t.Fatalf("error = %v", err)
	}
}
