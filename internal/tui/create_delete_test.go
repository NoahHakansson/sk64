package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/editor"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestGolden_CreateKindPrompt(t *testing.T) {
	h := resourceHarness(t, true)
	h.keys("N")
	h.golden("create_kind_prompt")
}

func TestGolden_CreateNamePrompt(t *testing.T) {
	h := resourceHarness(t, true)
	h.keys("N", "enter")
	h.golden("create_name_prompt")
}

func TestGolden_CreateTypePrompt(t *testing.T) {
	h := resourceHarness(t, true)
	h.keys("N", "enter")
	typeText(h, "new-secret")
	h.keys("enter")
	h.golden("create_type_prompt")
}

func TestGolden_DeleteConfirm(t *testing.T) {
	h := resourceHarness(t, true)
	h.keys("D")
	h.send(resourceLoadedMsg{reqID: h.topReqID(), res: deleteTestSecret(false)})
	h.golden("delete_confirm")
}

func TestGolden_DeleteConfirmUnicode(t *testing.T) {
	h := resourceHarnessOptions(t, Options{StartNamespace: "default"})
	h.keys("D")
	h.send(resourceLoadedMsg{reqID: h.topReqID(), res: deleteTestSecret(false)})
	h.golden("delete_confirm_unicode")
}

func TestGolden_DeleteConfirmMinSize(t *testing.T) {
	h := resourceHarness(t, true)
	h.m.(app).client.Server = longPathPrefixedTestServer
	h.send(tea.WindowSizeMsg{Width: 60, Height: 15})
	h.keys("D")
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
	h.send(resourceLoadedMsg{reqID: prompt.reqID, res: deleteTestSecret(false)})
	h.send(blastRadiusMsg{reqID: prompt.radiusLoader.reqID, index: k8s.NewRefIndex()})
	if !prompt.canDelete() {
		t.Fatal("minimum-size delete confirmation is not enabled")
	}
	view := ansi.Strip(h.view())
	compactView := strings.Join(strings.Fields(strings.ReplaceAll(view, "|", " ")), " ")
	clusterIdentity := clusterIdentityLines(prompt.client.Context, longPathPrefixedTestServer, prompt.contentWidth(), prompt.styles.glyphs.separator)
	if got := reassembleClusterServer(t, clusterIdentity); got != longPathPrefixedTestServer {
		t.Fatalf("minimum-size server identity = %q, want %q", got, longPathPrefixedTestServer)
	}
	for _, line := range clusterIdentity {
		if !strings.Contains(view, line) {
			t.Fatalf("minimum-size delete confirmation lost identity line %q:\n%s", line, view)
		}
	}
	for _, want := range []string{
		"delete Secret default/app-credentials",
		"Permanent. ctrl+z cannot restore a deleted resource.",
		"type app-credentials to confirm",
	} {
		if !strings.Contains(compactView, want) {
			t.Fatalf("minimum-size delete confirmation lost %q:\n%s", want, view)
		}
	}
	h.golden("delete_confirm_min_size")
}

func TestDeleteConfirmKeepsFeedbackAtMinimumSize(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, *deleteConfirm)
		want      string
	}{
		{
			name: "wrong name",
			configure: func(_ *testing.T, prompt *deleteConfirm) {
				prompt.input.SetValue("wrong")
				prompt.Update(key("enter"))
			},
			want: "name does not match",
		},
		{
			name: "forbidden",
			configure: func(t *testing.T, prompt *deleteConfirm) {
				_, reqID := prompt.start(t.Context())
				prompt.deleting = true
				prompt.Update(deleteDoneMsg{reqID: reqID, result: k8s.DeleteResult{Outcome: k8s.DeleteForbidden, Message: "denied by policy"}})
			},
			want: "delete forbidden: denied by policy",
		},
		{
			name: "conflict",
			configure: func(t *testing.T, prompt *deleteConfirm) {
				_, reqID := prompt.start(t.Context())
				prompt.deleting = true
				prompt.Update(deleteDoneMsg{reqID: reqID, result: k8s.DeleteResult{Outcome: k8s.DeleteConflict, Message: "resource version changed"}})
			},
			want: "resource changed since this prompt opened",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient()
			client.Server = longPathPrefixedTestServer
			prompt := newDeleteConfirm(t.Context(), client, k8s.KindSecret, "default", "app-credentials", testStyles(true))
			prompt.res = deleteTestSecret(false)
			index := k8s.NewRefIndex()
			index.AddWorkload(k8s.Workload{
				Kind:      k8s.KindDeployment,
				Name:      "web",
				Namespace: "default",
				Spec:      podSpecWithRef(prompt.name, k8s.TagEnv),
			})
			prompt.radiusSummary = summarizeBlastRadius(index, prompt.kind, prompt.name)
			prompt.SetSize(60, bodyHeight(15))

			test.configure(t, prompt)

			view := ansi.Strip(prompt.View())
			compactView := strings.Join(strings.Fields(strings.ReplaceAll(view, "|", " ")), " ")
			for _, want := range []string{
				test.want,
				"context " + client.Context,
				"gateway.example",
				"apiserver/path",
				"Permanent.",
				"deleted resource.",
				"1 workload will break once this Secret is gone.",
				"type app-credentials to confirm",
				"confirm:",
			} {
				if !strings.Contains(compactView, want) {
					t.Fatalf("minimum-size delete confirmation lost %q:\n%s", want, view)
				}
			}
		})
	}
}

