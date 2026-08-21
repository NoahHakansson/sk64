package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type refsFixture struct {
	workloads map[string][]k8s.Workload
	pods      []*corev1.Pod
	sas       []*corev1.ServiceAccount
	resources []k8s.Resource
	errors    map[string]error
}

func TestWorkloadItemReadinessStyles(t *testing.T) {
	tests := []struct {
		name, kind, ready string
		style             func(*styles) string
	}{
		{name: "fully ready", kind: k8s.KindDeployment, ready: "2/2 ready", style: func(st *styles) string { return st.successText.Render("2/2 ready") }},
		{name: "zero desired ready", kind: k8s.KindDeployment, ready: "0/0 ready", style: func(st *styles) string { return st.successText.Render("0/0 ready") }},
		{name: "partially ready", kind: k8s.KindStatefulSet, ready: "1/3 ready", style: func(st *styles) string { return st.warnText.Render("1/3 ready") }},
		{name: "zero ready", kind: k8s.KindDaemonSet, ready: "0/3 ready", style: func(st *styles) string { return st.errText.Render("0/3 ready") }},
		{name: "job phase", kind: k8s.KindJob, ready: "failed", style: func(*styles) string { return "failed" }},
	}
	for _, ascii := range []bool{true, false} {
		st := testStyles(ascii)
		for _, test := range tests {
			t.Run(fmt.Sprintf("ascii=%t/%s", ascii, test.name), func(t *testing.T) {
				item := workloadItem{entry: k8s.WorkloadEntry{Workload: k8s.Workload{Kind: test.kind, Name: "worker", Ready: test.ready}}, styles: st}
				_, columns := item.listColumns()
				if got, want := columns[0].text, test.style(st); got != want {
					t.Fatalf("readiness = %q, want %q", got, want)
				}
			})
		}
		t.Run(fmt.Sprintf("ascii=%t/cron", ascii), func(t *testing.T) {
			item := workloadItem{entry: k8s.WorkloadEntry{Workload: k8s.Workload{Kind: k8s.KindCronJob, Name: "backup", Ready: "0 2 * * *"}}, styles: st}
			_, columns := item.listColumns()
			want := st.dim.Render(st.glyphs.cronMarker + " 0 2 * * *")
			if columns[0].text != want {
				t.Fatalf("cron readiness = %q, want %q", columns[0].text, want)
			}
		})
	}
}

func TestWorkloadKindColumnsAlignAndOrphansNormalize(t *testing.T) {
	h := workloadAppearanceHarness(t, true)
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*workloadScreen)
	view := screen.list.View()
	deployment := lineContaining(t, view, "Deployment")
	cron := lineContaining(t, view, "CronJob")
	orphan := lineContaining(t, view, "Pods")

	nameColumns := []int{
		strings.Index(deployment, "web"),
		strings.Index(cron, "backup"),
		strings.Index(orphan, "orphaned"),
	}
	if nameColumns[0] < 0 || nameColumns[0] != nameColumns[1] || nameColumns[0] != nameColumns[2] {
		t.Fatalf("workload identity columns = %v\n%s", nameColumns, ansi.Strip(view))
	}
	if !strings.Contains(orphan, "Pods") || !strings.Contains(orphan, "orphaned (1)") {
		t.Fatalf("orphan row = %q, want Pods and orphaned count", orphan)
	}
}

func TestWorkloadStatusCountExcludesOrphanRow(t *testing.T) {
	h := workloadAppearanceHarness(t, true)
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*workloadScreen)
	if got := screen.statusRow(); got != "3 workloads" {
		t.Fatalf("workload status = %q, want workload-only count", got)
	}
}

func TestConsumersSpinnerTicksOnlyWhilePending(t *testing.T) {
	screen := newConsumersScreen(t.Context(), testClient(), k8s.KindSecret, "default", "credentials", editEnv{}, testStyles(false))
	tick := screen.spinner.Tick().(spinner.TickMsg)
	if _, cmd := screen.Update(tick); cmd != nil {
		t.Fatalf("idle spinner returned command %v", cmd)
	}
	screen.pending = true
	if _, cmd := screen.Update(tick); cmd == nil {
		t.Fatal("pending spinner returned no follow-up command")
	}
}

