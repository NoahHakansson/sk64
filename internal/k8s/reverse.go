package k8s

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type indexedWorkload struct {
	workload Workload
	refs     []Ref
	saName   string
}

type indexedPod struct {
	name      string
	refs      []Ref
	saName    string
	ownerKind string
	ownerName string
}

// RefIndex accumulates one namespace's Secret and ConfigMap consumers. It has
// no cluster access and is not safe for concurrent use.
type RefIndex struct {
	workloads       []indexedWorkload
	pods            []indexedPod
	serviceAccounts map[string]*corev1.ServiceAccount
	existing        map[string]map[string]struct{}
	existenceKnown  map[string]bool
	failedSources   []string
}

// NewRefIndex creates an empty reference index.
func NewRefIndex() *RefIndex {
	return &RefIndex{
		serviceAccounts: make(map[string]*corev1.ServiceAccount),
		existing:        make(map[string]map[string]struct{}),
		existenceKnown:  make(map[string]bool),
	}
}

// AddWorkload walks and records a workload's pod template references.
func (x *RefIndex) AddWorkload(w Workload) {
	refs, saName := WalkPodSpec(&w.Spec)
	x.workloads = append(x.workloads, indexedWorkload{workload: w, refs: refs, saName: saName})
}

// AddPod records a Pod and its references. Whether the Pod is deduped against
// an indexed workload is decided when results are queried, so Pods may be added
// before the workloads that own them.
func (x *RefIndex) AddPod(pod *corev1.Pod) {
	if pod == nil {
		return
	}
	refs, saName := WalkPodSpec(&pod.Spec)
	indexed := indexedPod{name: pod.Name, refs: refs, saName: saName}
	if owner := metav1.GetControllerOf(pod); owner != nil {
		indexed.ownerKind = owner.Kind
		indexed.ownerName = owner.Name
	}
	x.pods = append(x.pods, indexed)
}

// AddServiceAccount records an account for lazy chain resolution and direct
// Secret-consumer queries.
func (x *RefIndex) AddServiceAccount(sa *corev1.ServiceAccount) {
	if sa == nil || sa.Name == "" {
		return
	}
	clone := sa.DeepCopy()
	x.serviceAccounts[clone.Name] = clone
}

// AddExisting records the known resource names for a kind so Missing can
// distinguish absent resources from unknown existence.
func (x *RefIndex) AddExisting(kind string, names []string, complete bool) {
	if kind != KindSecret && kind != KindConfigMap {
		return
	}
	if complete {
		x.existenceKnown[kind] = true
	}
	if x.existing[kind] == nil {
		x.existing[kind] = make(map[string]struct{})
	}
	for _, name := range names {
		if name != "" {
			x.existing[kind][name] = struct{}{}
		}
	}
}

// AddSourceError records that source could not be listed. The index keeps the
// source name rather than an error because its notes intentionally expose a
// stable degraded-mode message to TUI callers.
func (x *RefIndex) AddSourceError(source string) {
	x.failedSources = append(x.failedSources, source)
}

// SourceName maps a workload kind to the lowercase plural used in source
// notes. It is also used by the TUI's paginated reference collector.
func SourceName(kind string) string {
	switch kind {
	case KindDeployment:
		return "deployments"
	case KindStatefulSet:
		return "statefulsets"
	case KindDaemonSet:
		return "daemonsets"
	case KindJob:
		return "jobs"
	case KindCronJob:
		return "cronjobs"
	default:
		return "workloads"
	}
}

// WorkloadEntry is a workload and its aggregated direct and ServiceAccount
// chain references.
type WorkloadEntry struct {
	Workload Workload
	Refs     []ResourceRef // Direct references merged with the ServiceAccount chain.
}

// Workloads returns workload entries sorted by canonical kind order and name.
func (x *RefIndex) Workloads() []WorkloadEntry {
	entries := make([]WorkloadEntry, 0, len(x.workloads))
	for _, workload := range x.workloads {
		entries = append(entries, WorkloadEntry{
			Workload: workload.workload,
			Refs:     AggregateRefs(x.withServiceAccount(workload.refs, workload.saName)),
		})
	}
	slices.SortFunc(entries, func(a, b WorkloadEntry) int {
		return cmp.Or(cmp.Compare(workloadKindIndex(a.Workload.Kind), workloadKindIndex(b.Workload.Kind)), cmp.Compare(a.Workload.Name, b.Workload.Name))
	})
	return entries
}

// Orphans returns sorted Pod names and references not covered by an indexed
// workload. Pods with no references are omitted.
func (x *RefIndex) Orphans() ([]string, []ResourceRef) {
	var names []string
	var refs []Ref
	for _, pod := range x.pods {
		if x.podCovered(pod) {
			continue
		}
		podRefs := x.withServiceAccount(pod.refs, pod.saName)
		if len(podRefs) == 0 {
			continue
		}
		names = append(names, pod.name)
		refs = append(refs, podRefs...)
	}
	slices.Sort(names)
	return names, AggregateRefs(refs)
}

// Consumer identifies one consumer and its aggregated view of a resource.
type Consumer struct {
	Kind string // A workload kind, Pod, or ServiceAccount.
	Name string
	Ref  ResourceRef // This consumer's aggregated view of the queried resource.
}