func TestCreateSecretFlow(t *testing.T) {
	flow, h := createFlowHarness(t, k8s.KindSecret, "created-secret", 0)
	depth := len(h.m.(app).stack)
	writeFlowFile(t, flow, "alpha: one\nbeta: two\n")
	h.send(editorFinishedMsg{})
	if flow.phase != phaseDiff || strings.Contains(flow.content(), "consumed by") {
		t.Fatalf("create diff state = phase %d content %q", flow.phase, flow.content())
	}
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
	passCommitGate(h)
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	model := h.m.(app)
	if len(model.stack) != depth-1 {
		t.Fatalf("stack depth after create = %d, want %d", len(model.stack), depth-1)
	}
	if model.editEnv.ring.Len() != 0 {
		t.Fatalf("undo entries after create = %d", model.editEnv.ring.Len())
	}
	screen := model.stack[len(model.stack)-1].(*resourceScreen)
	if !screen.pending {
		t.Fatal("resource screen did not refresh after create")
	}
	notice := plainOutcomeNotice(screen.outcome)
	if notice != "[success] created Secret default/created-secret" {
		t.Fatalf("create outcome = %q", notice)
	}
	if view := h.view(); !strings.Contains(view, notice) || !strings.Contains(view, "[loading] loading resources") {
		t.Fatalf("immediate create outcome view:\n%s", view)
	}

	h.send(
		resourcesPageMsg{reqID: screen.reqID, kind: k8s.KindSecret, page: k8s.ResourcePage{Items: []k8s.Resource{flow.res}}},
		resourcesPageMsg{reqID: screen.reqID, kind: k8s.KindConfigMap, page: k8s.ResourcePage{}},
	)
	if screen.pending || !strings.Contains(h.view(), notice) {
		t.Fatalf("create outcome after reload = pending %t view:\n%s", screen.pending, h.view())
	}
	h.keys("t")
	if screen.outcome.verb != outcomeNone {
		t.Fatalf("deliberate key left create outcome %q", plainOutcomeNotice(screen.outcome))
	}
}

func TestCreateConfigMapSkipsType(t *testing.T) {
	flow, h := createFlowHarness(t, k8s.KindConfigMap, "created-config", 0)
	if flow.target != targetCreate || flow.Title() != "created-config (create)" {
		t.Fatalf("create flow = target %d title %q", flow.target, flow.Title())
	}
	if _, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*editFlow); !ok {
		t.Fatalf("top screen = %T, want editFlow", h.m.(app).stack[len(h.m.(app).stack)-1])
	}
}

func TestCreatePromptValidation(t *testing.T) {
	h := resourceHarness(t, true)
	h.keys("N", "enter")
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*createPrompt)

	h.keys("enter")
	if prompt.message != "name is required" || prompt.step != stepName {
		t.Fatalf("empty name = step %d message %q", prompt.step, prompt.message)
	}
	prompt.input.SetValue("Bad_Name!")
	h.keys("enter")
	if prompt.message == "" || prompt.step != stepName {
		t.Fatalf("invalid name = step %d message %q", prompt.step, prompt.message)
	}
	prompt.input.SetValue("app-credentials")
	h.keys("enter")
	if !strings.Contains(prompt.message, "already exists") || prompt.step != stepName {
		t.Fatalf("same-kind duplicate = step %d message %q", prompt.step, prompt.message)
	}
	prompt.input.SetValue("app-settings")
	h.keys("enter")
	if prompt.step != stepType {
		t.Fatalf("other-kind duplicate stayed at step %d, want type", prompt.step)
	}
}

func TestCreatePromptResetsCursorAcrossEverySecretType(t *testing.T) {
	for typeCursor, secretType := range k8s.WellKnownSecretTypes() {
		t.Run(secretType, func(t *testing.T) {
			h := resourceHarness(t, true)
			h.keys("N", "enter")
			prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*createPrompt)
			prompt.input.SetValue(fmt.Sprintf("new-secret-%d", typeCursor))
			h.keys("enter")
			for range typeCursor {
				h.keys("down")
			}
			if prompt.step != stepType || prompt.cursor != typeCursor {
				t.Fatalf("type selection = step %d cursor %d, want stepType cursor %d", prompt.step, prompt.cursor, typeCursor)
			}

			h.keys("esc", "esc")
			if prompt.step != stepKind || prompt.cursor != 0 || prompt.kind != "" {
				t.Fatalf("back at kind = step %d cursor %d kind %q", prompt.step, prompt.cursor, prompt.kind)
			}
			if view := ansi.Strip(prompt.View()); !strings.Contains(view, "> Secret") {
				t.Fatalf("kind prompt has no valid highlighted kind:\n%s", view)
			}

			h.keys("enter")
			if prompt.step != stepName || prompt.cursor != 0 || prompt.kind != createKinds[0] {
				t.Fatalf("re-enter kind = step %d cursor %d kind %q", prompt.step, prompt.cursor, prompt.kind)
			}
		})
	}
}

