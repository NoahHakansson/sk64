package tui

import (
	"errors"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/charmbracelet/x/ansi"
)

func TestSummarizeBlastRadius(t *testing.T) {
	index := k8s.NewRefIndex()
	index.AddWorkload(k8s.Workload{
		Kind:      k8s.KindDeployment,
		Name:      "web",
		Namespace: "default",
		Spec:      podSpecWithRef("db-creds", k8s.TagEnv),
	})
	index.AddWorkload(k8s.Workload{
		Kind:      k8s.KindStatefulSet,
		Name:      "database",
		Namespace: "default",
		Spec:      podSpecWithRef("db-creds", k8s.TagVolume),
	})
	index.AddWorkload(k8s.Workload{
		Kind:      k8s.KindDaemonSet,
		Name:      "agent",
		Namespace: "default",
		Spec:      podSpecWithResourceRef(k8s.KindSecret, "db-creds", k8s.TagVolume, true),
	})
	index.AddPod(podWithRef("operator-pod", "db-creds", k8s.TagVolume))
	index.AddSourceError("pods")

	summary := summarizeBlastRadius(index, k8s.KindSecret, "db-creds")

	if summary.workloads != 3 || summary.env != 1 || summary.pods != 1 || summary.serviceAccounts != 0 {
		t.Fatalf("summary counts = %+v", summary)
	}
	if got := summary.total(); got != 4 {
		t.Fatalf("total() = %d, want 4", got)
	}
	if got := summary.subject(); got != "3 workloads and 1 pod" {
		t.Fatalf("subject() = %q", got)
	}
	if got := summary.consumerList(); got != "Deployment/web, StatefulSet/database, DaemonSet/agent (+1 more)" {
		t.Fatalf("consumerList() = %q", got)
	}
	if got := summary.line(); got != "consumed by 3 workloads and 1 pod (1 via env)" {
		t.Fatalf("line() = %q", got)
	}
	if got := strings.Join(summary.notes, ", "); got != "pods not listable" {
		t.Fatalf("notes = %q", got)
	}
}

func TestBlastRadiusStatesAndPluralization(t *testing.T) {
	tests := []struct {
		name    string
		summary blastRadius
		line    string
		subject string
	}{
		{name: "unknown", line: "blast radius unavailable"},
		{name: "known empty", summary: blastRadius{known: true}, line: "no consumers found"},
		{name: "one workload one env", summary: blastRadius{known: true, workloads: 1, env: 1}, line: "consumed by 1 workload (1 via env)", subject: "1 workload"},
		{name: "one workload no env", summary: blastRadius{known: true, workloads: 1}, line: "consumed by 1 workload", subject: "1 workload"},
		{name: "two workloads one env", summary: blastRadius{known: true, workloads: 2, env: 1}, line: "consumed by 2 workloads (1 via env)", subject: "2 workloads"},
		{
			name:    "all singular",
			summary: blastRadius{known: true, workloads: 1, env: 1, pods: 1, serviceAccounts: 1},
			line:    "consumed by 1 workload, 1 pod and 1 serviceaccount (1 via env)",
			subject: "1 workload, 1 pod and 1 serviceaccount",
		},
		{
			name:    "all plural",
			summary: blastRadius{known: true, workloads: 2, pods: 2, serviceAccounts: 2},
			line:    "consumed by 2 workloads, 2 pods and 2 serviceaccounts",
			subject: "2 workloads, 2 pods and 2 serviceaccounts",
		},
		{name: "pods only", summary: blastRadius{known: true, pods: 1}, line: "consumed by 1 pod", subject: "1 pod"},
		{name: "notes only", summary: blastRadius{known: true, notes: []string{"pods not listable"}}, line: "no consumers found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.summary.line(); got != test.line {
				t.Fatalf("line() = %q, want %q", got, test.line)
			}
			if got := test.summary.subject(); got != test.subject {
				t.Fatalf("subject() = %q, want %q", got, test.subject)
			}
		})
	}
}

func TestBlastRadiusRenderLinePrioritizesIncompleteReason(t *testing.T) {
	tests := []struct {
		name         string
		summary      blastRadius
		width        int
		wantExact    string
		wantContains []string
		wantAbsent   string
	}{
		{
			name:      "no notes",
			summary:   blastRadius{known: true, workloads: 2, env: 1, pods: 1},
			width:     60,
			wantExact: "consumed by 2 workloads and 1 pod (1 via env)",
		},
		{
			name:         "one note",
			summary:      blastRadius{known: true, workloads: 2, env: 1, pods: 1, notes: []string{"pods not listable"}},
			width:        60,
			wantContains: []string{"[incomplete]", "pods not listable"},
			wantAbsent:   "2 workloads and 1 pod",
		},
		{
			name:         "several notes",
			summary:      blastRadius{known: true, workloads: 2, pods: 1, notes: []string{"pods not listable", "jobs not listable"}},
			width:        60,
			wantContains: []string{"[incomplete]", "pods not listable", "jobs not listable"},
			wantAbsent:   "consumed by 2 workloads",
		},
		{
			name:         "several long notes",
			summary:      blastRadius{known: true, workloads: 2, notes: []string{"serviceaccounts not listable", "statefulsets not listable"}},
			width:        60,
			wantContains: []string{"[incomplete]", "serviceaccounts not listable", "(+1 more)"},
			wantAbsent:   "statefulsets not listable",
		},
		{
			name: "many consumers plus notes",
			summary: blastRadius{
				known:           true,
				workloads:       125,
				env:             100,
				pods:            44,
				serviceAccounts: 12,
				notes:           []string{"pods not listable", "jobs not listable"},
			},
			width:        80,
			wantContains: []string{"[incomplete]", "pods not listable", "jobs not listable"},
			wantAbsent:   "125 workloads, 44 pods and 12 serviceaccounts",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := test.summary.renderLine(testStyles(true), test.width)
			if test.wantExact != "" && got != test.wantExact {
				t.Fatalf("renderLine() = %q, want %q", got, test.wantExact)
			}
			for _, want := range test.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("renderLine() = %q, want %q", got, want)
				}
			}
			if test.wantAbsent != "" && strings.Contains(got, test.wantAbsent) {
				t.Fatalf("renderLine() = %q, consumer counts did not yield before the caveat", got)
			}
			if gotWidth := lipgloss.Width(got); gotWidth > test.width {
				t.Fatalf("renderLine() width = %d, want <= %d: %q", gotWidth, test.width, got)
			}
		})
	}
}

func TestEditFlowBlastRadiusStateLines(t *testing.T) {
	tests := []struct {
		name    string
		pending bool
		err     error
		want    string
	}{
		{name: "pending", pending: true, want: "[loading] checking consumers"},
		{name: "unavailable", err: errors.New("scan failed"), want: "[unknown] blast radius unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flow := editFlow{dialog: dialog{styles: testStyles(true)}, radiusErr: test.err}
			flow.radiusLoader.pending = test.pending

			if got := ansi.Strip(flow.blastRadiusLine(80)); got != test.want {
				t.Fatalf("blastRadiusLine() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBlastRadiusConsumerListLimitsNames(t *testing.T) {
	summary := blastRadius{
		names: []string{"Deployment/one", "Deployment/two", "Pod/three", "ServiceAccount/four"},
	}
	if got := summary.consumerList(); got != "Deployment/one, Deployment/two, Pod/three (+1 more)" {
		t.Fatalf("consumerList() = %q", got)
	}
}
