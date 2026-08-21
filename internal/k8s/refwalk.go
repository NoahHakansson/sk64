package k8s

import (
	"cmp"
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/NoahHakansson/sk64/internal/natsort"
)

// RefTag classifies how a pod spec or ServiceAccount references a resource.
type RefTag string

const (
	// TagEnv marks a specific key referenced by an environment variable.
	TagEnv RefTag = "env"
	// TagEnvFrom marks a whole resource referenced by an environment source.
	TagEnvFrom RefTag = "envFrom"
	// TagVolume marks a resource mounted through a volume.
	TagVolume RefTag = "volume"
	// TagProjected marks a resource mounted through a projected volume.
	TagProjected RefTag = "projected"
	// TagPull marks a Secret used to pull container images.
	TagPull RefTag = "pull"
	// TagSA marks a Secret referenced through a ServiceAccount.
	TagSA RefTag = "sa"
)

var refTagOrder = []RefTag{TagEnv, TagEnvFrom, TagVolume, TagProjected, TagPull, TagSA}

// Ref describes one raw Secret or ConfigMap reference occurrence.
type Ref struct {
	Kind    string // KindSecret or KindConfigMap.
	Name    string
	Key     string // Specific key for TagEnv; empty for whole-resource references.
	Tag     RefTag
	SubPath bool // Whether a volume mount uses subPath or subPathExpr.
}

// ResourceRef aggregates every occurrence that points at one resource.
type ResourceRef struct {
	Kind          string
	Name          string
	Tags          []RefTag // Unique tags in canonical display order.
	Keys          []string // Sorted union of TagEnv keys.
	SubPath       bool     // Whether any volume occurrence uses subPath.
	RolloutNeeded bool     // Whether environment or subPath use requires a restart to propagate changes.
}

// WalkPodSpec extracts Secret and ConfigMap references from every supported
// location in spec. The returned service-account name defaults to "default";
// callers resolve that chain separately with ServiceAccountRefs.
func WalkPodSpec(spec *corev1.PodSpec) ([]Ref, string) {
	if spec == nil {
		return nil, "default"
	}
	var refs []Ref
	volumeRefs := make(map[string][]int)
	appendEnvironmentRefs := func(env []corev1.EnvVar, envFrom []corev1.EnvFromSource) {
		for _, variable := range env {
			if variable.ValueFrom == nil {
				continue
			}
			if selector := variable.ValueFrom.SecretKeyRef; selector != nil && selector.Name != "" {
				refs = append(refs, Ref{Kind: KindSecret, Name: selector.Name, Key: selector.Key, Tag: TagEnv})
			}
			if selector := variable.ValueFrom.ConfigMapKeyRef; selector != nil && selector.Name != "" {
				refs = append(refs, Ref{Kind: KindConfigMap, Name: selector.Name, Key: selector.Key, Tag: TagEnv})
			}
		}
		for _, source := range envFrom {
			if source.SecretRef != nil && source.SecretRef.Name != "" {
				refs = append(refs, Ref{Kind: KindSecret, Name: source.SecretRef.Name, Tag: TagEnvFrom})
			}
			if source.ConfigMapRef != nil && source.ConfigMapRef.Name != "" {
				refs = append(refs, Ref{Kind: KindConfigMap, Name: source.ConfigMapRef.Name, Tag: TagEnvFrom})
			}
		}
	}
	for _, container := range spec.Containers {
		appendEnvironmentRefs(container.Env, container.EnvFrom)
	}
	for _, container := range spec.InitContainers {
		appendEnvironmentRefs(container.Env, container.EnvFrom)
	}
	for _, container := range spec.EphemeralContainers {
		appendEnvironmentRefs(container.Env, container.EnvFrom)
	}

	for _, volume := range spec.Volumes {
		if volume.Secret != nil && volume.Secret.SecretName != "" {
			refs = append(refs, Ref{Kind: KindSecret, Name: volume.Secret.SecretName, Tag: TagVolume})
			volumeRefs[volume.Name] = append(volumeRefs[volume.Name], len(refs)-1)
		}
		if volume.ConfigMap != nil && volume.ConfigMap.Name != "" {
			refs = append(refs, Ref{Kind: KindConfigMap, Name: volume.ConfigMap.Name, Tag: TagVolume})
			volumeRefs[volume.Name] = append(volumeRefs[volume.Name], len(refs)-1)
		}
		if volume.CSI != nil && volume.CSI.NodePublishSecretRef != nil && volume.CSI.NodePublishSecretRef.Name != "" {
			refs = append(refs, Ref{Kind: KindSecret, Name: volume.CSI.NodePublishSecretRef.Name, Tag: TagVolume})
			volumeRefs[volume.Name] = append(volumeRefs[volume.Name], len(refs)-1)
		}
		if volume.Projected == nil {
			continue
		}
		for _, source := range volume.Projected.Sources {
			if source.Secret != nil && source.Secret.Name != "" {
				refs = append(refs, Ref{Kind: KindSecret, Name: source.Secret.Name, Tag: TagProjected})
				volumeRefs[volume.Name] = append(volumeRefs[volume.Name], len(refs)-1)
			}
			if source.ConfigMap != nil && source.ConfigMap.Name != "" {
				refs = append(refs, Ref{Kind: KindConfigMap, Name: source.ConfigMap.Name, Tag: TagProjected})
				volumeRefs[volume.Name] = append(volumeRefs[volume.Name], len(refs)-1)
			}
		}
	}
	markSubPath := func(mounts []corev1.VolumeMount) {
		for _, mount := range mounts {
			if mount.SubPath == "" && mount.SubPathExpr == "" {
				continue
			}
			for _, index := range volumeRefs[mount.Name] {
				refs[index].SubPath = true
			}
		}
	}
	for _, container := range spec.Containers {
		markSubPath(container.VolumeMounts)
	}
	for _, container := range spec.InitContainers {
		markSubPath(container.VolumeMounts)
	}
	for _, container := range spec.EphemeralContainers {
		markSubPath(container.VolumeMounts)
	}

	for _, secret := range spec.ImagePullSecrets {
		if secret.Name != "" {
			refs = append(refs, Ref{Kind: KindSecret, Name: secret.Name, Tag: TagPull})
		}
	}
	saName := spec.ServiceAccountName
	if saName == "" {
		saName = "default"
	}
	return refs, saName
}