func TestCreatePromptKeepsSelectedChoiceAtMinimumSize(t *testing.T) {
	tests := []struct {
		name    string
		step    createStep
		cursor  int
		want    string
		subject string
	}{
		{name: "kind", step: stepKind, cursor: len(createKinds) - 1, want: "> ConfigMap", subject: "create namespace default"},
		{name: "secret type", step: stepType, cursor: len(k8s.WellKnownSecretTypes()) - 1, want: "> kubernetes.io/ssh-auth", subject: "create Secret default/new-secret"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompt := newCreatePrompt(t.Context(), testClient(), editEnv{}, "default", nil, testStyles(true))
			prompt.step = test.step
			prompt.cursor = test.cursor
			prompt.input.SetValue("new-secret")
			prompt.message = "validation failed"
			prompt.SetSize(60, 13)

			view := ansi.Strip(prompt.View())
			for _, want := range []string{test.want, test.subject, "context test-ctx", "server https://test.example", prompt.message} {
				if !strings.Contains(view, want) {
					t.Fatalf("minimum-size create prompt = %q, want %q", view, want)
				}
			}
		})
	}
}

func TestSecretTypeRequirementsCoverWellKnownTypes(t *testing.T) {
	for _, secretType := range k8s.WellKnownSecretTypes() {
		t.Run(secretType, func(t *testing.T) {
			if strings.TrimSpace(secretTypeRequirements[secretType]) == "" {
				t.Fatalf("Secret type %q has no create-prompt requirements", secretType)
			}
		})
	}
}

func TestCreateNoConfigMapsSkipsKind(t *testing.T) {
	h := newHarness(t, Options{StartNamespace: "default", ASCII: true, NoConfigMaps: true})
	h.send(resourcesPageMsg{reqID: h.topReqID(), kind: k8s.KindSecret, page: k8s.ResourcePage{}})
	depth := len(h.m.(app).stack)
	h.keys("N")
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*createPrompt)
	if prompt.step != stepName || prompt.kind != k8s.KindSecret {
		t.Fatalf("no-configmaps prompt = step %d kind %q", prompt.step, prompt.kind)
	}
	h.keys("esc")
	if len(h.m.(app).stack) != depth {
		t.Fatalf("esc stack depth = %d, want %d", len(h.m.(app).stack), depth)
	}
}

func TestCreateCancelPaths(t *testing.T) {
	t.Run("unchanged", func(t *testing.T) {
		_, h := createFlowHarness(t, k8s.KindSecret, "unchanged", 0)
		depth := len(h.m.(app).stack)
		h.send(editorFinishedMsg{})
		if len(h.m.(app).stack) != depth-1 {
			t.Fatalf("unchanged editor stack depth = %d, want %d", len(h.m.(app).stack), depth-1)
		}
		assertResourceNotCreated(t, h, k8s.KindSecret, "unchanged")
	})

	t.Run("comments only allows keyless resource", func(t *testing.T) {
		flow, h := createFlowHarness(t, k8s.KindConfigMap, "comments-only", 0)
		depth := len(h.m.(app).stack)
		writeFlowFile(t, flow, "# no keys\n")
		h.send(editorFinishedMsg{})
		if len(h.m.(app).stack) != depth || flow.phase != phaseDiff || len(flow.editedMap) != 0 {
			t.Fatalf("keyless create = stack depth %d phase %d values %#v", len(h.m.(app).stack), flow.phase, flow.editedMap)
		}
	})
}

func TestCreateDryRunRejected(t *testing.T) {
	flow, h := createFlowHarness(t, k8s.KindConfigMap, "rejected", 0)
	writeFlowFile(t, flow, "key: value\n")
	h.send(editorFinishedMsg{})
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunRejected, Message: "denied"}})
	if flow.phase != phaseDryRunRejected {
		t.Fatalf("dry-run rejection phase = %d", flow.phase)
	}
	wantRaw := string(flow.rawDoc)
	h.keys("e")
	if flow.phase != phaseEditing {
		t.Fatalf("re-edit phase = %d", flow.phase)
	}
	content, err := os.ReadFile(flow.filePath)
	if err != nil || string(content) != wantRaw {
		t.Fatalf("reopened content = %q, err = %v, want %q", content, err, wantRaw)
	}
}

func TestCreateValidateWarn(t *testing.T) {
	flow, h := createFlowHarness(t, k8s.KindSecret, "tls-secret", 1)
	writeFlowFile(t, flow, "tls.crt: certificate\n")
	h.send(editorFinishedMsg{})
	h.keys("Y")
	if flow.phase != phaseValidateWarn || !strings.Contains(flow.View(), "tls.key") {
		t.Fatalf("validation state = phase %d view %q", flow.phase, flow.View())
	}
	h.keys("Y")
	if flow.phase != phaseDryRun || !flow.pending {
		t.Fatalf("validation override state = phase %d pending %t", flow.phase, flow.pending)
	}
}

