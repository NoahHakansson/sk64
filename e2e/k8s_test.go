package e2e

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/NoahHakansson/sk64/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestListPaginatesSecretsAndConfigMaps(t *testing.T) {
	ctx := ctxT(t)
	extraName := "pagination-extra"
	if _, err := client.Clientset.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: extraName, Namespace: namespace},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create extra configmap: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = client.Clientset.CoreV1().ConfigMaps(namespace).Delete(cleanupCtx, extraName, metav1.DeleteOptions{})
	})

	secretNames, secretContinued := listResourceNames(t, func(token string) (k8s.ResourcePage, error) {
		return client.ListSecrets(ctx, namespace, 1, token)
	})
	if !secretContinued || !slices.Contains(secretNames, "app-creds") || !slices.Contains(secretNames, "registry-creds") {
		t.Fatalf("secret pages = %v, continued = %t", secretNames, secretContinued)
	}
	configMapNames, configMapContinued := listResourceNames(t, func(token string) (k8s.ResourcePage, error) {
		return client.ListConfigMaps(ctx, namespace, 1, token)
	})
	if !configMapContinued || !slices.Contains(configMapNames, "app-settings") || !slices.Contains(configMapNames, extraName) {
		t.Fatalf("configmap pages = %v, continued = %t", configMapNames, configMapContinued)
	}
}

func TestCollectNamespaceRefsTagsConsumers(t *testing.T) {
	index, err := client.CollectNamespaceRefs(ctxT(t), namespace)
	if err != nil {
		t.Fatalf("collect namespace refs: %v", err)
	}
	if notes := index.Notes(); len(notes) != 0 {
		t.Fatalf("index notes = %v, want none", notes)
	}
	appCreds := findConsumer(t, index.ConsumersOf(k8s.KindSecret, "app-creds"), k8s.KindDeployment, "web")
	if !slices.Equal(appCreds.Ref.Tags, []k8s.RefTag{k8s.TagEnv, k8s.TagVolume}) ||
		!slices.Equal(appCreds.Ref.Keys, []string{"DB_PASSWORD"}) ||
		!appCreds.Ref.SubPath || !appCreds.Ref.RolloutNeeded {
		t.Fatalf("app-creds consumer = %#v", appCreds)
	}
	settings := findConsumer(t, index.ConsumersOf(k8s.KindConfigMap, "app-settings"), k8s.KindDeployment, "web")
	if !slices.Equal(settings.Ref.Tags, []k8s.RefTag{k8s.TagEnvFrom}) || !settings.Ref.RolloutNeeded {
		t.Fatalf("app-settings consumer = %#v", settings)
	}
	registryConsumers := index.ConsumersOf(k8s.KindSecret, "registry-creds")
	serviceAccount := findConsumer(t, registryConsumers, k8s.KindServiceAccount, "app-sa")
	deployment := findConsumer(t, registryConsumers, k8s.KindDeployment, "web")
	if !slices.Equal(serviceAccount.Ref.Tags, []k8s.RefTag{k8s.TagSA}) ||
		!slices.Equal(deployment.Ref.Tags, []k8s.RefTag{k8s.TagSA}) {
		t.Fatalf("registry-creds consumers = %#v", registryConsumers)
	}
	// CollectNamespaceRefs never calls AddExisting, so resource existence stays
	// unknown and Missing reports false for every name.
	if index.Missing(k8s.KindSecret, "nope") {
		t.Fatal("Missing() = true without recorded existence")
	}
}

func TestDryRunSaveThenSave(t *testing.T) {
	resource := mustCreateSecret(t, "dry-run-save", map[string][]byte{"DB_PASSWORD": []byte("old")})
	oldVersion := resource.ResourceVersion()
	if err := resource.Set("DB_PASSWORD", []byte("s3cret")); err != nil {
		t.Fatalf("set secret value: %v", err)
	}
	if result := client.DryRunSave(ctxT(t), resource); result.Outcome != k8s.DryRunOK {
		t.Fatalf("DryRunSave() = %#v, want DryRunOK", result)
	}
	if result := client.Save(ctxT(t), resource); result.Outcome != k8s.SaveSucceeded {
		t.Fatalf("Save() = %#v, want SaveSucceeded", result)
	}
	saved, err := client.GetResource(ctxT(t), k8s.KindSecret, namespace, resource.Name())
	if err != nil {
		t.Fatalf("get saved secret: %v", err)
	}
	value, err := saved.Get("DB_PASSWORD")
	if err != nil || string(value) != "s3cret" || saved.ResourceVersion() == oldVersion {
		t.Fatalf("saved secret value/version = %q/%q, old version %q, err %v", value, saved.ResourceVersion(), oldVersion, err)
	}
}

