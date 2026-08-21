package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
)

// blastRadius summarizes one namespace index's view of a single resource.
type blastRadius struct {
	known           bool
	workloads       int
	env             int
	pods            int
	serviceAccounts int
	names           []string
	notes           []string
}

func summarizeBlastRadius(index *k8s.RefIndex, kind, name string) blastRadius {
	if index == nil {
		return blastRadius{}
	}
	summary := blastRadius{known: true, notes: index.Notes()}
	for _, consumer := range index.ConsumersOf(kind, name) {
		summary.names = append(summary.names, consumer.Kind+"/"+consumer.Name)
		switch consumer.Kind {
		case k8s.KindPod:
			summary.pods++
		case k8s.KindServiceAccount:
			summary.serviceAccounts++
		default:
			summary.workloads++
			if slices.Contains(consumer.Ref.Tags, k8s.TagEnv) || slices.Contains(consumer.Ref.Tags, k8s.TagEnvFrom) {
				summary.env++
			}
		}
	}
	return summary
}

func (b blastRadius) line() string {
	if !b.known {
		return "blast radius unavailable"
	}
	line := "no consumers found"
	if subject := b.subject(); subject != "" {
		line = "consumed by " + subject
		if b.env > 0 {
			line += fmt.Sprintf(" (%d via env)", b.env)
		}
	}
	return line
}

func (b blastRadius) renderLine(st *styles, width int) string {
	line := b.line()
	if len(b.notes) == 0 {
		return line
	}
	reason := strings.Join(b.notes, ", ")
	protected := st.stateMarker(stateLineIncomplete) + st.glyphs.separator + reason
	if width > 0 && len(b.notes) > 1 && lipgloss.Width(protected) > width {
		reason = fmt.Sprintf("%s (+%d more)", b.notes[0], len(b.notes)-1)
	}
	return renderStateLine(st, stateLineIncomplete, line, reason, width)
}

func (b blastRadius) total() int {
	return b.workloads + b.pods + b.serviceAccounts
}

func (b blastRadius) subject() string {
	parts := make([]string, 0, 3)
	if b.workloads > 0 {
		parts = append(parts, plural(b.workloads, "workload"))
	}
	if b.pods > 0 {
		parts = append(parts, plural(b.pods, "pod"))
	}
	if b.serviceAccounts > 0 {
		parts = append(parts, plural(b.serviceAccounts, "serviceaccount"))
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

func (b blastRadius) consumerList() string {
	const visibleConsumers = 3
	names := b.names
	if len(names) > visibleConsumers {
		names = names[:visibleConsumers]
	}
	line := strings.Join(names, ", ")
	if hidden := len(b.names) - len(names); hidden > 0 {
		line += fmt.Sprintf(" (+%d more)", hidden)
	}
	return line
}