func feedRefs(h *harness, reqID int, fixture refsFixture) {
	h.t.Helper()
	for _, kind := range k8s.WorkloadKinds {
		source := k8s.SourceName(kind)
		h.send(refsPageMsg{reqID: reqID, source: source, workloads: k8s.WorkloadPage{Items: fixture.workloads[kind]}, err: fixture.errors[source]})
	}
	h.send(
		refsPageMsg{reqID: reqID, source: "pods", pods: k8s.PodPage{Items: fixture.pods}, err: fixture.errors["pods"]},
		refsPageMsg{reqID: reqID, source: "serviceaccounts", sas: k8s.ServiceAccountPage{Items: fixture.sas}, err: fixture.errors["serviceaccounts"]},
	)
	if _, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*workloadScreen); !ok {
		return
	}
	for _, kind := range []string{k8s.KindSecret, k8s.KindConfigMap} {
		source := resourceSource(kind)
		items := make([]k8s.Resource, 0)
		for _, resource := range fixture.resources {
			if resource.Kind() == kind {
				items = append(items, resource)
			}
		}
		h.send(refsPageMsg{reqID: reqID, source: source, resources: k8s.ResourcePage{Items: items}, err: fixture.errors[source]})
	}
}

func TestGolden_WorkloadView(t *testing.T) {
	workloadAppearanceHarness(t, true).golden("workload_view")
}

func TestGolden_WorkloadViewUnicode(t *testing.T) {
	workloadAppearanceHarness(t, false).golden("workload_view_unicode")
}

func workloadAppearanceHarness(t *testing.T, ascii bool) *harness {
	t.Helper()
	h := workloadHarnessOptions(t, ascii)
	fixture := refsFixture{
		workloads: map[string][]k8s.Workload{
			k8s.KindDeployment:  {workloadWithRef(k8s.KindDeployment, "web", "2/3 ready", "web-secret", k8s.TagEnv)},
			k8s.KindStatefulSet: {workloadWithRef(k8s.KindStatefulSet, "database", "1/1 ready", "db-secret", k8s.TagVolume)},
			k8s.KindCronJob:     {workloadWithRef(k8s.KindCronJob, "backup", "0 2 * * *", "pull-secret", k8s.TagPull)},
		},
		pods: []*corev1.Pod{podWithRef("operator-pod", "pod-secret", k8s.TagVolume)},
		resources: []k8s.Resource{
			secretResource("web-secret"), secretResource("db-secret"), secretResource("pull-secret"), secretResource("pod-secret"),
		},
	}
	feedRefs(h, h.topReqID(), fixture)
	return h
}

func TestGolden_WorkloadViewNoAccess(t *testing.T) {
	h := workloadHarness(t)
	fixture := refsFixture{
		workloads: map[string][]k8s.Workload{
			k8s.KindStatefulSet: {workloadWithRef(k8s.KindStatefulSet, "database", "1/1 ready", "db-secret", k8s.TagVolume)},
			k8s.KindCronJob:     {workloadWithRef(k8s.KindCronJob, "backup", "0 2 * * *", "pull-secret", k8s.TagPull)},
		},
		resources: []k8s.Resource{secretResource("db-secret"), secretResource("pull-secret")},
		errors: map[string]error{
			"deployments": errors.New("forbidden"),
			"pods":        errors.New("forbidden"),
		},
	}
	feedRefs(h, h.topReqID(), fixture)
	h.golden("workload_view_no_access")
}

func TestGolden_WorkloadRefs(t *testing.T) {
	workloadRefsAppearanceHarness(t, true).golden("workload_refs")
}

func TestGolden_WorkloadRefsUnicode(t *testing.T) {
	workloadRefsAppearanceHarness(t, false).golden("workload_refs_unicode")
}

