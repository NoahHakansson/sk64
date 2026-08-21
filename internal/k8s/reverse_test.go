package k8s

import (
	"context"
	"errors"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestRefIndexWorkloadsAndSAChain(t *testing.T) {
	index := NewRefIndex()
	index.AddWorkload(Workload{Kind: KindStatefulSet, Name: "database", Namespace: "ns", Spec: podSpecWithSA("app-sa")})
	index.AddWorkload(Workload{Kind: KindDeployment, Name: "api", Namespace: "ns", Spec: podSpecWithEnvAndSA("chain-secret", "token", "app-sa")})
	index.AddWorkload(Workload{Kind: KindDaemonSet, Name: "agent", Namespace: "ns", Spec: podSpecWithSA("other-sa")})
	index.AddServiceAccount(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "app-sa", Namespace: "ns"},
		Secrets:    []corev1.ObjectReference{{Name: "chain-secret"}},
	})
	entries := index.Workloads()
	if len(entries) != 3 || entries[0].Workload.Name != "api" || entries[1].Workload.Name != "database" || entries[2].Workload.Name != "agent" {
		t.Fatalf("workload order = %+v", entries)
	}
	wantAPI := []ResourceRef{{Kind: KindSecret, Name: "chain-secret", Tags: []RefTag{TagEnv, TagSA}, Keys: []string{"token"}, RolloutNeeded: true}}
	wantDatabase := []ResourceRef{{Kind: KindSecret, Name: "chain-secret", Tags: []RefTag{TagSA}}}
	if !reflect.DeepEqual(entries[0].Refs, wantAPI) || !reflect.DeepEqual(entries[1].Refs, wantDatabase) || len(entries[2].Refs) != 0 {
		t.Fatalf("workload refs = %#v / %#v / %#v", entries[0].Refs, entries[1].Refs, entries[2].Refs)
	}
}

func TestRefIndexPodDedupe(t *testing.T) {
	tests := []struct {
		name      string
		workloads []Workload
		owners    []metav1.OwnerReference
		noRefs    bool
		kept      bool
	}{
		{name: "matching deployment", workloads: []Workload{{Kind: KindDeployment, Name: "web"}}, owners: []metav1.OwnerReference{controllerOwnerNamed("ReplicaSet", "web-5f7d9")}},
		{name: "similarly named bare replicaset", workloads: []Workload{{Kind: KindDeployment, Name: "web"}}, owners: []metav1.OwnerReference{controllerOwnerNamed("ReplicaSet", "web-manual")}, kept: true},
		{name: "bare replicaset", owners: []metav1.OwnerReference{controllerOwnerNamed("ReplicaSet", "legacy-rs")}, kept: true},
		{name: "matching statefulset", workloads: []Workload{{Kind: KindStatefulSet, Name: "db"}}, owners: []metav1.OwnerReference{controllerOwnerNamed(KindStatefulSet, "db")}},
		{name: "unindexed statefulset", owners: []metav1.OwnerReference{controllerOwnerNamed(KindStatefulSet, "db")}, kept: true},
		{name: "unknown controller", owners: []metav1.OwnerReference{controllerOwnerNamed("Widget", "operator")}, kept: true},
		{name: "uncontrolled", kept: true},
		{name: "no references", noRefs: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			index := NewRefIndex()
			for _, workload := range test.workloads {
				index.AddWorkload(workload)
			}
			spec := podSpecWithVolume("secret")
			if test.noRefs {
				spec = corev1.PodSpec{}
			}
			index.AddPod(testPod("pod", spec, test.owners...))
			names, refs := index.Orphans()
			if got := len(names) == 1; got != test.kept {
				t.Fatalf("pod kept = %t, want %t; names = %v", got, test.kept, names)
			}
			if test.kept {
				want := []ResourceRef{{Kind: KindSecret, Name: "secret", Tags: []RefTag{TagVolume}}}
				if !reflect.DeepEqual(refs, want) {
					t.Fatalf("orphan refs = %#v, want %#v", refs, want)
				}
			}
		})
	}
}

