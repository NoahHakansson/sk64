package k8s

import (
	"context"
	"errors"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/metadata"
	metadatafake "k8s.io/client-go/metadata/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestExists(t *testing.T) {
	tests := []struct {
		kind      string
		namespace string
		object    runtime.Object
	}{
		{kind: KindNamespace, object: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "target"}}},
		{kind: KindDeployment, namespace: "ns", object: &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}}},
		{kind: KindStatefulSet, namespace: "ns", object: &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}}},
		{kind: KindDaemonSet, namespace: "ns", object: &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}}},
		{kind: KindJob, namespace: "ns", object: &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}}},
		{kind: KindCronJob, namespace: "ns", object: &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}}},
		{kind: KindSecret, namespace: "ns", object: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}}},
		{kind: KindConfigMap, namespace: "ns", object: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "ns"}}},
	}

	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			seeded := &Client{Clientset: fake.NewClientset(test.object)}
			found, err := seeded.Exists(t.Context(), test.kind, test.namespace, "target")
			if err != nil || !found {
				t.Fatalf("Exists(seeded) = %v, %v, want true, nil", found, err)
			}

			absent := &Client{Clientset: fake.NewClientset()}
			found, err = absent.Exists(t.Context(), test.kind, test.namespace, "target")
			if err != nil || found {
				t.Fatalf("Exists(absent) = %v, %v, want false, nil", found, err)
			}
		})
	}

	clientset := fake.NewClientset()
	client := &Client{Clientset: clientset}
	if _, err := client.Exists(t.Context(), "Unknown", "ns", "target"); err == nil {
		t.Fatal("Exists(unknown) error = nil")
	}
	apiErr := errors.New("API unavailable")
	clientset.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apiErr
	})
	if _, err := client.Exists(t.Context(), KindSecret, "ns", "target"); !errors.Is(err, apiErr) || err.Error() != `check Secret/target in namespace "ns": API unavailable` {
		t.Fatalf("Exists(namespaced API error) = %v", err)
	}
	clientset.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apiErr
	})
	if _, err := client.Exists(t.Context(), KindNamespace, "", "target"); !errors.Is(err, apiErr) || err.Error() != "check Namespace/target: API unavailable" {
		t.Fatalf("Exists(cluster-scoped API error) = %v", err)
	}
}

func TestMatchGeneratedNames(t *testing.T) {
	clientset := fake.NewClientset()
	clientset.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("typed Secret list fetched payloads")
	})
	client := &Client{
		Clientset: clientset,
		Metadata: newFakeMetadataClient(t,
			metadataObject("Secret", "ns", "app-config"),
			metadataObject("Secret", "ns", "app-config-abc123"),
			metadataObject("Secret", "ns", "app-configother"),
			metadataObject("ConfigMap", "ns", "settings-z"),
			metadataObject("ConfigMap", "ns", "settings"),
		),
	}

	got, err := client.MatchGeneratedNames(t.Context(), KindSecret, "ns", []string{"app-config", "settings", "missing"})
	if err != nil {
		t.Fatalf("MatchGeneratedNames(secret) error = %v", err)
	}
	want := map[string]string{"app-config": "app-config"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MatchGeneratedNames(secret) = %v, want %v", got, want)
	}

	got, err = client.MatchGeneratedNames(t.Context(), KindConfigMap, "ns", []string{"app-config", "settings", "missing"})
	want = map[string]string{"settings": "settings"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("MatchGeneratedNames(configmap) = %v, %v, want %v", got, err, want)
	}
	if _, err := client.MatchGeneratedNames(t.Context(), KindDeployment, "ns", nil); err == nil {
		t.Fatal("MatchGeneratedNames(unsupported) error = nil")
	}
	if len(clientset.Actions()) != 0 {
		t.Fatalf("MatchGeneratedNames() used typed client actions: %+v", clientset.Actions())
	}
	if _, err := (&Client{}).MatchGeneratedNames(t.Context(), KindSecret, "ns", nil); err == nil {
		t.Fatal("MatchGeneratedNames(missing metadata client) error = nil")
	}
}