func TestGolden_WorkloadRefs60(t *testing.T) {
	longName := strings.Repeat("long-reference-name-", 4) + "tail"
	h := workloadHarness(t)
	feedRefs(h, h.topReqID(), refsFixture{
		workloads: map[string][]k8s.Workload{
			k8s.KindDeployment: {{
				Kind:      k8s.KindDeployment,
				Name:      "web",
				Namespace: "default",
				Ready:     "1/1 ready",
				Spec:      podSpecWithResourceRef(k8s.KindSecret, longName, k8s.TagVolume, true),
			}},
		},
	})
	h.keys("enter")
	h.send(tea.WindowSizeMsg{Width: 60, Height: 18})
	h.golden("workload_refs_60")
}

func TestGolden_ConsumersView(t *testing.T) {
	consumersAppearanceHarness(t, true).golden("consumers_view")
}

func TestGolden_ConsumersViewUnicode(t *testing.T) {
	consumersAppearanceHarness(t, false).golden("consumers_view_unicode")
}

func TestGolden_ConsumersViewIncomplete(t *testing.T) {
	consumersIncompleteHarness(t, true).golden("consumers_view_incomplete")
}

func TestGolden_ConsumersViewIncompleteUnicode(t *testing.T) {
	consumersIncompleteHarness(t, false).golden("consumers_view_incomplete_unicode")
}

func consumersIncompleteHarness(t *testing.T, ascii bool) *harness {
	t.Helper()
	h := consumersHarnessOptions(t, k8s.KindSecret, "shared", ascii)
	fixture := refsFixture{
		workloads: map[string][]k8s.Workload{
			k8s.KindDeployment: {workloadWithRef(k8s.KindDeployment, "web", "1/1 ready", "shared", k8s.TagEnv)},
		},
		errors: map[string]error{"pods": errors.New("forbidden")},
	}
	feedRefs(h, h.topReqID(), fixture)
	view := ansi.Strip(h.m.(app).stack[len(h.m.(app).stack)-1].View())
	subject := "Consumers of Secret default/shared"
	status := h.m.(app).styles.stateMarker(stateLineIncomplete) + " scan incomplete: pods not listable"
	if subjectIndex, statusIndex := strings.Index(view, subject), strings.Index(view, status); subjectIndex < 0 || statusIndex < subjectIndex {
		t.Fatalf("incomplete status did not render beneath subject:\n%s", view)
	}
	return h
}

func TestConsumersIncompleteStatusFollowsSubject(t *testing.T) {
	h := consumersHarness(t, k8s.KindSecret, "shared")
	feedRefs(h, h.topReqID(), refsFixture{
		workloads: map[string][]k8s.Workload{
			k8s.KindDeployment: {workloadWithRef(k8s.KindDeployment, "web", "1/1 ready", "shared", k8s.TagEnv)},
		},
		errors: map[string]error{"pods": errors.New("forbidden")},
	})
	view := ansi.Strip(h.m.(app).stack[len(h.m.(app).stack)-1].View())
	subject := "Consumers of Secret default/shared"
	status := "[incomplete] scan incomplete: pods not listable"
	if subjectIndex, statusIndex := strings.Index(view, subject), strings.Index(view, status); subjectIndex < 0 || statusIndex < subjectIndex {
		t.Fatalf("incomplete status did not render beneath subject:\n%s", view)
	}
}

func TestReferenceScanCancellationRendersUnknown(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) *harness
	}{
		{name: "workloads", setup: workloadHarness},
		{name: "consumers", setup: func(t *testing.T) *harness {
			t.Helper()
			return consumersHarness(t, k8s.KindSecret, "shared")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := test.setup(t)
			reqID := h.topReqID()
			h.send(refsPageMsg{
				reqID:  reqID,
				source: "deployments",
				workloads: k8s.WorkloadPage{
					Items:    []k8s.Workload{workloadWithRef(k8s.KindDeployment, "partial", "1/1 ready", "shared", k8s.TagEnv)},
					Continue: "next",
				},
			})
			h.keys("esc")

			view := ansi.Strip(h.m.(app).stack[len(h.m.(app).stack)-1].View())
			if !strings.Contains(view, "[unknown]") || !strings.Contains(view, "cancelled") || !strings.Contains(view, "ctrl+r to retry") {
				t.Fatalf("cancelled scan did not render actionable unknown state:\n%s", view)
			}
			if strings.Contains(view, "No items.") || strings.Contains(view, "no consumers found") || strings.Contains(view, "[empty]") {
				t.Fatalf("cancelled scan claimed an empty result:\n%s", view)
			}
			h.keys("ctrl+r")
			view = ansi.Strip(h.m.(app).stack[len(h.m.(app).stack)-1].View())
			if !strings.Contains(view, "[loading]") || strings.Contains(view, "[unknown]") {
				t.Fatalf("retry did not replace unknown with loading:\n%s", view)
			}

			h.send(refsPageMsg{reqID: h.topReqID(), source: "deployments", err: context.Canceled})
			view = ansi.Strip(h.m.(app).stack[len(h.m.(app).stack)-1].View())
			if !strings.Contains(view, "[unknown]") || strings.Contains(view, "[incomplete]") || strings.Contains(view, "[empty]") {
				t.Fatalf("cancelled scan result did not render unknown state:\n%s", view)
			}
		})
	}
}

