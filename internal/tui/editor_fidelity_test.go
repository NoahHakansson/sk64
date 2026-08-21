package tui

import (
	"bytes"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/NoahHakansson/sk64/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestWholeResourceEditorFidelity(t *testing.T) {
	unchangedTransforms := []struct {
		name      string
		transform func(string) string
		assertDoc func(*testing.T, string, string)
	}{
		{
			name:      "editor appends trailing newline",
			transform: func(document string) string { return document + "\n" },
		},
		{
			name: "editor strips trailing whitespace",
			transform: func(document string) string {
				lines := strings.Split(document, "\n")
				for index := range lines {
					lines[index] = strings.TrimRight(lines[index], " \t")
				}
				return strings.Join(lines, "\n")
			},
			assertDoc: func(t *testing.T, seeded, edited string) {
				t.Helper()
				if edited != seeded {
					t.Fatalf("whitespace-stripped document changed:\n%s", edited)
				}
			},
		},
		{
			name:      "editor uniformly reindents block content",
			transform: reindentBlockContent,
			assertDoc: func(t *testing.T, seeded, edited string) {
				t.Helper()
				if !strings.Contains(seeded, "TENANTS: |-\n") || !strings.Contains(seeded, "multi: |-\n") {
					t.Fatalf("seeded document does not contain both expected block scalars:\n%s", seeded)
				}
				if edited == seeded {
					t.Fatal("block-scalar reindent did not change the seeded document")
				}
			},
		},
		{
			name:      "editor converts LF to CRLF",
			transform: func(document string) string { return strings.ReplaceAll(document, "\n", "\r\n") },
		},
	}
	for _, test := range unchangedTransforms {
		t.Run(test.name, func(t *testing.T) {
			h, flow, resource, seeded, clientset := wholeResourceEditorHarness(t)
			edited := test.transform(seeded)
			if test.assertDoc != nil {
				test.assertDoc(t, seeded, edited)
			}
			writeFlowFile(t, flow, edited)
			h.send(editorFinishedMsg{})

			assertResourceValues(t, resource, editorFidelityValues())
			if countUpdateActions(clientset) != 0 {
				t.Fatalf("no-op edit recorded %d update actions", countUpdateActions(clientset))
			}
			if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top == flow {
				t.Fatal("no-op edit did not pop the flow")
			}
		})
	}

	t.Run("user prettifies JSON", func(t *testing.T) {
		h, flow, _, seeded, clientset := wholeResourceEditorHarness(t)
		compact := `[{"id":"district-sodervik","organization_id":"org_0TEST"}]`
		pretty := "[\n  {\n    \"id\": \"district-sodervik\",\n    \"organization_id\": \"org_0TEST\"\n  }\n]"
		edited := strings.Replace(seeded, "TENANTS: |-\n  "+compact, "TENANTS: |-\n  "+strings.ReplaceAll(pretty, "\n", "\n  "), 1)
		if edited == seeded {
			t.Fatal("JSON block was not replaced")
		}
		writeFlowFile(t, flow, edited)
		h.send(editorFinishedMsg{})
		if flow.phase != phaseDiff || flow.editedMap["TENANTS"] != pretty {
			t.Fatalf("prettified JSON state = phase %d value %q", flow.phase, flow.editedMap["TENANTS"])
		}
		h.keys("Y")
		h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
		passCommitGate(h)
		updates := recordedSecretData(t, clientset)
		assertRecordedSecretUpdatesEqual(t, updates)
		if got := updates.persisted["TENANTS"]; !bytes.Equal(got, []byte(pretty)) {
			t.Fatalf("recorded JSON = %q, want %q", got, pretty)
		}
	})

	t.Run("user adds trailing block space", func(t *testing.T) {
		h, flow, _, seeded, clientset := wholeResourceEditorHarness(t)
		edited := strings.Replace(seeded, "multi: |-\n  a\n  b", "multi: |-\n  a \n  b", 1)
		if edited == seeded {
			t.Fatal("multi-line block was not replaced")
		}
		writeFlowFile(t, flow, edited)
		h.send(editorFinishedMsg{})
		want := "a \nb"
		if flow.phase != phaseDiff || flow.editedMap["multi"] != want || !strings.Contains(flow.content(), "a ") {
			t.Fatalf("trailing-space edit state = phase %d value %q\n%s", flow.phase, flow.editedMap["multi"], flow.content())
		}
		h.keys("Y")
		h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
		passCommitGate(h)
		updates := recordedSecretData(t, clientset)
		assertRecordedSecretUpdatesEqual(t, updates)
		if got := updates.persisted["multi"]; !bytes.Equal(got, []byte(want)) {
			t.Fatalf("recorded multi value = %q, want %q", got, want)
		}
		original := editorFidelityValues()
		if got := updates.persisted["TENANTS"]; !bytes.Equal(got, original["TENANTS"]) {
			t.Fatalf("recorded untouched JSON = %q, want %q", got, original["TENANTS"])
		}
		if got := updates.persisted["crlf"]; !bytes.Equal(got, original["crlf"]) {
			t.Fatalf("recorded untouched CRLF value = %q, want %q", got, original["crlf"])
		}
	})

	t.Run("uneven block indentation is surfaced", func(t *testing.T) {
		h, flow, _, seeded, clientset := wholeResourceEditorHarness(t)
		edited := strings.Replace(seeded, "multi: |-\n  a\n  b", "multi: |-\n    a\n      b", 1)
		if edited == seeded {
			t.Fatal("multi-line block was not replaced")
		}
		writeFlowFile(t, flow, edited)
		h.send(editorFinishedMsg{})
		if flow.phase != phaseDiff || flow.editedMap["multi"] != "a\n  b" {
			t.Fatalf("uneven indentation state = phase %d value %q", flow.phase, flow.editedMap["multi"])
		}
		if countUpdateActions(clientset) != 0 {
			t.Fatalf("unconfirmed edit recorded %d update actions", countUpdateActions(clientset))
		}
	})
}

func TestSingleKeyEditorFidelity(t *testing.T) {
	tests := []struct {
		name             string
		original, edited string
		want             string
		wantUpdate       bool
	}{
		{name: "editor newline on value without newline", original: "secret-value", edited: "secret-value\n", want: "secret-value"},
		{name: "second newline is intentional", original: "secret-value\n", edited: "secret-value\n\n", want: "secret-value\n\n", wantUpdate: true},
		{name: "changed value drops editor newline", original: "secret-value", edited: "new-value\n", want: "new-value", wantUpdate: true},
		{name: "trailing space survives no-op", original: "abc ", edited: "abc ", want: "abc "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"key": []byte(test.original)})
			h := keyHarness(t, resource)
			h.keys("enter")
			flow := topEditFlow(t, h)
			clientset := flow.client.Clientset.(*fake.Clientset)
			clientset.ClearActions()
			writeFlowFile(t, flow, test.edited)
			h.send(editorFinishedMsg{})

			if !test.wantUpdate {
				value, err := resource.Get("key")
				if err != nil || string(value) != test.want {
					t.Fatalf("resource value = %q, %v; want %q", value, err, test.want)
				}
				if countUpdateActions(clientset) != 0 {
					t.Fatalf("no-op edit recorded %d update actions", countUpdateActions(clientset))
				}
				return
			}

			if flow.phase != phaseDiff || string(flow.edited) != test.want {
				t.Fatalf("edited state = phase %d value %q, want %q", flow.phase, flow.edited, test.want)
			}
			h.keys("Y")
			h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
			passCommitGate(h)
			updates := recordedSecretData(t, clientset)
			assertRecordedSecretUpdatesEqual(t, updates)
			if got := updates.persisted["key"]; !bytes.Equal(got, []byte(test.want)) {
				t.Fatalf("recorded value = %q, want %q", got, test.want)
			}
		})
	}
}