func TestRefIndexConsumersIncludeUncoveredPods(t *testing.T) {
	pod := testPod("owned", podSpecWithVolume("secret"), controllerOwnerNamed("ReplicaSet", "web-5f7d9"))
	index := NewRefIndex()
	index.AddSourceError("deployments")
	index.AddPod(pod)
	consumers := index.ConsumersOf(KindSecret, "secret")
	if len(consumers) != 1 || consumers[0].Kind != KindPod || consumers[0].Name != "owned" {
		t.Fatalf("uncovered consumers = %#v, want Pod/owned", consumers)
	}

	index = NewRefIndex()
	index.AddPod(pod)
	index.AddWorkload(Workload{Kind: KindDeployment, Name: "web"})
	if consumers := index.ConsumersOf(KindSecret, "secret"); len(consumers) != 0 {
		t.Fatalf("covered consumers = %#v, want none", consumers)
	}
}

func TestRefIndexConsumersOf(t *testing.T) {
	index := NewRefIndex()
	index.AddWorkload(Workload{Kind: KindDeployment, Name: "web", Namespace: "ns", Spec: podSpecWithEnvAndSA("target", "password", "default")})
	index.AddWorkload(Workload{Kind: KindStatefulSet, Name: "db", Namespace: "ns", Spec: podSpecWithSA("app-sa")})
	index.AddPod(testPod("manual", podSpecWithVolume("target")))
	index.AddPod(testPod("alpha", podSpecWithVolume("target")))
	index.AddServiceAccount(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "app-sa", Namespace: "ns"}, Secrets: []corev1.ObjectReference{{Name: "target"}}})
	index.AddServiceAccount(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "z-sa", Namespace: "ns"}, Secrets: []corev1.ObjectReference{{Name: "target"}}})
	consumers := index.ConsumersOf(KindSecret, "target")
	want := []Consumer{
		{Kind: KindDeployment, Name: "web", Ref: ResourceRef{Kind: KindSecret, Name: "target", Tags: []RefTag{TagEnv}, Keys: []string{"password"}, RolloutNeeded: true}},
		{Kind: KindStatefulSet, Name: "db", Ref: ResourceRef{Kind: KindSecret, Name: "target", Tags: []RefTag{TagSA}}},
		{Kind: KindPod, Name: "alpha", Ref: ResourceRef{Kind: KindSecret, Name: "target", Tags: []RefTag{TagVolume}}},
		{Kind: KindPod, Name: "manual", Ref: ResourceRef{Kind: KindSecret, Name: "target", Tags: []RefTag{TagVolume}}},
		{Kind: KindServiceAccount, Name: "app-sa", Ref: ResourceRef{Kind: KindSecret, Name: "target", Tags: []RefTag{TagSA}}},
		{Kind: KindServiceAccount, Name: "z-sa", Ref: ResourceRef{Kind: KindSecret, Name: "target", Tags: []RefTag{TagSA}}},
	}
	if !reflect.DeepEqual(consumers, want) {
		t.Fatalf("consumers = %#v, want %#v", consumers, want)
	}
	if unrelated := index.ConsumersOf(KindSecret, "unrelated"); len(unrelated) != 0 {
		t.Fatalf("unrelated consumers = %#v", unrelated)
	}
}

