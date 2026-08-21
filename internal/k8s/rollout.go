package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/NoahHakansson/sk64/internal/debuglog"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// RestartedAtAnnotation is the pod-template annotation used by rollout restart.
const RestartedAtAnnotation = "kubectl.kubernetes.io/restartedAt"

// rolloutNow supplies the restart timestamp; tests replace it.
var rolloutNow = time.Now

// RestartableKind reports whether kind supports a rollout restart.
func RestartableKind(kind string) bool {
	return kind == KindDeployment || kind == KindStatefulSet || kind == KindDaemonSet
}

// RestartWorkload applies the same strategic-merge pod-template annotation
// patch as kubectl rollout restart, using the current RFC3339 timestamp.
// Non-restartable kinds return an error.
func (c *Client) RestartWorkload(ctx context.Context, kind, namespace, name string) error {
	if err := c.restartWorkload(ctx, kind, namespace, name); err != nil {
		c.Debug.Resource("restart-failed", kind, namespace, name)
		c.Debug.Err("restart-failed", debuglog.ClassifyError(err))
		return err
	}
	c.Debug.Resource("restart-ok", kind, namespace, name)
	return nil
}

func (c *Client) restartWorkload(ctx context.Context, kind, namespace, name string) error {
	wrap := func(err error) error {
		return fmt.Errorf("restart workload %s/%s in namespace %q: %w", kind, name, namespace, err)
	}
	if !RestartableKind(kind) {
		return wrap(errors.New("kind is not restartable"))
	}
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]string{RestartedAtAnnotation: rolloutNow().Format(time.RFC3339)},
				},
			},
		},
	})
	if err != nil {
		return wrap(fmt.Errorf("build patch: %w", err))
	}
	switch kind {
	case KindDeployment:
		_, err = c.Clientset.AppsV1().Deployments(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case KindStatefulSet:
		_, err = c.Clientset.AppsV1().StatefulSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	case KindDaemonSet:
		_, err = c.Clientset.AppsV1().DaemonSets(namespace).Patch(ctx, name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
	}
	if err != nil {
		return wrap(err)
	}
	return nil
}
