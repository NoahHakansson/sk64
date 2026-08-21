package project

import (
	"bytes"
	"strings"

	"github.com/NoahHakansson/sk64/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

type yamlDoc struct {
	content []byte
	line    int
}

func splitDocs(data []byte) []yamlDoc {
	lines := bytes.Split(data, []byte("\n"))
	start := 0
	docs := make([]yamlDoc, 0, 1)
	appendDoc := func(end int) {
		for start < end && len(bytes.TrimSpace(lines[start])) == 0 {
			start++
		}
		if start < end {
			docs = append(docs, yamlDoc{content: bytes.Join(lines[start:end], []byte("\n")), line: start + 1})
		}
	}
	for i, line := range lines {
		if string(bytes.TrimRight(line, " \t\r")) == "---" {
			appendDoc(i)
			start = i + 1
		}
	}
	appendDoc(len(lines))
	return docs
}

func extractManifest(relPath string, data []byte) []Suggestion {
	var suggestions []Suggestion
	for _, doc := range splitDocs(data) {
		suggestions = append(suggestions, suggestionsFromDoc(relPath, doc.line, ModeManifest, "", doc.content)...)
	}
	return suggestions
}

func suggestionsFromDoc(relPath string, line int, mode RenderMode, detail string, doc []byte) []Suggestion {
	var head struct {
		Kind     string `json:"kind"`
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if err := yaml.Unmarshal(doc, &head); err != nil || head.Kind == "" || templated(head.Metadata.Name) || templated(head.Metadata.Namespace) {
		return nil
	}
	base := Suggestion{File: relPath, Line: line, Mode: mode, Detail: detail}
	if head.Kind == KindNamespace {
		if head.Metadata.Name == "" {
			return nil
		}
		base.Kind, base.Name = KindNamespace, head.Metadata.Name
		return []Suggestion{base}
	}
	var object any
	switch head.Kind {
	case k8s.KindDeployment:
		object = &appsv1.Deployment{}
	case k8s.KindStatefulSet:
		object = &appsv1.StatefulSet{}
	case k8s.KindDaemonSet:
		object = &appsv1.DaemonSet{}
	case k8s.KindJob:
		object = &batchv1.Job{}
	case k8s.KindCronJob:
		object = &batchv1.CronJob{}
	case k8s.KindPod:
		object = &corev1.Pod{}
	default:
		return nil
	}
	if err := yaml.Unmarshal(doc, object); err != nil {
		return nil
	}
	var spec corev1.PodSpec
	workload := head.Kind != k8s.KindPod
	if pod, ok := object.(*corev1.Pod); ok {
		spec = pod.Spec
	} else {
		var ok bool
		spec, ok = k8s.PodSpecOf(object)
		if !ok {
			return nil
		}
	}
	suggestions := make([]Suggestion, 0, 3)
	if head.Metadata.Namespace != "" {
		ns := base
		ns.Kind, ns.Name = KindNamespace, head.Metadata.Namespace
		suggestions = append(suggestions, ns)
	}
	if workload && head.Metadata.Name != "" {
		item := base
		item.Kind, item.Name, item.Namespace = head.Kind, head.Metadata.Name, head.Metadata.Namespace
		suggestions = append(suggestions, item)
	}
	refs, _ := k8s.WalkPodSpec(&spec)
	for _, ref := range k8s.AggregateRefs(refs) {
		if templated(ref.Name) {
			continue
		}
		item := base
		item.Kind, item.Name, item.Namespace = ref.Kind, ref.Name, head.Metadata.Namespace
		suggestions = append(suggestions, item)
	}
	return suggestions
}

func templated(value string) bool {
	return strings.Contains(value, "{{")
}