func TestRefIndexMissingAndNotes(t *testing.T) {
	index := NewRefIndex()
	index.AddExisting(KindSecret, []string{"present"}, false)
	if index.Missing(KindSecret, "absent") {
		t.Fatal("partial resource listing reported absent resource as missing")
	}
	index.AddExisting(KindSecret, []string{"last-page"}, true)
	if !index.Missing(KindSecret, "absent") || index.Missing(KindSecret, "present") || index.Missing(KindConfigMap, "unknown") {
		t.Fatalf("unexpected missing results")
	}
	index.AddSourceError("pods")
	index.AddSourceError("jobs")
	if want := []string{"pods not listable", "jobs not listable"}; !reflect.DeepEqual(index.Notes(), want) {
		t.Fatalf("notes = %v, want %v", index.Notes(), want)
	}
	if want := []string{"pods", "jobs"}; !reflect.DeepEqual(index.FailedSources(), want) {
		t.Fatalf("failed sources = %v, want %v", index.FailedSources(), want)
	}
	wantSources := map[string]string{
		KindDeployment: "deployments", KindStatefulSet: "statefulsets", KindDaemonSet: "daemonsets", KindJob: "jobs", KindCronJob: "cronjobs",
	}
	for kind, want := range wantSources {
		if got := SourceName(kind); got != want {
			t.Fatalf("SourceName(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestCollectNamespaceRefs(t *testing.T) {
	client := &Client{Clientset: fake.NewClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"},
			Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: podSpecWithSA("app-sa")}},
		},
		testPod("owned", podSpecWithVolume("owned-secret"), controllerOwnerNamed("ReplicaSet", "web-bcfd2")),
		testPod("manual", podSpecWithVolume("manual-secret")),
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "app-sa", Namespace: "ns"}, Secrets: []corev1.ObjectReference{{Name: "sa-secret"}}},
	)}
	index, err := client.CollectNamespaceRefs(t.Context(), "ns")
	if err != nil {
		t.Fatal(err)
	}
	workloads := index.Workloads()
	if len(workloads) != 1 || workloads[0].Workload.Name != "web" || len(workloads[0].Refs) != 1 || workloads[0].Refs[0].Name != "sa-secret" {
		t.Fatalf("workloads = %#v", workloads)
	}
	names, refs := index.Orphans()
	if !reflect.DeepEqual(names, []string{"manual"}) || len(refs) != 1 || refs[0].Name != "manual-secret" {
		t.Fatalf("orphans = %v %#v", names, refs)
	}
}

func TestCollectNamespaceRefsDegraded(t *testing.T) {
	clientset := fake.NewClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"}})
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "items"}, "", errors.New("denied"))
	clientset.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, forbidden })
	clientset.PrependReactor("list", "statefulsets", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, forbidden })
	client := &Client{Clientset: clientset}
	index, err := client.CollectNamespaceRefs(t.Context(), "ns")
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Workloads()) != 1 || !reflect.DeepEqual(index.Notes(), []string{"statefulsets not listable", "pods not listable"}) {
		t.Fatalf("degraded index = workloads %#v notes %v", index.Workloads(), index.Notes())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.CollectNamespaceRefs(ctx, "ns"); err == nil {
		t.Fatal("cancelled collection returned nil error")
	}
}

func TestCollectPagedForwardsContinuation(t *testing.T) {
	index := NewRefIndex()
	var tokens []string

	err := collectPaged(t.Context(), index, "pods", func(continueToken string) (string, error) {
		tokens = append(tokens, continueToken)
		if len(tokens) == 1 {
			return "next-pods", nil
		}
		return "", nil
	})

	if err != nil {
		t.Fatalf("collectPaged() error = %v", err)
	}
	if want := []string{"", "next-pods"}; !reflect.DeepEqual(tokens, want) {
		t.Fatalf("collectPaged() continue tokens = %v, want %v", tokens, want)
	}
}

func podSpecWithEnvAndSA(secret, key, serviceAccount string) corev1.PodSpec {
	spec := podSpecWithSA(serviceAccount)
	spec.Containers[0].Env = []corev1.EnvVar{{
		Name: "VALUE", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: key}},
	}}
	return spec
}

func podSpecWithSA(serviceAccount string) corev1.PodSpec {
	return corev1.PodSpec{ServiceAccountName: serviceAccount, Containers: []corev1.Container{{Name: "app"}}}
}

func podSpecWithVolume(secret string) corev1.PodSpec {
	return corev1.PodSpec{
		Containers: []corev1.Container{{Name: "app"}},
		Volumes: []corev1.Volume{{Name: "secret", VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{SecretName: secret},
		}}},
	}
}

func testPod(name string, spec corev1.PodSpec, owners ...metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", OwnerReferences: owners}, Spec: spec}
}

func controllerOwnerNamed(kind, name string) metav1.OwnerReference {
	controller := true
	return metav1.OwnerReference{Kind: kind, Name: name, UID: types.UID(name), Controller: &controller}
}