func TestGolden_RolloutChecklist(t *testing.T) {
	h, _, _ := rolloutOfferHarnessOptions(t, true)
	h.golden("rollout_checklist")
}

func TestGolden_RolloutChecklistUnicode(t *testing.T) {
	h, _, _ := rolloutOfferHarnessOptions(t, false)
	h.golden("rollout_checklist_unicode")
}

func TestGolden_RolloutChecklistIncomplete(t *testing.T) {
	h, _, _ := incompleteRolloutOfferHarness(t)
	h.send(tea.WindowSizeMsg{Width: 60, Height: 15})
	h.golden("rollout_checklist_incomplete")
}

func TestGolden_RolloutSkip(t *testing.T) {
	h, flow, keyScreen := rolloutOfferHarness(t)
	h.keys("esc")
	h.send(resourceLoadedMsg{reqID: keyScreen.reqID, res: flow.res})
	h.golden("rollout_skipped")
}

func TestGolden_SavedCompletionWithoutRestart(t *testing.T) {
	resource := editSecret("10", []byte("old"))
	h := keyHarness(t, resource)
	keyScreen := topKeyScreen(t, h)
	flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, "DB_PASSWORD", []byte("new"), h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	enterSaving(h)
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, index: k8s.NewRefIndex()})
	h.golden("saved_nothing_to_restart")

	h.keys("enter")
	h.send(resourceLoadedMsg{reqID: keyScreen.reqID, res: flow.res})
	h.golden("saved_complete")
}

func TestGolden_RolloutDone(t *testing.T) {
	h, flow, _ := rolloutOfferHarness(t)
	h.keys("R")
	passCommitGate(h)
	failure := `statefulsets.apps "database" is forbidden: cannot patch`
	h.send(rolloutDoneMsg{reqID: flow.reqID, results: []rolloutResult{
		{kind: k8s.KindDeployment, name: "web"},
		{kind: k8s.KindStatefulSet, name: "database", err: errors.New(failure)},
	}})
	content := strings.Join(strings.Fields(ansi.Strip(flow.viewport.GetContent())), " ")
	if !strings.Contains(content, failure) {
		t.Fatalf("rollout failure reason was clipped:\n%s", content)
	}
	h.golden("rollout_done")
}

func TestGolden_RolloutDoneMinimumManyResults(t *testing.T) {
	h, flow, _ := rolloutOfferHarness(t)
	h.send(tea.WindowSizeMsg{Width: 60, Height: 15})
	h.keys("R")
	passCommitGate(h)
	h.send(rolloutDoneMsg{reqID: flow.reqID, results: manyRolloutResults()})
	view := ansi.Strip(h.view())
	for _, want := range []string{
		"Rollout results  1-2 of 12 shown",
		"10 restarted, 2 failed",
		"[error] Deployment/payments  failed: forbidden",
		"[error] StatefulSet/database  failed: timed out",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("minimum rollout results lost %q:\n%s", want, view)
		}
	}
	h.golden("rollout_done_min_many_results")
}