func TestDeleteConfirmFlow(t *testing.T) {
	h, resource := deleteHarness(t, false)
	depth := len(h.m.(app).stack)
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
	h.send(blastRadiusMsg{reqID: prompt.radiusLoader.reqID, index: k8s.NewRefIndex()})
	typeText(h, "wrong")
	h.keys("enter")
	if prompt.message != "name does not match" || len(h.m.(app).stack) != depth {
		t.Fatalf("wrong confirmation = message %q depth %d", prompt.message, len(h.m.(app).stack))
	}
	prompt.input.SetValue(resource.Name())
	h.keys("enter")
	if len(h.m.(app).stack) != depth-1 {
		t.Fatalf("successful delete depth = %d, want %d", len(h.m.(app).stack), depth-1)
	}
	if _, err := h.m.(app).client.Clientset.CoreV1().Secrets("default").Get(t.Context(), resource.Name(), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted resource Get() error = %v", err)
	}
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
	if !screen.pending {
		t.Fatal("resource screen did not refresh after delete")
	}
	notice := plainOutcomeNotice(screen.outcome)
	if notice != "[success] deleted Secret default/app-credentials" {
		t.Fatalf("delete outcome = %q", notice)
	}
	if view := h.view(); !strings.Contains(view, notice) || !strings.Contains(view, "[loading] loading resources") {
		t.Fatalf("immediate delete outcome view:\n%s", view)
	}

	h.send(
		resourcesPageMsg{reqID: screen.reqID, kind: k8s.KindSecret, page: k8s.ResourcePage{}},
		resourcesPageMsg{reqID: screen.reqID, kind: k8s.KindConfigMap, page: k8s.ResourcePage{}},
	)
	if screen.pending || !strings.Contains(h.view(), notice) {
		t.Fatalf("delete outcome after reload = pending %t view:\n%s", screen.pending, h.view())
	}
	h.keys("t")
	if screen.outcome.verb != outcomeNone {
		t.Fatalf("deliberate key left delete outcome %q", plainOutcomeNotice(screen.outcome))
	}
}

func TestDeleteConfirmEnterStaysTheAcceptKey(t *testing.T) {
	h, resource := deleteHarness(t, false)
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
	h.send(blastRadiusMsg{reqID: prompt.radiusLoader.reqID, index: k8s.NewRefIndex()})
	clientset := h.m.(app).client.Clientset.(*fake.Clientset)
	clientset.ClearActions()

	prompt.input.SetValue(resource.Name())
	h.keys("Y")
	if len(clientset.Actions()) != 0 {
		t.Fatalf("Y recorded %d client actions", len(clientset.Actions()))
	}
	if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != prompt {
		t.Fatalf("Y left top screen %T, want deleteConfirm", top)
	}
	if got := prompt.input.Value(); got != resource.Name()+"Y" {
		t.Fatalf("input after Y = %q, want Y typed into the name", got)
	}

	prompt.input.SetValue(resource.Name())
	h.keys("enter")
	actions := deleteActions(clientset.Actions())
	if len(actions) != 1 {
		t.Fatalf("enter delete actions = %#v, want one delete", actions)
	}
}

func TestDeleteConfirmBlocksEnterWhileConsumerCheckIsPending(t *testing.T) {
	h, resource := deleteHarness(t, false)
	depth := len(h.m.(app).stack)
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
	clientset := h.m.(app).client.Clientset.(*fake.Clientset)
	clientset.ClearActions()

	prompt.input.SetValue("wrong")
	h.keys("enter")
	if prompt.message != "name does not match" {
		t.Fatalf("pending consumer check wrong-name message = %q", prompt.message)
	}

	prompt.input.SetValue(resource.Name())
	h.keys("enter")

	if actions := deleteActions(clientset.Actions()); len(actions) != 0 {
		t.Fatalf("pending consumer check issued delete actions: %#v", actions)
	}
	if prompt.deleting {
		t.Fatal("pending consumer check entered deleting state")
	}
	if len(h.m.(app).stack) != depth || h.m.(app).stack[depth-1] != prompt {
		t.Fatalf("pending consumer check changed stack depth %d or top screen %T", len(h.m.(app).stack), h.m.(app).stack[len(h.m.(app).stack)-1])
	}
	if prompt.message != consumerCheckPendingMessage {
		t.Fatalf("pending consumer check message = %q", prompt.message)
	}
	if hints := plainFooter(t, prompt, 1); strings.Contains(hints, "enter delete") {
		t.Fatalf("pending consumer check hints = %q, advertised delete", hints)
	}
	view := ansi.Strip(prompt.View())
	for _, want := range []string{"checking consumers", "consumer check is still running", "esc cancels"} {
		if !strings.Contains(view, want) {
			t.Fatalf("pending consumer check view lost %q: %q", want, view)
		}
	}
}

func TestDeleteConfirmEnterWhileResourceLoadsIsNonDestructive(t *testing.T) {
	h := resourceHarness(t, true)
	depth := len(h.m.(app).stack)
	h.keys("D")
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
	clientset := h.m.(app).client.Clientset.(*fake.Clientset)
	clientset.ClearActions()

	h.keys("enter")

	if actions := deleteActions(clientset.Actions()); len(actions) != 0 {
		t.Fatalf("resource load issued delete actions: %#v", actions)
	}
	if prompt.res != nil || prompt.deleting {
		t.Fatalf("resource load enter state = resource loaded %t deleting %t", prompt.res != nil, prompt.deleting)
	}
	if len(h.m.(app).stack) != depth+1 || h.m.(app).stack[depth] != prompt {
		t.Fatalf("resource load enter changed stack depth %d or top screen %T", len(h.m.(app).stack), h.m.(app).stack[len(h.m.(app).stack)-1])
	}
}

