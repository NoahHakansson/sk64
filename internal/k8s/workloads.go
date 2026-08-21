package k8s

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// KindDeployment identifies a Kubernetes Deployment.
	KindDeployment = "Deployment"
	// KindStatefulSet identifies a Kubernetes StatefulSet.
	KindStatefulSet = "StatefulSet"
	// KindDaemonSet identifies a Kubernetes DaemonSet.
	KindDaemonSet = "DaemonSet"
	// KindJob identifies a Kubernetes Job.
	KindJob = "Job"
	// KindCronJob identifies a Kubernetes CronJob.
	KindCronJob = "CronJob"
)

// WorkloadKinds is the fixed order used for multi-kind workload operations.
var WorkloadKinds = []string{KindDeployment, KindStatefulSet, KindDaemonSet, KindJob, KindCronJob}

// Workload is one listed workload with its extracted pod template spec.
type Workload struct {
	Kind      string
	Name      string
	Namespace string
	Ready     string         // Human-readable readiness summary for the workload kind.
	Spec      corev1.PodSpec // Extracted pod template consumed by WalkPodSpec.
}

// WorkloadPage is one page of workloads and its continuation token.
type WorkloadPage struct {
	Items    []Workload
	Continue string
}

// PodPage is one page of live Pods. Items point into the page's private list
// result so callers can pass them directly to RefIndex without another copy.
type PodPage struct {
	Items    []*corev1.Pod
	Continue string
}

// ServiceAccountPage is one page of ServiceAccounts. Items point into the
// page's private list result so callers can pass them directly to RefIndex.
type ServiceAccountPage struct {
	Items    []*corev1.ServiceAccount
	Continue string
}

// ListWorkloads returns one page of the requested supported workload kind.
func (c *Client) ListWorkloads(ctx context.Context, namespace, kind string, limit int64, continueToken string) (WorkloadPage, error) {
	options := metav1.ListOptions{Limit: limit, Continue: continueToken}
	switch kind {
	case KindDeployment:
		return listWorkloadPage[appsv1.Deployment, *appsv1.Deployment](namespace, kind, func() ([]appsv1.Deployment, string, error) {
			result, err := c.Clientset.AppsV1().Deployments(namespace).List(ctx, options)
			if err != nil {
				return nil, "", err
			}
			return result.Items, result.Continue, nil
		})
	case KindStatefulSet:
		return listWorkloadPage[appsv1.StatefulSet, *appsv1.StatefulSet](namespace, kind, func() ([]appsv1.StatefulSet, string, error) {
			result, err := c.Clientset.AppsV1().StatefulSets(namespace).List(ctx, options)
			if err != nil {
				return nil, "", err
			}
			return result.Items, result.Continue, nil
		})
	case KindDaemonSet:
		return listWorkloadPage[appsv1.DaemonSet, *appsv1.DaemonSet](namespace, kind, func() ([]appsv1.DaemonSet, string, error) {
			result, err := c.Clientset.AppsV1().DaemonSets(namespace).List(ctx, options)
			if err != nil {
				return nil, "", err
			}
			return result.Items, result.Continue, nil
		})
	case KindJob:
		return listWorkloadPage[batchv1.Job, *batchv1.Job](namespace, kind, func() ([]batchv1.Job, string, error) {
			result, err := c.Clientset.BatchV1().Jobs(namespace).List(ctx, options)
			if err != nil {
				return nil, "", err
			}
			return result.Items, result.Continue, nil
		})
	case KindCronJob:
		return listWorkloadPage[batchv1.CronJob, *batchv1.CronJob](namespace, kind, func() ([]batchv1.CronJob, string, error) {
			result, err := c.Clientset.BatchV1().CronJobs(namespace).List(ctx, options)
			if err != nil {
				return nil, "", err
			}
			return result.Items, result.Continue, nil
		})
	default:
		return WorkloadPage{}, fmt.Errorf("list workloads in namespace %q: unknown kind %q", namespace, kind)
	}
}