func TestRolloutDoneKeepsFailuresScrollable(t *testing.T) {
	h, flow, _ := rolloutOfferHarness(t)
	h.send(tea.WindowSizeMsg{Width: 60, Height: 15})
	h.keys("R")
	passCommitGate(h)
	results := manyRolloutResults()
	secretValue := "rollout-result-secret-value-must-not-render"
	if err := flow.res.Set("DB_PASSWORD", []byte(secretValue)); err != nil {
		t.Fatalf("set secret value fixture: %v", err)
	}
	h.send(rolloutDoneMsg{reqID: flow.reqID, results: results})

	summary := "10 restarted, 2 failed"
	failures := map[string]bool{
		k8s.KindDeployment + "/payments":  false,
		k8s.KindStatefulSet + "/database": false,
	}
	for step := 0; step <= flow.viewport.TotalLineCount()+flow.viewport.Height(); step++ {
		view := ansi.Strip(h.view())
		if !strings.Contains(view, summary) {
			t.Fatalf("scroll frame %d lost summary %q:\n%s", step, summary, view)
		}
		if strings.Contains(view, "...and ") {
			t.Fatalf("scroll frame %d replaced results with a counted cue:\n%s", step, view)
		}
		for failure := range failures {
			if strings.Contains(view, failure) {
				failures[failure] = true
			}
		}
		h.keys("down")
	}
	for failure, seen := range failures {
		if !seen {
			t.Fatalf("failure %q was not reachable by scrolling:\n%s", failure, ansi.Strip(flow.viewport.GetContent()))
		}
	}

	content := ansi.Strip(flow.viewport.GetContent())
	if strings.Contains(content, secretValue) {
		t.Fatalf("rollout results contain secret value bytes: %q", content)
	}
	for lineNumber, line := range strings.Split(content, "\n") {
		if width := ansi.StringWidth(line); width > flow.contentWidth() {
			t.Fatalf("result line %d width = %d, want <= %d: %q", lineNumber+1, width, flow.contentWidth(), line)
		}
	}
	for _, result := range results {
		if result.err == nil {
			continue
		}
		normalized := strings.Join(strings.Fields(content), " ")
		if !strings.Contains(normalized, result.err.Error()) {
			t.Fatalf("failure for %s/%s was clipped:\n%s", result.kind, result.name, content)
		}
	}
}

func manyRolloutResults() []rolloutResult {
	results := make([]rolloutResult, 0, 12)
	for i := 1; i <= 10; i++ {
		results = append(results, rolloutResult{kind: k8s.KindDeployment, name: fmt.Sprintf("worker-%02d", i)})
	}
	results = append(results,
		rolloutResult{kind: k8s.KindDeployment, name: "payments", err: errors.New("forbidden")},
		rolloutResult{kind: k8s.KindStatefulSet, name: "database", err: errors.New("timed out")},
	)
	return results
}

func TestWorkloadKeyOpensView(t *testing.T) {
	h := namespaceHarness(t)
	h.keys("w")
	workload, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*workloadScreen)
	if !ok || workload.namespace != "default" {
		t.Fatalf("workload key opened %T for namespace %q", h.m.(app).stack[len(h.m.(app).stack)-1], workload.namespace)
	}
	fixture := refsFixture{
		workloads: map[string][]k8s.Workload{k8s.KindDeployment: {{
			Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Ready: "1/1 ready",
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "web", EnvFrom: []corev1.EnvFromSource{
				{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "exists"}}},
				{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing"}}},
			}}}},
		}}},
		resources: []k8s.Resource{secretResource("exists")},
	}
	feedRefs(h, h.topReqID(), fixture)
	h.keys("enter")
	refs, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*workloadRefsScreen)
	if !ok {
		t.Fatalf("workload enter opened %T", h.m.(app).stack[len(h.m.(app).stack)-1])
	}
	h.keys("enter")
	keyScreen, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*keyScreen)
	if !ok || keyScreen.kind != k8s.KindSecret || keyScreen.namespace != "default" || keyScreen.name != "exists" {
		t.Fatalf("reference enter opened %#v", keyScreen)
	}
	h.send(resourceLoadedMsg{reqID: h.topReqID(), res: secretResource("exists")})
	h.keys("esc", "down")
	depth := len(h.m.(app).stack)
	h.keys("enter")
	if len(h.m.(app).stack) != depth || refs.list.SelectedItem().(refItem).row.ref.Name != "missing" {
		t.Fatal("missing reference was navigable")
	}
}