func TestDeleteConfirmConsumerCheckCompletionUnlocksOneDelete(t *testing.T) {
	tests := []struct {
		name string
		msg  func(int) blastRadiusMsg
	}{
		{
			name: "completed index",
			msg: func(reqID int) blastRadiusMsg {
				return blastRadiusMsg{reqID: reqID, index: k8s.NewRefIndex()}
			},
		},
		{
			name: "explicit failure",
			msg: func(reqID int) blastRadiusMsg {
				return blastRadiusMsg{reqID: reqID, err: errors.New("forbidden")}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, resource := deleteHarness(t, false)
			prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
			clientset := h.m.(app).client.Clientset.(*fake.Clientset)
			clientset.ClearActions()
			prompt.input.SetValue(resource.Name())

			h.keys("enter")
			if prompt.message != consumerCheckPendingMessage {
				t.Fatalf("pending consumer check message = %q", prompt.message)
			}
			if actions := deleteActions(clientset.Actions()); len(actions) != 0 {
				t.Fatalf("pending consumer check delete actions = %#v", actions)
			}

			h.send(test.msg(prompt.radiusLoader.reqID))
			if prompt.message != "" {
				t.Fatalf("completed consumer check retained message %q", prompt.message)
			}
			if view := ansi.Strip(prompt.View()); strings.Contains(view, consumerCheckPendingMessage) {
				t.Fatalf("completed consumer check retained wait feedback: %q", view)
			}
			if hints := plainFooter(t, prompt, 1); !strings.Contains(hints, "enter delete") {
				t.Fatalf("completed consumer check hints = %q", hints)
			}

			h.keys("enter")

			if actions := deleteActions(clientset.Actions()); len(actions) != 1 {
				t.Fatalf("completed consumer check delete actions = %#v, want one", actions)
			}
		})
	}
}

func TestDeleteSucceededStopsBothLoadersAndRejectsLateEvidence(t *testing.T) {
	h := resourceHarness(t, true)
	h.keys("D")
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
	h.send(resourceLoadedMsg{reqID: prompt.reqID, res: deleteTestSecret(false)})
	radiusReqID := prompt.radiusLoader.reqID
	_, deleteReqID := prompt.start(t.Context())
	prompt.deleting = true

	h.send(deleteDoneMsg{reqID: deleteReqID, result: k8s.DeleteResult{Outcome: k8s.DeleteSucceeded}})

	if prompt.pending || prompt.radiusLoader.pending {
		t.Fatalf("loaders after success = delete %t radius %t", prompt.pending, prompt.radiusLoader.pending)
	}
	model := h.m.(app)
	depth := len(model.stack)
	top := model.stack[len(model.stack)-1]
	resourceScreen := top.(*resourceScreen)
	remainingReqID := resourceScreen.reqID

	h.send(blastRadiusMsg{reqID: radiusReqID, index: k8s.NewRefIndex()})

	model = h.m.(app)
	if len(model.stack) != depth || model.stack[len(model.stack)-1] != top || resourceScreen.reqID != remainingReqID {
		t.Fatalf("late evidence changed remaining stack: depth %d/%d top %T reqID %d/%d", len(model.stack), depth, model.stack[len(model.stack)-1], resourceScreen.reqID, remainingReqID)
	}
}

func TestDeleteConfirmShowsConsumers(t *testing.T) {
	h, _ := deleteHarness(t, false)
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
	index := k8s.NewRefIndex()
	index.AddWorkload(k8s.Workload{
		Kind:      k8s.KindDeployment,
		Name:      "web",
		Namespace: "default",
		Spec:      podSpecWithRef(prompt.name, k8s.TagEnv),
	})
	index.AddServiceAccount(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: "builder", Namespace: "default"},
		Secrets:    []corev1.ObjectReference{{Name: prompt.name}},
	})

	h.send(blastRadiusMsg{reqID: prompt.radiusLoader.reqID, index: index})

	view := h.view()
	if !strings.Contains(view, "Deployment/web") || !strings.Contains(view, "1 workload and 1 serviceaccount will break") {
		t.Fatalf("delete confirmation view = %q", view)
	}
}

func TestDeleteConfirmWarnsWhenConsumerCheckFails(t *testing.T) {
	h, resource := deleteHarness(t, false)
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
	h.send(blastRadiusMsg{reqID: prompt.radiusLoader.reqID, err: errors.New("forbidden")})
	if view := h.view(); !strings.Contains(view, "Consumer check failed") {
		t.Fatalf("delete confirmation view = %q", view)
	}

	clientset := h.m.(app).client.Clientset.(*fake.Clientset)
	clientset.ClearActions()
	prompt.input.SetValue(resource.Name())
	h.keys("enter")
	actions := deleteActions(clientset.Actions())
	if len(actions) != 1 {
		t.Fatalf("delete after consumer error actions = %#v", actions)
	}
}