func wholeResourceEditorHarness(t *testing.T) (*harness, *editFlow, k8s.Resource, string, *fake.Clientset) {
	t.Helper()
	resource := testSecret(corev1.SecretTypeOpaque, editorFidelityValues())
	h := keyHarness(t, resource)
	h.keys("e")
	flow := topEditFlow(t, h)
	document, err := os.ReadFile(flow.filePath)
	if err != nil {
		t.Fatal(err)
	}
	clientset := flow.client.Clientset.(*fake.Clientset)
	clientset.ClearActions()
	return h, flow, resource, string(document), clientset
}

func editorFidelityValues() map[string][]byte {
	return map[string][]byte{
		"TENANTS": []byte(`[{"id":"district-sodervik","organization_id":"org_0TEST"}]`),
		"plain":   []byte("value"),
		"multi":   []byte("a\nb"),
		"crlf":    []byte("a\r\nb"),
		"unicode": []byte("höj 😀"),
	}
}

func reindentBlockContent(document string) string {
	lines := strings.Split(document, "\n")
	inBlock := false
	for index, line := range lines {
		if strings.Contains(line, ": |") {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if strings.HasPrefix(line, "  ") {
			lines[index] = "  " + line
			continue
		}
		if line != "" {
			inBlock = false
		}
	}
	return strings.Join(lines, "\n")
}

func countUpdateActions(clientset *fake.Clientset) int {
	count := 0
	for _, action := range clientset.Actions() {
		if _, ok := action.(clienttesting.UpdateAction); ok {
			count++
		}
	}
	return count
}

type recordedSecretUpdates struct {
	dryRun    map[string][]byte
	persisted map[string][]byte
}

func recordedSecretData(t *testing.T, clientset *fake.Clientset) recordedSecretUpdates {
	t.Helper()
	var recorded recordedSecretUpdates
	for _, action := range clientset.Actions() {
		update, ok := action.(clienttesting.UpdateActionImpl)
		if !ok {
			continue
		}
		secret, ok := update.GetObject().(*corev1.Secret)
		if !ok {
			t.Fatalf("updated object = %T, want *corev1.Secret", update.GetObject())
		}
		if len(update.GetUpdateOptions().DryRun) != 0 {
			if recorded.dryRun != nil {
				t.Fatal("client recorded multiple dry-run updates")
			}
			recorded.dryRun = secret.Data
			continue
		}
		if recorded.persisted != nil {
			t.Fatal("client recorded multiple persisted updates")
		}
		recorded.persisted = secret.Data
	}
	if recorded.dryRun == nil || recorded.persisted == nil {
		t.Fatalf("client updates = dry-run %t, persisted %t; want both", recorded.dryRun != nil, recorded.persisted != nil)
	}
	return recorded
}

func assertRecordedSecretUpdatesEqual(t *testing.T, updates recordedSecretUpdates) {
	t.Helper()
	if !maps.EqualFunc(updates.dryRun, updates.persisted, bytes.Equal) {
		t.Fatalf("dry-run payload = %q, persisted payload = %q; want identical", updates.dryRun, updates.persisted)
	}
}
