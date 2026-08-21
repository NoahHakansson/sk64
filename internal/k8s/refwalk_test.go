package k8s

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func loadFixture[T any](t *testing.T, name string) T {
	t.Helper()
	// #nosec G304 -- callers provide repository-owned test fixture names.
	data, err := os.ReadFile(filepath.Join("testdata", "refwalk", name))
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return result
}

func TestWalkPodSpecEnv(t *testing.T) {
	pod := loadFixture[corev1.Pod](t, "env.yaml")
	refs, _ := WalkPodSpec(&pod.Spec)
	want := []Ref{
		{Kind: KindSecret, Name: "db-secret", Key: "password", Tag: TagEnv},
		{Kind: KindConfigMap, Name: "app-config", Key: "endpoint", Tag: TagEnv},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %#v, want %#v", refs, want)
	}
}

func TestWalkPodSpecEnvFrom(t *testing.T) {
	pod := loadFixture[corev1.Pod](t, "envfrom.yaml")
	refs, _ := WalkPodSpec(&pod.Spec)
	want := []Ref{
		{Kind: KindSecret, Name: "db-secret", Tag: TagEnvFrom},
		{Kind: KindConfigMap, Name: "app-config", Tag: TagEnvFrom},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %#v, want %#v", refs, want)
	}
}

func TestWalkPodSpecVolumes(t *testing.T) {
	pod := loadFixture[corev1.Pod](t, "volumes.yaml")
	refs, _ := WalkPodSpec(&pod.Spec)
	want := []Ref{
		{Kind: KindSecret, Name: "mounted-secret", Tag: TagVolume, SubPath: true},
		{Kind: KindConfigMap, Name: "mounted-config", Tag: TagVolume, SubPath: true},
		{Kind: KindSecret, Name: "unmounted-secret", Tag: TagVolume},
		{Kind: KindSecret, Name: "csi-publish-secret", Tag: TagVolume},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %#v, want %#v", refs, want)
	}
}

func TestWalkPodSpecProjected(t *testing.T) {
	pod := loadFixture[corev1.Pod](t, "projected.yaml")
	refs, _ := WalkPodSpec(&pod.Spec)
	want := []Ref{
		{Kind: KindSecret, Name: "projected-secret", Tag: TagProjected, SubPath: true},
		{Kind: KindConfigMap, Name: "projected-config", Tag: TagProjected, SubPath: true},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %#v, want %#v", refs, want)
	}
}

func TestWalkPodSpecPullAndSA(t *testing.T) {
	pod := loadFixture[corev1.Pod](t, "pull_sa.yaml")
	refs, saName := WalkPodSpec(&pod.Spec)
	want := []Ref{{Kind: KindSecret, Name: "registry-secret", Tag: TagPull}}
	if !reflect.DeepEqual(refs, want) || saName != "app-sa" {
		t.Fatalf("refs/sa = %#v/%q, want %#v/%q", refs, saName, want, "app-sa")
	}
	_, saName = WalkPodSpec(&corev1.PodSpec{})
	if saName != "default" {
		t.Fatalf("default service account = %q", saName)
	}
}

func TestWalkPodSpecAllContainerLists(t *testing.T) {
	pod := loadFixture[corev1.Pod](t, "containers.yaml")
	refs, _ := WalkPodSpec(&pod.Spec)
	want := []ResourceRef{{
		Kind: KindSecret, Name: "shared-secret", Tags: []RefTag{TagEnv, TagEnvFrom}, Keys: []string{"token"}, RolloutNeeded: true,
	}}
	if got := AggregateRefs(refs); !reflect.DeepEqual(got, want) {
		t.Fatalf("aggregate = %#v, want %#v", got, want)
	}
}

func TestServiceAccountRefs(t *testing.T) {
	serviceAccount := loadFixture[corev1.ServiceAccount](t, "serviceaccount.yaml")
	want := []Ref{
		{Kind: KindSecret, Name: "token-secret", Tag: TagSA},
		{Kind: KindSecret, Name: "registry-secret", Tag: TagSA},
	}
	if got := ServiceAccountRefs(&serviceAccount); !reflect.DeepEqual(got, want) {
		t.Fatalf("refs = %#v, want %#v", got, want)
	}
}

func TestAggregateRefsRolloutNeeded(t *testing.T) {
	tests := []struct {
		name        string
		tag         RefTag
		subPath     bool
		wantRollout bool
	}{
		{name: "ordinary volume", tag: TagVolume},
		{name: "subPath volume", tag: TagVolume, subPath: true, wantRollout: true},
		{name: "ordinary projected volume", tag: TagProjected},
		{name: "subPath projected volume", tag: TagProjected, subPath: true, wantRollout: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			refs := AggregateRefs([]Ref{{Kind: KindConfigMap, Name: "settings", Tag: test.tag, SubPath: test.subPath}})
			if len(refs) != 1 {
				t.Fatalf("aggregate count = %d, want 1", len(refs))
			}
			if refs[0].RolloutNeeded != test.wantRollout {
				t.Fatalf("RolloutNeeded = %t, want %t for tag %q with SubPath %t", refs[0].RolloutNeeded, test.wantRollout, test.tag, test.subPath)
			}
		})
	}
}

func TestAggregateRefs(t *testing.T) {
	tests := []struct {
		name string
		refs []Ref
		want []ResourceRef
	}{
		{
			name: "orders tags and unions keys",
			refs: []Ref{
				{Kind: KindSecret, Name: "z", Tag: TagSA},
				{Kind: KindSecret, Name: "z", Tag: TagEnvFrom},
				{Kind: KindSecret, Name: "z", Key: "b", Tag: TagEnv},
				{Kind: KindSecret, Name: "z", Key: "a", Tag: TagEnv, SubPath: true},
				{Kind: KindSecret, Name: "z", Key: "a", Tag: TagEnv},
			},
			want: []ResourceRef{{Kind: KindSecret, Name: "z", Tags: []RefTag{TagEnv, TagEnvFrom, TagSA}, Keys: []string{"a", "b"}, SubPath: true, RolloutNeeded: true}},
		},
		{
			name: "sorts kind then name",
			refs: []Ref{
				{Kind: KindSecret, Name: "b", Tag: TagPull},
				{Kind: KindConfigMap, Name: "z", Tag: TagVolume},
				{Kind: KindSecret, Name: "a", Tag: TagProjected},
			},
			want: []ResourceRef{
				{Kind: KindConfigMap, Name: "z", Tags: []RefTag{TagVolume}},
				{Kind: KindSecret, Name: "a", Tags: []RefTag{TagProjected}},
				{Kind: KindSecret, Name: "b", Tags: []RefTag{TagPull}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := AggregateRefs(test.refs); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("aggregate = %#v, want %#v", got, test.want)
			}
		})
	}
}