func TestConsumersKey(t *testing.T) {
	t.Run("resource screen", func(t *testing.T) {
		h := resourceHarness(t, true)
		h.keys("r")
		consumer, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*consumersScreen)
		if !ok || consumer.name != "app-credentials" || consumer.kind != k8s.KindSecret {
			t.Fatalf("resource r opened %#v", consumer)
		}
	})
	for _, readOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "key screen", true: "read-only key screen"}[readOnly], func(t *testing.T) {
			h := keyHarnessOptions(t, navigationSecret(), Options{StartNamespace: "default", ASCII: true, ReadOnly: readOnly})
			h.keys("r")
			consumer, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*consumersScreen)
			if !ok || consumer.name != "app-credentials" || consumer.kind != k8s.KindSecret {
				t.Fatalf("key r opened %#v", consumer)
			}
		})
	}
}

func TestRefsCollectorStaleAndChain(t *testing.T) {
	client := &k8s.Client{Clientset: fake.NewClientset(), Context: "test", Namespace: "default"}
	collector := newRefsCollector(t.Context(), client, "default", false)
	collector.startCollect()
	reqID := collector.reqID
	if cmd, done := collector.handleRefsPage(refsPageMsg{reqID: reqID - 1, source: "deployments"}); cmd != nil || done || collector.pendingSrc != 7 {
		t.Fatal("stale page changed collector")
	}
	cmd, done := collector.handleRefsPage(refsPageMsg{reqID: reqID, source: "pods", pods: k8s.PodPage{Continue: "next"}})
	if cmd == nil || done || collector.pendingSrc != 7 {
		t.Fatal("continued page finished its source")
	}
	_ = cmd()
	assertLastListContinueToken(t, collector.client, "next")
	if cmd, done = collector.handleRefsPage(refsPageMsg{reqID: reqID, source: "pods"}); cmd != nil || done || collector.pendingSrc != 6 {
		t.Fatal("final page did not finish its source")
	}
	sources := []string{"deployments", "statefulsets", "daemonsets", "jobs", "cronjobs", "serviceaccounts"}
	for i, source := range sources {
		_, done = collector.handleRefsPage(refsPageMsg{reqID: reqID, source: source, err: errors.New("denied")})
		if done != (i == len(sources)-1) {
			t.Fatalf("done after source %q = %t", source, done)
		}
	}
	if collector.pending || collector.pendingSrc != 0 {
		t.Fatalf("collector final pending state = %t sources %d", collector.pending, collector.pendingSrc)
	}
}

func workloadRefsAppearanceHarness(t *testing.T, ascii bool) *harness {
	t.Helper()
	h := workloadHarnessOptions(t, ascii)
	workload := k8s.Workload{Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Ready: "1/1 ready", Spec: comprehensivePodSpec()}
	feedRefs(h, h.topReqID(), refsFixture{
		workloads: map[string][]k8s.Workload{k8s.KindDeployment: {workload}},
		sas: []*corev1.ServiceAccount{{
			ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
			Secrets:    []corev1.ObjectReference{{Name: "sa-secret"}},
		}},
		resources: []k8s.Resource{
			secretResource("env-secret"), secretResource("from-secret"), secretResource("volume-secret"),
			secretResource("projected-secret"), secretResource("pull-secret"), secretResource("sa-secret"),
		},
	})
	h.keys("enter")
	return h
}

func consumersAppearanceHarness(t *testing.T, ascii bool) *harness {
	t.Helper()
	h := consumersHarnessOptions(t, k8s.KindSecret, "shared", ascii)
	feedRefs(h, h.topReqID(), refsFixture{
		workloads: map[string][]k8s.Workload{
			k8s.KindDeployment: {workloadWithRef(k8s.KindDeployment, "web", "1/1 ready", "shared", k8s.TagEnv)},
		},
		pods: []*corev1.Pod{podWithRef("operator-pod", "shared", k8s.TagVolume)},
		sas: []*corev1.ServiceAccount{{
			ObjectMeta: metav1.ObjectMeta{Name: "puller", Namespace: "default"},
			Secrets:    []corev1.ObjectReference{{Name: "shared"}},
		}},
	})
	return h
}