func TestDeleteConfirmKeepsTargetIdentityAtSupportedSizes(t *testing.T) {
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{name: "normal", width: 80, height: 22},
		{name: "minimum", width: 60, height: 13},
	} {
		t.Run(size.name, func(t *testing.T) {
			client := testClient()
			prompt := newDeleteConfirm(t.Context(), client, k8s.KindSecret, "default", "app-credentials", testStyles(true))
			prompt.res = deleteTestSecret(false)
			prompt.radiusSummary = summarizeBlastRadius(k8s.NewRefIndex(), prompt.kind, prompt.name)
			prompt.SetSize(size.width, size.height)
			view := ansi.Strip(prompt.View())
			for _, want := range []string{
				"delete Secret default/app-credentials",
				"context " + client.Context,
				"server " + client.Server,
				"type app-credentials to confirm",
			} {
				if !strings.Contains(view, want) {
					t.Fatalf("delete confirmation at %dx%d lost %q:\n%s", size.width, size.height, want, view)
				}
			}
		})
	}
}

func TestDeleteConfirmKeepsSafetyWarningsAtSupportedSizes(t *testing.T) {
	const permanenceWarning = "Permanent. ctrl+z cannot restore a deleted resource."
	tests := []struct {
		name         string
		configure    func(*deleteConfirm)
		wantWarnings []string
	}{
		{
			name: "loaded resource",
			configure: func(prompt *deleteConfirm) {
				prompt.radiusSummary = summarizeBlastRadius(k8s.NewRefIndex(), prompt.kind, prompt.name)
			},
		},
		{
			name: "failed consumer check",
			configure: func(prompt *deleteConfirm) {
				prompt.radiusErr = errors.New("forbidden")
			},
			wantWarnings: []string{"Consumer check failed; workloads you cannot see here may depend on this Secret."},
		},
		{
			name: "incomplete consumer list",
			configure: func(prompt *deleteConfirm) {
				index := k8s.NewRefIndex()
				index.AddSourceError("pods")
				prompt.radiusSummary = summarizeBlastRadius(index, prompt.kind, prompt.name)
			},
			wantWarnings: []string{"Consumer list is incomplete: pods not listable."},
		},
		{
			name: "consumers found",
			configure: func(prompt *deleteConfirm) {
				index := k8s.NewRefIndex()
				index.AddWorkload(k8s.Workload{
					Kind:      k8s.KindDeployment,
					Name:      "web",
					Namespace: "default",
					Spec:      podSpecWithRef(prompt.name, k8s.TagEnv),
				})
				prompt.radiusSummary = summarizeBlastRadius(index, prompt.kind, prompt.name)
			},
			wantWarnings: []string{"1 workload will break once this Secret is gone."},
		},
	}
	sizes := []struct {
		name          string
		width, height int
	}{
		{name: "minimum", width: 60, height: 15},
		{name: "normal", width: 80, height: 24},
	}
	for _, test := range tests {
		for _, size := range sizes {
			t.Run(test.name+"/"+size.name, func(t *testing.T) {
				prompt := newDeleteConfirm(t.Context(), testClient(), k8s.KindSecret, "default", "app-credentials", testStyles(true))
				prompt.res = deleteTestSecret(false)
				test.configure(prompt)
				prompt.SetSize(size.width, size.height)
				if !prompt.canDelete() {
					t.Fatalf("delete confirmation at %dx%d is not enabled", size.width, size.height)
				}

				view := ansi.Strip(prompt.View())
				compactView := strings.Join(strings.Fields(strings.ReplaceAll(view, "|", " ")), " ")
				for _, want := range append([]string{permanenceWarning}, test.wantWarnings...) {
					if !strings.Contains(compactView, want) {
						t.Fatalf("delete confirmation at %dx%d lost %q:\n%s", size.width, size.height, want, view)
					}
				}
			})
		}
	}
}

func TestDeleteConfirmAlwaysWarnsAboutPermanence(t *testing.T) {
	h := resourceHarness(t, true)
	h.keys("D")
	if view := h.view(); !strings.Contains(view, "ctrl+z cannot restore") {
		t.Fatalf("loading delete confirmation view = %q", view)
	}
}

func TestDeleteConfirmEscStopsBothLoaders(t *testing.T) {
	h := resourceHarness(t, true)
	h.keys("D")
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
	if !prompt.pending || !prompt.radiusLoader.pending {
		t.Fatalf("loaders before esc = resource %t radius %t", prompt.pending, prompt.radiusLoader.pending)
	}

	h.keys("esc")

	if prompt.pending || prompt.radiusLoader.pending {
		t.Fatalf("loaders after esc = resource %t radius %t", prompt.pending, prompt.radiusLoader.pending)
	}
}