func TestMatchesGeneratedName(t *testing.T) {
	tests := []struct {
		name, prefix, candidate string
		want                    bool
	}{
		{name: "exact", prefix: "app-config", candidate: "app-config", want: true},
		{name: "generated suffix", prefix: "app-config", candidate: "app-config-abc123", want: true},
		{name: "missing hyphen", prefix: "app-config", candidate: "app-configother"},
		{name: "unrelated", prefix: "app-config", candidate: "settings"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := MatchesGeneratedName(test.prefix, test.candidate); got != test.want {
				t.Fatalf("MatchesGeneratedName(%q, %q) = %t, want %t", test.prefix, test.candidate, got, test.want)
			}
		})
	}
}

func TestMatchGeneratedNamesPagination(t *testing.T) {
	metadataClient := newFakeMetadataClient(t)
	page := 0
	metadataClient.PrependReactor("list", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		page++
		if page == 1 {
			return true, &metav1.List{
				ListMeta: metav1.ListMeta{Continue: "next-prefix"},
				Items: []runtime.RawExtension{
					{Object: metadataObject("Secret", "ns", "app-config-z")},
				},
			}, nil
		}
		return true, &metav1.List{Items: []runtime.RawExtension{
			{Object: metadataObject("Secret", "ns", "app-config-a")},
			{Object: metadataObject("Secret", "ns", "unrelated")},
		}}, nil
	})
	recorder := &recordingMetadata{Interface: metadataClient}
	client := &Client{Metadata: recorder}

	got, err := client.MatchGeneratedNames(t.Context(), KindSecret, "ns", []string{"app-config", "missing"})
	if err != nil {
		t.Fatalf("MatchGeneratedNames() error = %v", err)
	}
	want := map[string]string{"app-config": "app-config-a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MatchGeneratedNames() = %v, want %v", got, want)
	}
	actions := metadataClient.Actions()
	if len(actions) != 2 {
		t.Fatalf("MatchGeneratedNames() actions = %d, want 2", len(actions))
	}
	if len(recorder.options) != 2 || recorder.options[0].Continue != "" || recorder.options[1].Continue != "next-prefix" {
		t.Fatalf("MatchGeneratedNames() options = %+v, want continue tokens \"\", \"next-prefix\"", recorder.options)
	}
}

type recordingMetadata struct {
	metadata.Interface
	options []metav1.ListOptions
}

func (r *recordingMetadata) Resource(resource schema.GroupVersionResource) metadata.Getter {
	return recordingMetadataGetter{Getter: r.Interface.Resource(resource), recorder: r}
}

type recordingMetadataGetter struct {
	metadata.Getter
	recorder *recordingMetadata
}

func (g recordingMetadataGetter) Namespace(namespace string) metadata.ResourceInterface {
	return recordingMetadataResource{ResourceInterface: g.Getter.Namespace(namespace), recorder: g.recorder}
}

func (g recordingMetadataGetter) List(ctx context.Context, options metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	g.recorder.options = append(g.recorder.options, options)
	return g.Getter.List(ctx, options)
}

type recordingMetadataResource struct {
	metadata.ResourceInterface
	recorder *recordingMetadata
}

func (r recordingMetadataResource) List(ctx context.Context, options metav1.ListOptions) (*metav1.PartialObjectMetadataList, error) {
	r.recorder.options = append(r.recorder.options, options)
	return r.ResourceInterface.List(ctx, options)
}

func newFakeMetadataClient(t *testing.T, objects ...runtime.Object) *metadatafake.FakeMetadataClient {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatalf("add metadata types to scheme: %v", err)
	}
	return metadatafake.NewSimpleMetadataClient(scheme, objects...)
}

func metadataObject(kind, namespace, name string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: kind},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}
}