func listWorkloadPage[T any, P interface {
	*T
	metav1.Object
}](namespace, kind string, list func() ([]T, string, error)) (WorkloadPage, error) {
	items, continueToken, err := list()
	if err != nil {
		return WorkloadPage{}, fmt.Errorf("list %s in namespace %q: %w", SourceName(kind), namespace, err)
	}
	page := WorkloadPage{Continue: continueToken, Items: make([]Workload, 0, len(items))}
	for i := range items {
		page.Items = append(page.Items, newWorkload(kind, P(&items[i])))
	}
	return page, nil
}

func newWorkload(kind string, object metav1.Object) Workload {
	spec, _ := PodSpecOf(object)
	return Workload{Kind: kind, Name: object.GetName(), Namespace: object.GetNamespace(), Ready: readySummary(object), Spec: spec}
}

// ListPods returns one page of live Pods in namespace.
func (c *Client) ListPods(ctx context.Context, namespace string, limit int64, continueToken string) (PodPage, error) {
	result, err := c.Clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return PodPage{}, fmt.Errorf("list pods in namespace %q: %w", namespace, err)
	}
	items := make([]*corev1.Pod, 0, len(result.Items))
	for i := range result.Items {
		items = append(items, &result.Items[i])
	}
	return PodPage{Items: items, Continue: result.Continue}, nil
}

// ListServiceAccounts returns one page of ServiceAccounts in namespace.
func (c *Client) ListServiceAccounts(ctx context.Context, namespace string, limit int64, continueToken string) (ServiceAccountPage, error) {
	result, err := c.Clientset.CoreV1().ServiceAccounts(namespace).List(ctx, metav1.ListOptions{Limit: limit, Continue: continueToken})
	if err != nil {
		return ServiceAccountPage{}, fmt.Errorf("list serviceaccounts in namespace %q: %w", namespace, err)
	}
	items := make([]*corev1.ServiceAccount, 0, len(result.Items))
	for i := range result.Items {
		items = append(items, &result.Items[i])
	}
	return ServiceAccountPage{Items: items, Continue: result.Continue}, nil
}

// PodSpecOf returns the pod template spec embedded in a Deployment,
// StatefulSet, DaemonSet, Job, or CronJob. The boolean is false for other
// object types. It is exported for reuse by the repo scanner.
func PodSpecOf(obj any) (corev1.PodSpec, bool) {
	switch obj := obj.(type) {
	case *appsv1.Deployment:
		return obj.Spec.Template.Spec, true
	case *appsv1.StatefulSet:
		return obj.Spec.Template.Spec, true
	case *appsv1.DaemonSet:
		return obj.Spec.Template.Spec, true
	case *batchv1.Job:
		return obj.Spec.Template.Spec, true
	case *batchv1.CronJob:
		return obj.Spec.JobTemplate.Spec.Template.Spec, true
	default:
		return corev1.PodSpec{}, false
	}
}

func readySummary(obj any) string {
	switch obj := obj.(type) {
	case *appsv1.Deployment:
		return fmt.Sprintf("%d/%d ready", obj.Status.ReadyReplicas, replicasOrDefault(obj.Spec.Replicas))
	case *appsv1.StatefulSet:
		return fmt.Sprintf("%d/%d ready", obj.Status.ReadyReplicas, replicasOrDefault(obj.Spec.Replicas))
	case *appsv1.DaemonSet:
		return fmt.Sprintf("%d/%d ready", obj.Status.NumberReady, obj.Status.DesiredNumberScheduled)
	case *batchv1.Job:
		if obj.Spec.Suspend != nil && *obj.Spec.Suspend {
			return "suspended"
		}
		if obj.Status.Active > 0 {
			return fmt.Sprintf("%d active", obj.Status.Active)
		}
		if obj.Status.Failed > 0 {
			return "failed"
		}
		if obj.Status.Succeeded > 0 {
			return "succeeded"
		}
		return "pending"
	case *batchv1.CronJob:
		if obj.Spec.Suspend != nil && *obj.Spec.Suspend {
			return "suspended"
		}
		if len(obj.Status.Active) > 0 {
			return fmt.Sprintf("%s (%d active)", obj.Spec.Schedule, len(obj.Status.Active))
		}
		return obj.Spec.Schedule
	default:
		return ""
	}
}

func replicasOrDefault(replicas *int32) int32 {
	if replicas == nil {
		return 1
	}
	return *replicas
}
