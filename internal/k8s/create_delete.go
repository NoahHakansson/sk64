package k8s

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// WellKnownSecretTypes returns the supported Secret type choices for creation.
func WellKnownSecretTypes() []string {
	return []string{
		string(corev1.SecretTypeOpaque),
		string(corev1.SecretTypeTLS),
		string(corev1.SecretTypeDockerConfigJson),
		string(corev1.SecretTypeBasicAuth),
		string(corev1.SecretTypeSSHAuth),
	}
}

// DryRunCreate submits res as a server-side dry-run create.
func (c *Client) DryRunCreate(ctx context.Context, res Resource) DryRunResult {
	_, err := c.create(ctx, res, metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
	if err == nil {
		return DryRunResult{Outcome: DryRunOK}
	}
	if apierrors.IsAlreadyExists(err) {
		return DryRunResult{Outcome: DryRunRejected, Message: fmt.Sprintf("%s %q already exists", strings.ToLower(res.Kind()), res.Name())}
	}
	return classifyDryRunError(err)
}

// Create submits res as a real create and verifies an ambiguous result once.
func (c *Client) Create(ctx context.Context, res Resource) SaveResult {
	result := c.createAndVerify(ctx, res)
	c.Debug.Resource("create-"+result.Outcome.logSuffix(), res.Kind(), res.Namespace(), res.Name())
	return result
}

func (c *Client) createAndVerify(ctx context.Context, res Resource) SaveResult {
	err := withAttemptDeadline(ctx, func(attemptCtx context.Context) error {
		_, err := c.create(attemptCtx, res, metav1.CreateOptions{})
		return err
	})
	if err == nil {
		return SaveResult{Outcome: SaveSucceeded}
	}
	if apierrors.IsAlreadyExists(err) {
		return SaveResult{Outcome: SaveFailed, Message: fmt.Sprintf("%s %q already exists", strings.ToLower(res.Kind()), res.Name())}
	}
	if apierrors.IsForbidden(err) {
		return SaveResult{Outcome: SaveForbidden, Message: apiStatusMessage(err)}
	}
	if !isAmbiguousNetworkError(err) {
		return SaveResult{Outcome: SaveFailed, Message: err.Error()}
	}

	cluster, getErr := c.getResourceWithDeadline(ctx, res)
	if getErr == nil {
		if sameData(cluster, res) {
			return SaveResult{Outcome: SaveSucceeded}
		}
		return SaveResult{Outcome: SaveFailed, Message: fmt.Sprintf("create result is ambiguous and the resource has different data: %v", err)}
	}
	if apierrors.IsNotFound(getErr) {
		return SaveResult{Outcome: SaveFailed, Message: "create result unknown — refresh and retry"}
	}
	return SaveResult{Outcome: SaveFailed, Message: fmt.Sprintf("create result is ambiguous and current resource could not be fetched: %v", getErr)}
}

func (c *Client) create(ctx context.Context, res Resource, opts metav1.CreateOptions) (Resource, error) {
	switch resource := res.(type) {
	case *secretResource:
		created, err := c.Clientset.CoreV1().Secrets(resource.Namespace()).Create(ctx, resource.secret, opts)
		if err != nil {
			return nil, fmt.Errorf("create secret %q in namespace %q: %w", resource.Name(), resource.Namespace(), err)
		}
		return NewSecret(created), nil
	case *configMapResource:
		created, err := c.Clientset.CoreV1().ConfigMaps(resource.Namespace()).Create(ctx, resource.configMap, opts)
		if err != nil {
			return nil, fmt.Errorf("create configmap %q in namespace %q: %w", resource.Name(), resource.Namespace(), err)
		}
		return NewConfigMap(created), nil
	default:
		return nil, fmt.Errorf("create resource %q in namespace %q: unsupported resource type %T", res.Name(), res.Namespace(), res)
	}
}

// DeleteOutcome classifies a precondition-guarded delete attempt.
type DeleteOutcome int

const (
	DeleteSucceeded DeleteOutcome = iota
	DeleteConflict
	DeleteForbidden
	DeleteFailed
)

func (o DeleteOutcome) logSuffix() string {
	switch o {
	case DeleteSucceeded:
		return "ok"
	case DeleteConflict:
		return "conflict"
	case DeleteForbidden:
		return "forbidden"
	default:
		return "failed"
	}
}

// DeleteResult describes a delete attempt.
type DeleteResult struct {
	Outcome DeleteOutcome
	Message string
}

// DeleteResource deletes a resource guarded by its captured UID and resourceVersion.
func (c *Client) DeleteResource(ctx context.Context, res Resource) DeleteResult {
	result := c.deleteGuarded(ctx, res)
	c.Debug.Resource("delete-"+result.Outcome.logSuffix(), res.Kind(), res.Namespace(), res.Name())
	return result
}

func (c *Client) deleteGuarded(ctx context.Context, res Resource) DeleteResult {
	kind, namespace, name := res.Kind(), res.Namespace(), res.Name()
	resourceVersion := res.ResourceVersion()
	resourceUID := types.UID(res.UID())
	options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &resourceUID, ResourceVersion: &resourceVersion}}
	switch kind {
	case KindSecret, KindConfigMap:
	default:
		return DeleteResult{Outcome: DeleteFailed, Message: fmt.Sprintf("delete resource %q in namespace %q: unknown kind %q", name, namespace, kind)}
	}
	err := withAttemptDeadline(ctx, func(attemptCtx context.Context) error {
		if kind == KindSecret {
			return c.Clientset.CoreV1().Secrets(namespace).Delete(attemptCtx, name, options)
		}
		return c.Clientset.CoreV1().ConfigMaps(namespace).Delete(attemptCtx, name, options)
	})
	if err == nil {
		return DeleteResult{Outcome: DeleteSucceeded}
	}
	if apierrors.IsConflict(err) {
		return DeleteResult{Outcome: DeleteConflict, Message: apiStatusMessage(err)}
	}
	if apierrors.IsForbidden(err) {
		return DeleteResult{Outcome: DeleteForbidden, Message: apiStatusMessage(err)}
	}
	if apierrors.IsNotFound(err) {
		return DeleteResult{Outcome: DeleteSucceeded, Message: "resource was already deleted"}
	}
	return DeleteResult{Outcome: DeleteFailed, Message: fmt.Sprintf("delete %s %q in namespace %q: %v", strings.ToLower(kind), name, namespace, err)}
}