// ServiceAccountRefs returns Secret references carried by a ServiceAccount.
func ServiceAccountRefs(sa *corev1.ServiceAccount) []Ref {
	if sa == nil {
		return nil
	}
	refs := make([]Ref, 0, len(sa.Secrets)+len(sa.ImagePullSecrets))
	for _, secret := range sa.Secrets {
		if secret.Name != "" {
			refs = append(refs, Ref{Kind: KindSecret, Name: secret.Name, Tag: TagSA})
		}
	}
	for _, secret := range sa.ImagePullSecrets {
		if secret.Name != "" {
			refs = append(refs, Ref{Kind: KindSecret, Name: secret.Name, Tag: TagSA})
		}
	}
	return refs
}

// AggregateRefs merges raw occurrences by resource and returns them sorted by
// kind and name with canonical tag and key ordering.
func AggregateRefs(refs []Ref) []ResourceRef {
	type aggregate struct {
		ref  ResourceRef
		tags map[RefTag]struct{}
		keys map[string]struct{}
	}
	type resourceKey struct {
		kind string
		name string
	}
	byResource := make(map[resourceKey]*aggregate)
	for _, ref := range refs {
		if ref.Name == "" || ref.Kind == "" {
			continue
		}
		key := resourceKey{kind: ref.Kind, name: ref.Name}
		entry := byResource[key]
		if entry == nil {
			entry = &aggregate{
				ref:  ResourceRef{Kind: ref.Kind, Name: ref.Name},
				tags: make(map[RefTag]struct{}),
				keys: make(map[string]struct{}),
			}
			byResource[key] = entry
		}
		entry.tags[ref.Tag] = struct{}{}
		if ref.Tag == TagEnv && ref.Key != "" {
			entry.keys[ref.Key] = struct{}{}
		}
		entry.ref.SubPath = entry.ref.SubPath || ref.SubPath
		entry.ref.RolloutNeeded = entry.ref.RolloutNeeded || ref.Tag == TagEnv || ref.Tag == TagEnvFrom || ref.SubPath
	}
	result := make([]ResourceRef, 0, len(byResource))
	for _, entry := range byResource {
		for _, tag := range refTagOrder {
			if _, ok := entry.tags[tag]; ok {
				entry.ref.Tags = append(entry.ref.Tags, tag)
			}
		}
		entry.ref.Keys = slices.SortedFunc(maps.Keys(entry.keys), natsort.Compare)
		result = append(result, entry.ref)
	}
	slices.SortFunc(result, func(a, b ResourceRef) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name))
	})
	return result
}
