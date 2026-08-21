package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestListWorkloadsPerKind(t *testing.T) {
	objects := []runtime.Object{
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "deploy", Namespace: "ns"}, Spec: appsv1.DeploymentSpec{Template: podTemplate("deployment-container")}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "stateful", Namespace: "ns"}, Spec: appsv1.StatefulSetSpec{Template: podTemplate("statefulset-container")}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "daemon", Namespace: "ns"}, Spec: appsv1.DaemonSetSpec{Template: podTemplate("daemonset-container")}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "ns"}, Spec: batchv1.JobSpec{Template: podTemplate("job-container")}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "cron", Namespace: "ns"}, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: podTemplate("cronjob-container")}}}},
	}
	client := &Client{Clientset: fake.NewClientset(objects...)}
	tests := []struct {
		kind, name, container string
	}{
		{KindDeployment, "deploy", "deployment-container"},
		{KindStatefulSet, "stateful", "statefulset-container"},
		{KindDaemonSet, "daemon", "daemonset-container"},
		{KindJob, "job", "job-container"},
		{KindCronJob, "cron", "cronjob-container"},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			page, err := client.ListWorkloads(t.Context(), "ns", test.kind, DefaultPageSize, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 1 {
				t.Fatalf("items = %d", len(page.Items))
			}
			item := page.Items[0]
			if item.Kind != test.kind || item.Name != test.name || item.Namespace != "ns" || item.Spec.Containers[0].Name != test.container {
				t.Fatalf("workload = %+v", item)
			}
		})
	}
}

func TestPodSpecOf(t *testing.T) {
	cronJob := loadFixture[batchv1.CronJob](t, "cronjob.yaml")
	tests := []struct {
		name string
		obj  any
		want string
		ok   bool
	}{
		{"deployment", &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Template: podTemplate("deployment")}}, "deployment", true},
		{"statefulset", &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Template: podTemplate("statefulset")}}, "statefulset", true},
		{"daemonset", &appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{Template: podTemplate("daemonset")}}, "daemonset", true},
		{"job", &batchv1.Job{Spec: batchv1.JobSpec{Template: podTemplate("job")}}, "job", true},
		{"cronjob", &cronJob, "cron-container", true},
		{"unsupported", &corev1.Pod{}, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec, ok := PodSpecOf(test.obj)
			if ok != test.ok {
				t.Fatalf("ok = %t", ok)
			}
			if ok && spec.Containers[0].Name != test.want {
				t.Fatalf("container = %q", spec.Containers[0].Name)
			}
		})
	}
}

func TestReadySummaries(t *testing.T) {
	three := int32(3)
	truth := true
	tests := []struct {
		name string
		obj  any
		want string
	}{
		{"deployment", &appsv1.Deployment{Spec: appsv1.DeploymentSpec{Replicas: &three}, Status: appsv1.DeploymentStatus{ReadyReplicas: 2}}, "2/3 ready"},
		{"deployment default replicas", &appsv1.Deployment{Status: appsv1.DeploymentStatus{ReadyReplicas: 1}}, "1/1 ready"},
		{"statefulset", &appsv1.StatefulSet{Spec: appsv1.StatefulSetSpec{Replicas: &three}, Status: appsv1.StatefulSetStatus{ReadyReplicas: 2}}, "2/3 ready"},
		{"daemonset", &appsv1.DaemonSet{Status: appsv1.DaemonSetStatus{NumberReady: 4, DesiredNumberScheduled: 5}}, "4/5 ready"},
		{"job suspended", &batchv1.Job{Spec: batchv1.JobSpec{Suspend: &truth}}, "suspended"},
		{"job active", &batchv1.Job{Status: batchv1.JobStatus{Active: 2}}, "2 active"},
		{"job failed", &batchv1.Job{Status: batchv1.JobStatus{Failed: 1}}, "failed"},
		{"job succeeded", &batchv1.Job{Status: batchv1.JobStatus{Succeeded: 1}}, "succeeded"},
		{"job pending", &batchv1.Job{}, "pending"},
		{"cronjob", &batchv1.CronJob{Spec: batchv1.CronJobSpec{Schedule: "@daily"}}, "@daily"},
		{"cronjob active", &batchv1.CronJob{Spec: batchv1.CronJobSpec{Schedule: "@daily"}, Status: batchv1.CronJobStatus{Active: []corev1.ObjectReference{{}, {}}}}, "@daily (2 active)"},
		{"cronjob suspended", &batchv1.CronJob{Spec: batchv1.CronJobSpec{Schedule: "@daily", Suspend: &truth}}, "suspended"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := readySummary(test.obj); got != test.want {
				t.Fatalf("summary = %q, want %q", got, test.want)
			}
		})
	}
}

func TestListWorkloadsErrors(t *testing.T) {
	clientset := fake.NewClientset()
	client := &Client{Clientset: clientset}
	if _, err := client.ListWorkloads(t.Context(), "ns", "Widget", 1, ""); err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("unknown kind error = %v", err)
	}
	forbidden := apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "", errors.New("denied"))
	clientset.PrependReactor("list", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, forbidden
	})
	_, err := client.ListWorkloads(t.Context(), "ns", KindDeployment, 1, "")
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "list deployments") {
		t.Fatalf("forbidden error = %v", err)
	}
}

func TestListPodsAndServiceAccounts(t *testing.T) {
	client := &Client{Clientset: fake.NewClientset(
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "ns"}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "sa", Namespace: "ns"}},
	)}
	pods, err := client.ListPods(context.Background(), "ns", 10, "")
	if err != nil || len(pods.Items) != 1 || pods.Items[0].Name != "pod" {
		t.Fatalf("pods = %+v, err = %v", pods, err)
	}
	serviceAccounts, err := client.ListServiceAccounts(context.Background(), "ns", 10, "")
	if err != nil || len(serviceAccounts.Items) != 1 || serviceAccounts.Items[0].Name != "sa" {
		t.Fatalf("serviceaccounts = %+v, err = %v", serviceAccounts, err)
	}
}

func podTemplate(container string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: container}}}}
}