func TestSaveConflictAfterConcurrentUpdate(t *testing.T) {
	stale := mustCreateSecret(t, "save-conflict", map[string][]byte{"value": []byte("original")})
	other := secondClient(t)
	concurrent, err := other.GetResource(ctxT(t), k8s.KindSecret, namespace, stale.Name())
	if err != nil {
		t.Fatalf("second client get: %v", err)
	}
	if err := concurrent.Set("value", []byte("other")); err != nil {
		t.Fatalf("second client set: %v", err)
	}
	if result := other.Save(ctxT(t), concurrent); result.Outcome != k8s.SaveSucceeded {
		t.Fatalf("second client Save() = %#v", result)
	}
	if err := stale.Set("value", []byte("mine")); err != nil {
		t.Fatalf("set stale resource: %v", err)
	}
	result := client.Save(ctxT(t), stale)
	if result.Outcome != k8s.SaveConflict || result.Cluster == nil {
		t.Fatalf("stale Save() = %#v, want conflict with cluster resource", result)
	}
	value, err := result.Cluster.Get("value")
	if err != nil || string(value) != "other" || result.Cluster.ResourceVersion() == stale.ResourceVersion() {
		t.Fatalf("cluster resource value/version = %q/%q, stale %q, err %v", value, result.Cluster.ResourceVersion(), stale.ResourceVersion(), err)
	}
}

func TestRestartWorkloadWritesAnnotation(t *testing.T) {
	ctx := ctxT(t)
	if err := client.RestartWorkload(ctx, k8s.KindDeployment, namespace, "web"); err != nil {
		t.Fatalf("restart deployment: %v", err)
	}
	deployment, err := client.Clientset.AppsV1().Deployments(namespace).Get(ctx, "web", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	restartedAt := deployment.Spec.Template.Annotations[k8s.RestartedAtAnnotation]
	if _, err := time.Parse(time.RFC3339, restartedAt); err != nil {
		t.Fatalf("restart annotation = %q: %v", restartedAt, err)
	}
}

func TestDeleteResourceStaleResourceVersionConflicts(t *testing.T) {
	stale := mustCreateSecret(t, "delete-conflict", map[string][]byte{"value": []byte("original")})
	other := secondClient(t)
	concurrent, err := other.GetResource(ctxT(t), k8s.KindSecret, namespace, stale.Name())
	if err != nil {
		t.Fatalf("second client get: %v", err)
	}
	if err := concurrent.Set("value", []byte("updated")); err != nil {
		t.Fatalf("second client set: %v", err)
	}
	if result := other.Save(ctxT(t), concurrent); result.Outcome != k8s.SaveSucceeded {
		t.Fatalf("second client Save() = %#v", result)
	}
	if result := client.DeleteResource(ctxT(t), stale); result.Outcome != k8s.DeleteConflict {
		t.Fatalf("DeleteResource(stale) = %#v, want DeleteConflict", result)
	}
	fresh, err := client.GetResource(ctxT(t), k8s.KindSecret, namespace, stale.Name())
	if err != nil {
		t.Fatalf("get fresh resource: %v", err)
	}
	if result := client.DeleteResource(ctxT(t), fresh); result.Outcome != k8s.DeleteSucceeded {
		t.Fatalf("DeleteResource(fresh) = %#v, want DeleteSucceeded", result)
	}
}

func listResourceNames(t *testing.T, list func(string) (k8s.ResourcePage, error)) ([]string, bool) {
	t.Helper()
	var names []string
	continued := false
	token := ""
	for {
		page, err := list(token)
		if err != nil {
			t.Fatalf("list resource page: %v", err)
		}
		for _, resource := range page.Items {
			names = append(names, resource.Name())
		}
		if page.Continue == "" {
			return names, continued
		}
		continued = true
		token = page.Continue
	}
}

func findConsumer(t *testing.T, consumers []k8s.Consumer, kind, name string) k8s.Consumer {
	t.Helper()
	for _, consumer := range consumers {
		if consumer.Kind == kind && consumer.Name == name {
			return consumer
		}
	}
	t.Fatalf("missing %s/%s consumer in %#v", kind, name, consumers)
	return k8s.Consumer{}
}