func workloadHarness(t *testing.T) *harness {
	t.Helper()
	return workloadHarnessOptions(t, true)
}

func workloadHarnessOptions(t *testing.T, ascii bool) *harness {
	t.Helper()
	h := newHarness(t, Options{ASCII: ascii})
	h.send(namespacesPageMsg{reqID: h.topReqID(), page: k8s.NamespacePage{Names: []string{"default"}}})
	h.keys("w")
	return h
}

func consumersHarness(t *testing.T, kind, name string) *harness {
	t.Helper()
	return consumersHarnessOptions(t, kind, name, true)
}

func consumersHarnessOptions(t *testing.T, kind, name string, ascii bool) *harness {
	t.Helper()
	h := newHarness(t, Options{ASCII: ascii})
	h.send(pushScreenMsg{s: newConsumersScreen(t.Context(), h.m.(app).client, kind, "default", name, h.m.(app).editEnv, h.m.(app).styles)})
	return h
}

func workloadWithRef(kind, name, ready, resourceName string, tag k8s.RefTag) k8s.Workload {
	spec := podSpecWithRef(resourceName, tag)
	return k8s.Workload{Kind: kind, Name: name, Namespace: "default", Ready: ready, Spec: spec}
}

func podWithRef(name, resourceName string, tag k8s.RefTag) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}, Spec: podSpecWithRef(resourceName, tag)}
}

func podSpecWithRef(name string, tag k8s.RefTag) corev1.PodSpec {
	return podSpecWithResourceRef(k8s.KindSecret, name, tag, false)
}

func podSpecWithResourceRef(kind, name string, tag k8s.RefTag, subPath bool) corev1.PodSpec {
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
	switch tag {
	case k8s.TagEnv:
		if kind == k8s.KindSecret {
			spec.Containers[0].Env = []corev1.EnvVar{{Name: "VALUE", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "key"}}}}
		} else {
			spec.Containers[0].Env = []corev1.EnvVar{{Name: "VALUE", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: name}, Key: "key"}}}}
		}
	case k8s.TagEnvFrom:
		if kind == k8s.KindSecret {
			spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}
		} else {
			spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}}}
		}
	case k8s.TagVolume:
		volume := corev1.Volume{Name: "data"}
		if kind == k8s.KindSecret {
			volume.Secret = &corev1.SecretVolumeSource{SecretName: name}
		} else {
			volume.ConfigMap = &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: name}}
		}
		spec.Volumes = []corev1.Volume{volume}
	case k8s.TagProjected:
		projection := corev1.VolumeProjection{}
		if kind == k8s.KindSecret {
			projection.Secret = &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: name}}
		} else {
			projection.ConfigMap = &corev1.ConfigMapProjection{LocalObjectReference: corev1.LocalObjectReference{Name: name}}
		}
		spec.Volumes = []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{projection}}}}}
	case k8s.TagPull:
		spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: name}}
	}
	if subPath {
		spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "data", MountPath: "/data", SubPath: "key"}}
	}
	return spec
}

func comprehensivePodSpec() corev1.PodSpec {
	return corev1.PodSpec{
		ServiceAccountName: "app",
		ImagePullSecrets:   []corev1.LocalObjectReference{{Name: "pull-secret"}},
		Containers: []corev1.Container{{
			Name: "app",
			Env: []corev1.EnvVar{
				{Name: "A", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "env-secret"}, Key: "API_KEY"}}},
				{Name: "B", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "env-secret"}, Key: "DB_PASSWORD"}}},
			},
			EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "from-secret"}}}},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "volume", MountPath: "/volume", SubPath: "key"},
				{Name: "projected", MountPath: "/projected"},
			},
		}},
		Volumes: []corev1.Volume{
			{Name: "volume", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "volume-secret"}}},
			{Name: "projected", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "projected-secret"}}}}}}},
			{Name: "missing", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing-config"}}}},
		},
	}
}

func secretResource(name string) k8s.Resource {
	return k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}})
}
