package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/NoahHakansson/sk64/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

var (
	client     *k8s.Client
	restConfig *rest.Config
	namespace  = "sk64-e2e"
)

func TestMain(m *testing.M) { os.Exit(runSuite(m)) }

func runSuite(m *testing.M) int {
	cfg, stop, err := startControlPlane()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: %v\n", err)
		return 1
	}
	defer func() {
		if err := stop(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e teardown: stop control plane: %v\n", err)
		}
	}()
	restConfig = cfg

	client, err = k8s.NewForConfig(restConfig, namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: create Kubernetes client: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := client.Probe(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: probe control plane: %v\n", err)
		return 1
	}
	if _, err := client.Clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{}); err != nil {
		fmt.Fprintf(os.Stderr, "e2e setup: create namespace %q: %v\n", namespace, err)
		return 1
	}

	fixtures := []struct {
		name   string
		create func() error
	}{
		{
			name: "secret app-creds",
			create: func() error {
				_, err := client.Clientset.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "app-creds", Namespace: namespace},
					Type:       corev1.SecretTypeOpaque,
					Data: map[string][]byte{
						"DB_PASSWORD": []byte("hunter2"),
						"session.db":  {0x00, 0x01, 0x02, 0xff},
					},
				}, metav1.CreateOptions{})
				return err
			},
		},
		{
			name: "configmap app-settings",
			create: func() error {
				_, err := client.Clientset.CoreV1().ConfigMaps(namespace).Create(ctx, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "app-settings", Namespace: namespace},
					Data:       map[string]string{"LOG_LEVEL": "debug"},
				}, metav1.CreateOptions{})
				return err
			},
		},
		{
			name: "serviceaccount app-sa",
			create: func() error {
				_, err := client.Clientset.CoreV1().ServiceAccounts(namespace).Create(ctx, &corev1.ServiceAccount{
					ObjectMeta:       metav1.ObjectMeta{Name: "app-sa", Namespace: namespace},
					ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry-creds"}},
				}, metav1.CreateOptions{})
				return err
			},
		},
		{
			name: "secret registry-creds",
			create: func() error {
				_, err := client.Clientset.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "registry-creds", Namespace: namespace},
					Type:       corev1.SecretTypeDockerConfigJson,
					Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte(`{"auths":{}}`)},
				}, metav1.CreateOptions{})
				return err
			},
		},
		{
			name: "deployment web",
			create: func() error {
				replicas := int32(0)
				_, err := client.Clientset.AppsV1().Deployments(namespace).Create(ctx, &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: namespace},
					Spec: appsv1.DeploymentSpec{
						Replicas: &replicas,
						Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}},
							Spec: corev1.PodSpec{
								ServiceAccountName: "app-sa",
								Containers: []corev1.Container{{
									Name:  "app",
									Image: "registry.k8s.io/pause:3.10",
									Env: []corev1.EnvVar{{
										Name: "DB_PASSWORD",
										ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{Name: "app-creds"},
											Key:                  "DB_PASSWORD",
										}},
									}},
									EnvFrom: []corev1.EnvFromSource{{
										ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-settings"}},
									}},
									VolumeMounts: []corev1.VolumeMount{{
										Name: "creds", MountPath: "/etc/creds/db", SubPath: "DB_PASSWORD",
									}},
								}},
								Volumes: []corev1.Volume{{
									Name: "creds",
									VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
										SecretName: "app-creds",
									}},
								}},
							},
						},
					},
				}, metav1.CreateOptions{})
				return err
			},
		},
	}
	for _, fixture := range fixtures {
		if err := fixture.create(); err != nil {
			fmt.Fprintf(os.Stderr, "e2e setup: create %s: %v\n", fixture.name, err)
			return 1
		}
	}
	return m.Run()
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func mustCreateSecret(t *testing.T, name string, data map[string][]byte) k8s.Resource {
	t.Helper()
	ctx := ctxT(t)
	if _, err := client.Clientset.CoreV1().Secrets(namespace).Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create secret %q: %v", name, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := client.Clientset.CoreV1().Secrets(namespace).Delete(cleanupCtx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete secret %q: %v", name, err)
		}
	})
	resource, err := client.GetResource(ctx, k8s.KindSecret, namespace, name)
	if err != nil {
		t.Fatalf("get secret %q: %v", name, err)
	}
	return resource
}

func secondClient(t *testing.T) *k8s.Client {
	t.Helper()
	other, err := k8s.NewForConfig(restConfig, namespace)
	if err != nil {
		t.Fatalf("create second Kubernetes client: %v", err)
	}
	return other
}