func TestDeleteConfirmKeepsTypingAreaForMaximumIdentity(t *testing.T) {
	name := strings.Repeat("n", 249) + "-one"
	contextName := strings.Repeat("context-segment-", 10) + "production"
	server := "https://gateway.example/" + strings.Repeat("shared-path/", 12) + "clusters/one"
	tests := []struct {
		name                          string
		terminalWidth, terminalHeight int
	}{
		{name: "minimum terminal", terminalWidth: 60, terminalHeight: 15},
		{name: "standard terminal", terminalWidth: 80, terminalHeight: 24},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient()
			client.Context = contextName
			client.Server = server
			prompt := newDeleteConfirm(t.Context(), client, k8s.KindSecret, "default", name, testStyles(true))
			prompt.res = deleteTestSecret(false)
			prompt.radiusSummary = summarizeBlastRadius(k8s.NewRefIndex(), prompt.kind, prompt.name)
			assignedHeight := bodyHeight(test.terminalHeight)
			prompt.SetSize(test.terminalWidth, assignedHeight)

			if width := prompt.input.Width(); width < 20 {
				t.Fatalf("input width = %d, want room to see the typed name", width)
			}
			view := ansi.Strip(prompt.View())
			if got := lipgloss.Height(view); got != assignedHeight {
				t.Fatalf("dialog height = %d, want %d:\n%s", got, assignedHeight, view)
			}
			assertRenderedLinesFitWidth(t, view, test.terminalWidth)
			compactView := strings.Join(strings.Fields(strings.ReplaceAll(view, "|", " ")), " ")
			for _, want := range []string{
				"to confirm",
				"confirm:",
				"Permanent.",
				"deleted resource.",
				"production",
			} {
				if !strings.Contains(compactView, want) {
					t.Fatalf("delete confirmation lost %q:\n%s", want, view)
				}
			}
			assertRenderedLineContains(t, view, "server ", "gateway.example")
			if test.terminalWidth == 60 {
				assertRenderedLineContains(t, view, "type ", "-one to confirm")
			}
		})
	}
}

func TestDeleteConflictAndForbidden(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		want       string
		conflicted bool
	}{
		{name: "conflict", err: apierrors.NewConflict(schema.GroupResource{Resource: "secrets"}, "app-credentials", errors.New("changed")), want: "resource changed since this prompt opened", conflicted: true},
		{name: "forbidden", err: apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "app-credentials", errors.New("denied")), want: "delete forbidden"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, resource := deleteHarness(t, false)
			clientset := h.m.(app).client.Clientset.(*fake.Clientset)
			clientset.PrependReactor("delete", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, test.err
			})
			depth := len(h.m.(app).stack)
			prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
			h.send(blastRadiusMsg{reqID: prompt.radiusLoader.reqID, index: k8s.NewRefIndex()})
			prompt.input.SetValue(resource.Name())
			h.keys("enter")
			if len(h.m.(app).stack) != depth || !strings.Contains(prompt.message, test.want) {
				t.Fatalf("delete result = depth %d message %q", len(h.m.(app).stack), prompt.message)
			}
			if prompt.conflicted != test.conflicted {
				t.Fatalf("conflicted = %t, want %t", prompt.conflicted, test.conflicted)
			}
			if test.conflicted {
				if prompt.canDelete() {
					t.Fatal("conflicted delete remained enabled")
				}
				if hints := plainFooter(t, prompt, 1); strings.Contains(hints, "enter delete") {
					t.Fatalf("conflicted hints = %q, advertised delete", hints)
				}
				if !strings.Contains(prompt.message, "press esc, refresh the list, and retry") {
					t.Fatalf("conflict message lost recovery path: %q", prompt.message)
				}
				deleteCount := len(deleteActions(clientset.Actions()))
				h.keys("enter")
				if got := len(deleteActions(clientset.Actions())); got != deleteCount {
					t.Fatalf("conflicted enter recorded %d deletes, want %d", got, deleteCount)
				}
			}
			h.keys("esc")
			if len(h.m.(app).stack) != depth-1 {
				t.Fatalf("esc stack depth = %d, want %d", len(h.m.(app).stack), depth-1)
			}
		})
	}
}

func TestDeleteVanishedResource(t *testing.T) {
	h := resourceHarness(t, true)
	depth := len(h.m.(app).stack)
	h.keys("D")
	err := apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "app-credentials")
	h.send(resourceLoadedMsg{reqID: h.topReqID(), err: err})
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*deleteConfirm)
	if prompt.res != nil || !strings.Contains(prompt.message, "no longer exists") {
		t.Fatalf("vanished resource = res %v message %q", prompt.res, prompt.message)
	}
	h.keys("esc")
	if len(h.m.(app).stack) != depth {
		t.Fatalf("esc stack depth = %d, want %d", len(h.m.(app).stack), depth)
	}
}

func TestDeleteErrorsAreStyled(t *testing.T) {
	tests := []struct {
		name   string
		update func(*deleteConfirm)
	}{
		{
			name: "load failure",
			update: func(prompt *deleteConfirm) {
				_, reqID := prompt.start(t.Context())
				prompt.Update(resourceLoadedMsg{reqID: reqID, err: errors.New("load failed")})
			},
		},
		{
			name: "delete failure",
			update: func(prompt *deleteConfirm) {
				_, reqID := prompt.start(t.Context())
				prompt.deleting = true
				prompt.Update(deleteDoneMsg{reqID: reqID, result: k8s.DeleteResult{Outcome: k8s.DeleteFailed, Message: "delete failed"}})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prompt := newDeleteConfirm(t.Context(), testClient(), k8s.KindSecret, "default", "secret", testStyles(true))
			prompt.SetSize(80, 22)
			test.update(prompt)
			view := prompt.View()
			if !strings.Contains(ansi.Strip(view), prompt.message) {
				t.Fatalf("View() = %q, want message %q", view, prompt.message)
			}
			styled := strings.TrimSuffix(prompt.styles.errText.Render(prompt.message), "\x1b[m")
			if !strings.Contains(view, styled) {
				t.Fatalf("View() = %q, want styled message %q", view, styled)
			}
		})
	}
}

