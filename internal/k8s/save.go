package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DryRunOutcome classifies a server-side dry-run attempt.
type DryRunOutcome int

const (
	DryRunOK DryRunOutcome = iota
	DryRunRejected
	DryRunUnsupported
	DryRunConflict
	DryRunFailed
)

func (o DryRunOutcome) logSuffix() string {
	switch o {
	case DryRunOK:
		return "ok"
	case DryRunRejected:
		return "rejected"
	case DryRunUnsupported:
		return "unsupported"
	case DryRunConflict:
		return "conflict"
	default:
		return "failed"
	}
}

// DryRunResult describes a server-side dry-run attempt.
type DryRunResult struct {
	Outcome DryRunOutcome
	Message string
	Cluster Resource
}

// DryRunSave submits res as a dry-run update without retrying.
func (c *Client) DryRunSave(ctx context.Context, res Resource) DryRunResult {
	result := c.dryRunSave(ctx, res)
	c.Debug.Resource("dry-run-"+result.Outcome.logSuffix(), res.Kind(), res.Namespace(), res.Name())
	return result
}

func (c *Client) dryRunSave(ctx context.Context, res Resource) DryRunResult {
	_, err := c.update(ctx, res, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
	if err == nil {
		return DryRunResult{Outcome: DryRunOK}
	}
	if apierrors.IsConflict(err) {
		cluster, getErr := c.getResourceWithDeadline(ctx, res)
		if getErr != nil {
			return DryRunResult{Outcome: DryRunFailed, Message: fmt.Sprintf("dry-run conflict; fetch current resource: %v", getErr)}
		}
		return DryRunResult{Outcome: DryRunConflict, Message: err.Error(), Cluster: cluster}
	}
	return classifyDryRunError(err)
}

func classifyDryRunError(err error) DryRunResult {
	if strings.Contains(strings.ToLower(err.Error()), "does not support dry run") {
		return DryRunResult{Outcome: DryRunUnsupported, Message: apiStatusMessage(err)}
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		return DryRunResult{Outcome: DryRunRejected, Message: apiStatusMessage(err)}
	}
	return DryRunResult{Outcome: DryRunFailed, Message: err.Error()}
}

// SaveOutcome classifies a real update attempt.
type SaveOutcome int

const (
	SaveSucceeded SaveOutcome = iota
	SaveConflict
	SaveForbidden
	SaveFailed
)

func (o SaveOutcome) logSuffix() string {
	switch o {
	case SaveSucceeded:
		return "ok"
	case SaveConflict:
		return "conflict"
	case SaveForbidden:
		return "forbidden"
	default:
		return "failed"
	}
}

// SaveResult describes a real update attempt.
type SaveResult struct {
	Outcome SaveOutcome
	Cluster Resource
	Message string
}

// attemptDeadline bounds a single apiserver request so a hung connection cannot
// block the UI forever. It sits above the apiserver's own 60s request budget:
// a shorter deadline would abort slow-but-legitimate admission chains and make
// them indistinguishable from network ambiguity, sending the retry out with a
// stale resourceVersion. The resourceVersion guard, not this deadline, is what
// makes retrying safe.
const attemptDeadline = 90 * time.Second

// withAttemptDeadline runs one apiserver attempt under attemptDeadline.
func withAttemptDeadline(ctx context.Context, do func(context.Context) error) error {
	attemptCtx, cancel := context.WithTimeout(ctx, attemptDeadline)
	defer cancel()
	return do(attemptCtx)
}

func (c *Client) getResourceWithDeadline(ctx context.Context, res Resource) (Resource, error) {
	var cluster Resource
	err := withAttemptDeadline(ctx, func(attemptCtx context.Context) error {
		var err error
		cluster, err = c.GetResource(attemptCtx, res.Kind(), res.Namespace(), res.Name())
		return err
	})
	return cluster, err
}

// Save updates res with its original resourceVersion and resolves ambiguous write failures.
func (c *Client) Save(ctx context.Context, res Resource) SaveResult {
	result := c.save(ctx, res)
	c.Debug.Resource("save-"+result.Outcome.logSuffix(), res.Kind(), res.Namespace(), res.Name())
	return result
}

func (c *Client) save(ctx context.Context, res Resource) SaveResult {
	for attempt := 1; attempt <= 3; attempt++ {
		err := withAttemptDeadline(ctx, func(attemptCtx context.Context) error {
			_, err := c.update(attemptCtx, res, metav1.UpdateOptions{})
			return err
		})
		if err == nil {
			return SaveResult{Outcome: SaveSucceeded}
		}
		if apierrors.IsConflict(err) {
			return c.saveConflict(ctx, res, err)
		}
		if apierrors.IsForbidden(err) {
			return SaveResult{Outcome: SaveForbidden, Message: apiStatusMessage(err)}
		}
		if isPreSendError(err) {
			if attempt == 3 {
				return SaveResult{Outcome: SaveFailed, Message: fmt.Sprintf("save failed after %d attempts: %v", attempt, err)}
			}
			retryBackoff(ctx, attempt)
			if ctx.Err() != nil {
				return SaveResult{Outcome: SaveFailed, Message: fmt.Sprintf("save cancelled before retry: %v", ctx.Err())}
			}
			continue
		}
		if isAmbiguousNetworkError(err) {
			cluster, getErr := c.getResourceWithDeadline(ctx, res)
			if getErr != nil {
				return SaveResult{Outcome: SaveFailed, Message: fmt.Sprintf("save result is ambiguous and current resource could not be fetched: %v", getErr)}
			}
			if sameData(cluster, res) {
				return SaveResult{Outcome: SaveSucceeded}
			}
			if cluster.ResourceVersion() != res.ResourceVersion() {
				return SaveResult{Outcome: SaveConflict, Cluster: cluster, Message: fmt.Sprintf("resource changed after ambiguous save: %v", err)}
			}
			if attempt == 3 {
				return SaveResult{Outcome: SaveFailed, Message: fmt.Sprintf("save remained ambiguous after %d attempts: %v", attempt, err)}
			}
			retryBackoff(ctx, attempt)
			if ctx.Err() != nil {
				return SaveResult{Outcome: SaveFailed, Message: fmt.Sprintf("save cancelled before retry: %v", ctx.Err())}
			}
			continue
		}
		return SaveResult{Outcome: SaveFailed, Message: err.Error()}
	}
	return SaveResult{Outcome: SaveFailed, Message: "save failed: retry budget exhausted"}
}

func (c *Client) saveConflict(ctx context.Context, res Resource, updateErr error) SaveResult {
	cluster, err := c.getResourceWithDeadline(ctx, res)
	if err != nil {
		return SaveResult{Outcome: SaveFailed, Message: fmt.Sprintf("save conflict; fetch current resource: %v", err)}
	}
	return SaveResult{Outcome: SaveConflict, Cluster: cluster, Message: apiStatusMessage(updateErr)}
}

func (c *Client) update(ctx context.Context, res Resource, opts metav1.UpdateOptions) (Resource, error) {
	switch resource := res.(type) {
	case *secretResource:
		updated, err := c.Clientset.CoreV1().Secrets(resource.Namespace()).Update(ctx, resource.secret, opts)
		if err != nil {
			return nil, fmt.Errorf("update secret %q in namespace %q: %w", resource.Name(), resource.Namespace(), err)
		}
		return NewSecret(updated), nil
	case *configMapResource:
		updated, err := c.Clientset.CoreV1().ConfigMaps(resource.Namespace()).Update(ctx, resource.configMap, opts)
		if err != nil {
			return nil, fmt.Errorf("update configmap %q in namespace %q: %w", resource.Name(), resource.Namespace(), err)
		}
		return NewConfigMap(updated), nil
	default:
		return nil, fmt.Errorf("update resource %q in namespace %q: unsupported resource type %T", res.Name(), res.Namespace(), res)
	}
}

func sameData(a, b Resource) bool {
	aKeys, bKeys := a.Keys(), b.Keys()
	if len(aKeys) != len(bKeys) {
		return false
	}
	for i, key := range aKeys {
		if key != bKeys[i] {
			return false
		}
		aValue, aErr := a.Get(key)
		bValue, bErr := b.Get(key)
		if aErr != nil || bErr != nil || !bytes.Equal(aValue, bValue) {
			return false
		}
	}
	return true
}

func isAmbiguousNetworkError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) {
		return true
	}
	var networkError net.Error
	return (errors.As(err, &networkError) && networkError.Timeout()) || strings.Contains(strings.ToLower(err.Error()), "connection reset")
}

func isPreSendError(err error) bool {
	var dnsError *net.DNSError
	return errors.As(err, &dnsError) || errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(strings.ToLower(err.Error()), "connection refused")
}

var retryBackoff = func(ctx context.Context, attempt int) {
	timer := time.NewTimer(500 * time.Millisecond << (attempt - 1))
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func apiStatusMessage(err error) string {
	var status apierrors.APIStatus
	if errors.As(err, &status) && status.Status().Message != "" {
		return status.Status().Message
	}
	return err.Error()
}