// ConsumersOf returns matching workloads, orphan Pods, and ServiceAccounts in
// stable kind and name order.
func (x *RefIndex) ConsumersOf(kind, name string) []Consumer {
	consumers := make([]Consumer, 0)
	for _, workload := range x.Workloads() {
		if ref, ok := findResourceRef(workload.Refs, kind, name); ok {
			consumers = append(consumers, Consumer{Kind: workload.Workload.Kind, Name: workload.Workload.Name, Ref: ref})
		}
	}
	pods := make([]Consumer, 0)
	for _, pod := range x.pods {
		if x.podCovered(pod) {
			continue
		}
		if ref, ok := findResourceRef(AggregateRefs(x.withServiceAccount(pod.refs, pod.saName)), kind, name); ok {
			pods = append(pods, Consumer{Kind: KindPod, Name: pod.name, Ref: ref})
		}
	}
	slices.SortFunc(pods, func(a, b Consumer) int { return cmp.Compare(a.Name, b.Name) })
	consumers = append(consumers, pods...)
	serviceAccounts := make([]Consumer, 0)
	for _, serviceAccount := range x.serviceAccounts {
		if ref, ok := findResourceRef(AggregateRefs(ServiceAccountRefs(serviceAccount)), kind, name); ok {
			serviceAccounts = append(serviceAccounts, Consumer{Kind: KindServiceAccount, Name: serviceAccount.Name, Ref: ref})
		}
	}
	slices.SortFunc(serviceAccounts, func(a, b Consumer) int { return cmp.Compare(a.Name, b.Name) })
	return append(consumers, serviceAccounts...)
}

// podCovered reports whether a Pod's controller chain was successfully walked
// by AddWorkload. This index intentionally does not list ReplicaSets, so it
// recognizes Deployment-owned ReplicaSets by their pod-template-hash suffix.
func (x *RefIndex) podCovered(pod indexedPod) bool {
	switch pod.ownerKind {
	case KindStatefulSet, KindDaemonSet, KindJob:
		for _, workload := range x.workloads {
			if workload.workload.Kind == pod.ownerKind && workload.workload.Name == pod.ownerName {
				return true
			}
		}
	case "ReplicaSet":
		for _, workload := range x.workloads {
			if workload.workload.Kind != KindDeployment {
				continue
			}
			if suffix, ok := strings.CutPrefix(pod.ownerName, workload.workload.Name+"-"); ok && isPodTemplateHash(suffix) {
				return true
			}
		}
	}
	return false
}

func isPodTemplateHash(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("bcdfghjklmnpqrstvwxz2456789", char) {
			return false
		}
	}
	return true
}

// Missing reports known absence. It returns false when AddExisting has not
// established whether resources of kind could be listed.
func (x *RefIndex) Missing(kind, name string) bool {
	if !x.existenceKnown[kind] {
		return false
	}
	_, exists := x.existing[kind][name]
	return !exists
}

// Notes returns degraded-source notes in insertion order.
func (x *RefIndex) Notes() []string {
	notes := make([]string, 0, len(x.failedSources))
	for _, source := range x.failedSources {
		notes = append(notes, source+" not listable")
	}
	return notes
}

// FailedSources returns source names that could not be listed, in insertion
// order.
func (x *RefIndex) FailedSources() []string {
	return slices.Clone(x.failedSources)
}

func (x *RefIndex) withServiceAccount(refs []Ref, serviceAccountName string) []Ref {
	joined := slices.Clone(refs)
	if serviceAccount := x.serviceAccounts[serviceAccountName]; serviceAccount != nil {
		joined = append(joined, ServiceAccountRefs(serviceAccount)...)
	}
	return joined
}

func findResourceRef(refs []ResourceRef, kind, name string) (ResourceRef, bool) {
	for _, ref := range refs {
		if ref.Kind == kind && ref.Name == name {
			return ref, true
		}
	}
	return ResourceRef{}, false
}

func workloadKindIndex(kind string) int {
	for index, candidate := range WorkloadKinds {
		if kind == candidate {
			return index
		}
	}
	return len(WorkloadKinds)
}

// CollectNamespaceRefs lists all workload, Pod, and ServiceAccount pages into
// one index. Source failures degrade the index; context cancellation is the
// only returned error.
func (c *Client) CollectNamespaceRefs(ctx context.Context, namespace string) (*RefIndex, error) {
	index := NewRefIndex()
	wrapError := func(err error) error {
		return fmt.Errorf("collect references in namespace %q: %w", namespace, err)
	}
	collect := func(source string, listPage func(string) (string, error)) error {
		if err := collectPaged(ctx, index, source, listPage); err != nil {
			return wrapError(err)
		}
		return nil
	}
	for _, kind := range WorkloadKinds {
		err := collect(SourceName(kind), func(continueToken string) (string, error) {
			page, err := c.ListWorkloads(ctx, namespace, kind, DefaultPageSize, continueToken)
			if err != nil {
				return "", err
			}
			for _, workload := range page.Items {
				index.AddWorkload(workload)
			}
			return page.Continue, nil
		})
		if err != nil {
			return nil, err
		}
	}
	if err := collect("pods", func(continueToken string) (string, error) {
		page, err := c.ListPods(ctx, namespace, DefaultPageSize, continueToken)
		if err != nil {
			return "", err
		}
		for _, pod := range page.Items {
			index.AddPod(pod)
		}
		return page.Continue, nil
	}); err != nil {
		return nil, err
	}
	if err := collect("serviceaccounts", func(continueToken string) (string, error) {
		page, err := c.ListServiceAccounts(ctx, namespace, DefaultPageSize, continueToken)
		if err != nil {
			return "", err
		}
		for _, serviceAccount := range page.Items {
			index.AddServiceAccount(serviceAccount)
		}
		return page.Continue, nil
	}); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, wrapError(ctx.Err())
	}
	return index, nil
}

func collectPaged(ctx context.Context, index *RefIndex, source string, listPage func(string) (string, error)) error {
	continueToken := ""
	for {
		next, err := listPage(continueToken)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			index.AddSourceError(source)
			return nil
		}
		if next == "" {
			return nil
		}
		continueToken = next
	}
}