func deleteActions(actions []clienttesting.Action) []clienttesting.Action {
	deletes := make([]clienttesting.Action, 0, 1)
	for _, action := range actions {
		if action.GetVerb() == "delete" {
			deletes = append(deletes, action)
		}
	}
	return deletes
}

func TestReadOnlyBlocksCreateDelete(t *testing.T) {
	h := resourceHarnessOptions(t, Options{ASCII: true, ReadOnly: true})
	depth := len(h.m.(app).stack)
	h.keys("N", "D")
	if len(h.m.(app).stack) != depth {
		t.Fatalf("read-only stack depth = %d, want %d", len(h.m.(app).stack), depth)
	}

	immutable := deleteTestSecret(true)
	h = newHarness(t, Options{StartNamespace: "default", ASCII: true})
	h.send(
		resourcesPageMsg{reqID: h.topReqID(), kind: k8s.KindSecret, page: k8s.ResourcePage{Items: []k8s.Resource{immutable}}},
		resourcesPageMsg{reqID: h.topReqID(), kind: k8s.KindConfigMap, page: k8s.ResourcePage{}},
	)
	depth = len(h.m.(app).stack)
	h.keys("D")
	if len(h.m.(app).stack) != depth+1 {
		t.Fatalf("immutable delete stack depth = %d, want %d", len(h.m.(app).stack), depth+1)
	}
}

func TestCreateBlockedWhileResourceListPending(t *testing.T) {
	h := newHarness(t, Options{StartNamespace: "default", ASCII: true})
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
	depth := len(h.m.(app).stack)
	h.keys("N")
	if !screen.pending || len(h.m.(app).stack) != depth {
		t.Fatalf("pending create = pending %t stack depth %d, want depth %d", screen.pending, len(h.m.(app).stack), depth)
	}
}

func TestResourceHints(t *testing.T) {
	tests := []struct {
		name string
		env  editEnv
		want string
	}{
		{
			name: "writable",
			want: "enter keys  N new  D delete  r consumers  L link  t type  / filter  ? help",
		},
		{
			name: "read only",
			env:  editEnv{readOnly: true},
			want: "enter keys  r consumers  L link  t type  / filter  ? help",
		},
		{
			name: "secrets only",
			env:  editEnv{noConfigMaps: true},
			want: "enter keys  N new  D delete  r consumers  L link  / filter  ? help",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			screen := newResourceScreen(t.Context(), testClient(), "default", test.env, testStyles(true))
			if got := plainFooter(t, screen, 1); got != test.want {
				t.Fatalf("Hints() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResourceListRefreshOnChange(t *testing.T) {
	h := resourceHarness(t, true)
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*resourceScreen)
	reqID := screen.reqID
	h.send(resourceListChangedMsg{namespace: "other"})
	if screen.pending || screen.reqID != reqID {
		t.Fatalf("other namespace refresh = pending %t reqID %d", screen.pending, screen.reqID)
	}
	h.send(resourceListChangedMsg{namespace: "default"})
	if !screen.pending || screen.reqID == reqID {
		t.Fatalf("matching refresh = pending %t reqID %d", screen.pending, screen.reqID)
	}
	refreshReqID := screen.reqID
	h.send(resourceListChangedMsg{namespace: "default"})
	if !screen.pending || screen.reqID == refreshReqID {
		t.Fatalf("pending refresh = pending %t reqID %d, want a new reqID after %d", screen.pending, screen.reqID, refreshReqID)
	}
}

func createFlowHarness(t *testing.T, kind, name string, secretTypeCursor int) (*editFlow, *harness) {
	t.Helper()
	t.Cleanup(editor.CleanupAll)
	h := resourceHarness(t, true)
	h.keys("N")
	if kind == k8s.KindConfigMap {
		h.keys("down")
	}
	h.keys("enter")
	typeText(h, name)
	h.keys("enter")
	if kind == k8s.KindSecret {
		for range secretTypeCursor {
			h.keys("down")
		}
		h.keys("enter")
	}
	return topEditFlow(t, h), h
}

func deleteHarness(t *testing.T, immutable bool) (*harness, k8s.Resource) {
	t.Helper()
	h := resourceHarness(t, true)
	secret := deleteTestSecretObject(immutable)
	resource := k8s.NewSecret(secret)
	if err := h.m.(app).client.Clientset.(*fake.Clientset).Tracker().Add(secret.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	h.keys("D")
	h.send(resourceLoadedMsg{reqID: h.topReqID(), res: resource})
	return h, resource
}

func deleteTestSecret(immutable bool) k8s.Resource {
	return k8s.NewSecret(deleteTestSecretObject(immutable))
}

func deleteTestSecretObject(immutable bool) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-credentials", Namespace: "default", UID: "uid-1", ResourceVersion: "10"},
		Immutable:  &immutable,
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"key": []byte("value")},
	}
}

func assertResourceNotCreated(t *testing.T, h *harness, kind, name string) {
	t.Helper()
	clientset := h.m.(app).client.Clientset
	var err error
	if kind == k8s.KindSecret {
		_, err = clientset.CoreV1().Secrets("default").Get(t.Context(), name, metav1.GetOptions{})
	} else {
		_, err = clientset.CoreV1().ConfigMaps("default").Get(t.Context(), name, metav1.GetOptions{})
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("resource %s/%s exists or Get failed: %v", kind, name, err)
	}
}
