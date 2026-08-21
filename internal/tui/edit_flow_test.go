package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/NoahHakansson/sk64/internal/editor"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/undo"
	"github.com/charmbracelet/x/ansi"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestGolden_EditFlowDiff(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old line\nsecond"), []byte("new line\nsecond"))
	flow.client.Server = longPathPrefixedTestServer
	h.send(tea.WindowSizeMsg{Width: 60, Height: 24})
	h.golden("edit_flow_diff")
}

func TestGolden_EditFlowBinaryDiff(t *testing.T) {
	h, _ := proposedFlowHarness(t, []byte("old"), []byte{0, 1, 2, 3})
	if strings.Contains(h.view(), string([]byte{0, 1, 2, 3})) {
		t.Fatal("binary diff contains raw bytes")
	}
	h.golden("edit_flow_binary_diff")
}

func TestEditConfirmationsKeepCommitIdentityAtSupportedSizes(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		newFlow   func(*k8s.Client, *styles) *editFlow
	}{
		{
			name:      "save",
			operation: "Save key DB_PASSWORD",
			newFlow: func(client *k8s.Client, st *styles) *editFlow {
				return newEditFlow(t.Context(), client, editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", []byte("new"), st)
			},
		},
		{
			name:      "delete",
			operation: "Delete key DB_PASSWORD",
			newFlow: func(client *k8s.Client, st *styles) *editFlow {
				return newKeyDeleteFlow(t.Context(), client, editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", st)
			},
		},
		{
			name:      "create",
			operation: "Create ConfigMap default/new-config",
			newFlow: func(client *k8s.Client, st *styles) *editFlow {
				resource := k8s.NewConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "new-config", Namespace: "default"}})
				flow := newResourceCreateFlow(t.Context(), client, editEnv{}, resource, st)
				flow.editedMap = map[string]string{"key": "value"}
				flow.phase = phaseDiff
				flow.refreshContent()
				return flow
			},
		},
	}
	for _, test := range tests {
		for _, size := range []struct {
			name          string
			width, height int
		}{
			{name: "normal", width: 80, height: 22},
			{name: "minimum", width: 60, height: 13},
		} {
			t.Run(test.name+"/"+size.name, func(t *testing.T) {
				client := testClient()
				flow := test.newFlow(client, testStyles(true))
				flow.SetSize(size.width, size.height)
				view := ansi.Strip(flow.View())
				for _, want := range []string{test.operation, "default/", "context " + client.Context, "server " + client.Server} {
					if !strings.Contains(view, want) {
						t.Fatalf("%s confirmation at %dx%d lost %q:\n%s", test.name, size.width, size.height, want, view)
					}
				}
			})
		}
	}
}

func TestCommitSurfacesDistinguishPathPrefixedServers(t *testing.T) {
	surfaces := []struct {
		name   string
		render func(*k8s.Client, int, int) string
	}{
		{
			name: "delete",
			render: func(client *k8s.Client, width, height int) string {
				prompt := newDeleteConfirm(t.Context(), client, k8s.KindSecret, "default", "app-credentials", testStyles(true))
				prompt.res = deleteTestSecret(false)
				prompt.radiusSummary = summarizeBlastRadius(k8s.NewRefIndex(), prompt.kind, prompt.name)
				prompt.SetSize(width, height)
				return prompt.View()
			},
		},
		{
			name: "save diff header",
			render: func(client *k8s.Client, width, height int) string {
				flow := newEditFlow(t.Context(), client, editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", []byte("new"), testStyles(true))
				flow.SetSize(width, height)
				return strings.Join(flow.diffHeader(), "\n")
			},
		},
		{
			name: "create",
			render: func(client *k8s.Client, width, height int) string {
				prompt := newCreatePrompt(t.Context(), client, editEnv{}, "default", nil, testStyles(true))
				prompt.kind = k8s.KindSecret
				prompt.step = stepName
				prompt.input.SetValue("new-secret")
				prompt.SetSize(width, height)
				return prompt.View()
			},
		},
		{
			name: "plaintext export",
			render: func(client *k8s.Client, width, height int) string {
				prompt := newFilePrompt(t.Context(), client, editEnv{}, editSecret("10", []byte("raw value")), "DB_PASSWORD", fileExport, testStyles(true))
				prompt.SetSize(width, height)
				return prompt.View()
			},
		},
	}
	servers := []string{
		"https://gateway.example/clusters/one",
		"https://gateway.example/clusters/two",
	}
	for _, surface := range surfaces {
		for _, size := range []struct {
			name          string
			width, height int
		}{
			{name: "60 columns", width: 60, height: 13},
			{name: "100 columns", width: 100, height: 22},
		} {
			t.Run(surface.name+"/"+size.name, func(t *testing.T) {
				views := make([]string, len(servers))
				for i, server := range servers {
					client := testClient()
					client.Server = server
					views[i] = ansi.Strip(surface.render(client, size.width, size.height))
					if !strings.Contains(views[i], "server "+server) {
						t.Fatalf("surface lost full server %q:\n%s", server, views[i])
					}
					assertRenderedLinesFitWidth(t, views[i], size.width)
				}
				if views[0] == views[1] {
					t.Fatalf("servers with different path prefixes rendered identically:\n%s", views[0])
				}
			})
		}
	}
}

func TestWriteCommitSurfacesWrapServerLosslessly(t *testing.T) {
	server := "https://api.production.example:65535/clusters/production/a/very/long/apiserver/path"
	surfaces := []struct {
		name   string
		render func(*k8s.Client, *styles, int, int) (string, int, string)
	}{
		{
			name: "save diff",
			render: func(client *k8s.Client, st *styles, width, height int) (string, int, string) {
				flow := newEditFlow(t.Context(), client, editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", []byte("new"), st)
				flow.SetSize(width, height)
				return strings.Join(flow.diffHeader(), "\n"), width, plainFooter(t, flow, 1)
			},
		},
		{
			name: "conflict",
			render: func(client *k8s.Client, st *styles, width, height int) (string, int, string) {
				flow := newEditFlow(t.Context(), client, editEnv{}, editSecret("11", []byte("cluster")), "DB_PASSWORD", []byte("mine"), st)
				flow.phase = phaseConflict
				flow.SetSize(width, height)
				return strings.Join(flow.diffHeader(), "\n"), width, plainFooter(t, flow, 1)
			},
		},
		{
			name: "delete",
			render: func(client *k8s.Client, st *styles, width, height int) (string, int, string) {
				prompt := newDeleteConfirm(t.Context(), client, k8s.KindSecret, "default", "app-credentials", st)
				prompt.res = deleteTestSecret(false)
				prompt.radiusSummary = summarizeBlastRadius(k8s.NewRefIndex(), prompt.kind, prompt.name)
				prompt.SetSize(width, height)
				return prompt.View(), prompt.contentWidth(), plainFooter(t, prompt, 1)
			},
		},
		{
			name: "create",
			render: func(client *k8s.Client, st *styles, width, height int) (string, int, string) {
				prompt := newCreatePrompt(t.Context(), client, editEnv{}, "default", nil, st)
				prompt.kind = k8s.KindSecret
				prompt.step = stepName
				prompt.input.SetValue("new-secret")
				prompt.SetSize(width, height)
				return prompt.View(), prompt.contentWidth(), plainFooter(t, prompt, 1)
			},
		},
		{
			name: "file import",
			render: func(client *k8s.Client, st *styles, width, height int) (string, int, string) {
				prompt := newFilePrompt(t.Context(), client, editEnv{}, editSecret("10", []byte("raw value")), "DB_PASSWORD", fileImport, st)
				prompt.SetSize(width, height)
				return prompt.View(), prompt.contentWidth(), plainFooter(t, prompt, 1)
			},
		},
		{
			name: "file export",
			render: func(client *k8s.Client, st *styles, width, height int) (string, int, string) {
				prompt := newFilePrompt(t.Context(), client, editEnv{}, editSecret("10", []byte("raw value")), "DB_PASSWORD", fileExport, st)
				prompt.SetSize(width, height)
				return prompt.View(), prompt.contentWidth(), plainFooter(t, prompt, 1)
			},
		},
	}

	for _, ascii := range []bool{true, false} {
		st := testStyles(ascii)
		mode := "unicode"
		if ascii {
			mode = "ascii"
		}
		for _, surface := range surfaces {
			for _, size := range []struct {
				name          string
				width, height int
			}{
				{name: "60x15 terminal body", width: 60, height: 13},
				{name: "80x24 terminal body", width: 80, height: 22},
			} {
				t.Run(mode+"/"+surface.name+"/"+size.name, func(t *testing.T) {
					client := testClient()
					client.Server = server
					view, identityWidth, hints := surface.render(client, st, size.width, size.height)
					plainView := ansi.Strip(view)
					compactView := strings.Join(strings.Fields(strings.ReplaceAll(plainView, "|", " ")), " ")
					assertRenderedLinesFitWidth(t, plainView, size.width)

					identity := clusterIdentityLines(client.Context, server, identityWidth, st.glyphs.separator)
					if got := reassembleClusterServer(t, identity); got != server {
						t.Fatalf("reassembled server = %q, want %q", got, server)
					}
					for _, line := range identity {
						if !strings.Contains(plainView, line) {
							t.Fatalf("%s at %dx%d lost identity line %q:\n%s", surface.name, size.width, size.height, line, plainView)
						}
					}
					for _, want := range []string{"Secret default/", "context " + client.Context} {
						if !strings.Contains(compactView, want) {
							t.Fatalf("%s at %dx%d lost %q:\n%s", surface.name, size.width, size.height, want, plainView)
						}
					}
					if surface.name == "delete" {
						for _, want := range []string{"type app-credentials to confirm", "confirm:"} {
							if !strings.Contains(compactView, want) {
								t.Fatalf("delete at %dx%d lost prompt %q:\n%s", size.width, size.height, want, plainView)
							}
						}
					} else if (surface.name == "save diff" || surface.name == "conflict") && !strings.Contains(hints, "Y ") {
						t.Fatalf("%s at %dx%d lost confirmation hint: %q", surface.name, size.width, size.height, hints)
					}
				})
			}
		}
	}
}

func TestDiffConfirmationWrapsLongTargetIdentityWithoutDroppingIt(t *testing.T) {
	namespace := strings.Repeat("team-", 8) + "prod"
	name := strings.Repeat("credentials-", 8) + "primary"
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, ResourceVersion: "10"},
		Data:       map[string][]byte{"key": []byte("old")},
	})
	flow := newEditFlow(t.Context(), testClient(), editEnv{}, resource, "key", []byte("new"), testStyles(true))
	flow.SetSize(60, 13)
	header := flow.diffHeader()
	plainLines := strings.Split(ansi.Strip(strings.Join(header, "\n")), "\n")
	for i := range plainLines {
		plainLines[i] = strings.TrimRight(plainLines[i], " ")
	}
	joined := strings.Join(plainLines, "")
	for _, want := range []string{"Secret", namespace + "/" + name, "context test-ctx", "server https://test.example"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("wrapped diff header lost %q:\n%s", want, strings.Join(header, "\n"))
		}
	}
	for i, line := range header {
		if width := ansi.StringWidth(line); width > 60 {
			t.Fatalf("header line %d width = %d, want <= 60: %q", i, width, line)
		}
	}
}

func TestEditDialogPhasesKeepTargetIdentityAndOperation(t *testing.T) {
	tests := []struct {
		name      string
		phase     editPhase
		operation string
		prepare   func(*editFlow)
	}{
		{name: "editor", phase: phaseEditing, operation: "edit"},
		{name: "dry-run", phase: phaseDryRun, operation: "dry-run save"},
		{name: "rejected", phase: phaseDryRunRejected, operation: "dry-run save"},
		{name: "unsupported", phase: phaseDryRunUnsupported, operation: "save"},
		{name: "forbidden", phase: phaseForbidden, operation: "save"},
		{name: "validation", phase: phaseValidateWarn, operation: "save", prepare: func(flow *editFlow) {
			flow.warnings = []k8s.Warning{"validation warning"}
		}},
		{name: "saving", phase: phaseSaving, operation: "save"},
		{name: "binary collision", phase: phaseBinaryCollision, operation: "edit"},
		{name: "rollout offer", phase: phaseSaved, operation: "restart", prepare: func(flow *editFlow) {
			flow.radiusLoader.stop()
			flow.radius = k8s.NewRefIndex()
			flow.rollout = []rolloutItem{{kind: k8s.KindDeployment, name: "web", selected: true}}
		}},
		{name: "rolling out", phase: phaseRollingOut, operation: "restart"},
		{name: "rollout done", phase: phaseRolloutDone, operation: "restart", prepare: func(flow *editFlow) {
			flow.rolloutResults = []rolloutResult{{kind: k8s.KindDeployment, name: "web"}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient()
			flow := newEditFlow(t.Context(), client, editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", []byte("new"), testStyles(true))
			flow.phase = test.phase
			flow.message = "operation detail"
			if test.prepare != nil {
				test.prepare(flow)
			}
			flow.SetSize(80, 22)
			flow.refreshContent()
			view := ansi.Strip(flow.View())
			for _, want := range []string{"Secret default/db-creds", "key DB_PASSWORD", "context " + client.Context, "server " + client.Server, test.operation} {
				if !strings.Contains(strings.ToLower(view), strings.ToLower(want)) {
					t.Fatalf("%s phase lost %q:\n%s", test.name, want, view)
				}
			}
		})
	}
}

func TestASCIIFramePreservesResourceGlyphBytes(t *testing.T) {
	const value = "literal — separator and … ellipsis"
	newResource := func() k8s.Resource {
		return k8s.NewConfigMap(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "glyph-config", Namespace: "default", ResourceVersion: "10"},
			Data:       map[string]string{"message": value},
		})
	}

	tests := []struct {
		name   string
		render func(*testing.T) string
	}{
		{
			name: "key list value filter",
			render: func(t *testing.T) string {
				h := keyHarnessOptions(t, newResource(), Options{StartNamespace: "default", ASCII: true, ReadOnly: true})
				h.keys("v", "/")
				for _, char := range value {
					h.send(key(string(char)))
				}
				return ansi.Strip(h.m.View().Content)
			},
		},
		{
			name: "value viewer",
			render: func(t *testing.T) string {
				h := keyHarnessOptions(t, newResource(), Options{StartNamespace: "default", ASCII: true, ReadOnly: true})
				h.keys("enter")
				return ansi.Strip(h.m.View().Content)
			},
		},
		{
			name: "pre-save diff",
			render: func(t *testing.T) string {
				h := newHarness(t, Options{ASCII: true})
				flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, newResource(), "message", []byte(value+" updated"), h.m.(app).styles)
				h.send(pushScreenMsg{s: flow})
				return ansi.Strip(h.m.View().Content)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := test.render(t)
			if !strings.Contains(view, value) {
				t.Fatalf("ASCII frame rewrote resource bytes %q:\n%s", value, view)
			}
			chromeAndLabels := strings.ReplaceAll(view, value, "")
			for _, rendered := range chromeAndLabels {
				if rendered > 0x7f {
					t.Fatalf("ASCII chrome rendered non-ASCII rune %q:\n%s", rendered, view)
				}
			}
		})
	}
}

func TestDiffPhaseKeepsFullWidthViewport(t *testing.T) {
	longLine := strings.Repeat("x", 150)
	h, flow := proposedFlowHarness(t, []byte("old"), []byte(longLine))
	h.send(tea.WindowSizeMsg{Width: 200, Height: 40})

	if width := flow.viewport.Width(); width != 200 {
		t.Fatalf("diff viewport width = %d, want 200", width)
	}
	if view := h.view(); !strings.Contains(view, longLine) {
		t.Fatalf("full-width diff lost long line:\n%s", view)
	}
}

func TestDiffHeaderSplitsMultilineMessages(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	flow.message = "line one\nline two\nline three"
	flow.refreshContent()

	view := h.view()
	if lines := strings.Count(view, "\n") + 1; lines != 24 {
		t.Fatalf("view height = %d lines, want 24:\n%s", lines, view)
	}
	for _, line := range strings.Split(flow.message, "\n") {
		if !strings.Contains(view, line) {
			t.Fatalf("view lost header line %q:\n%s", line, view)
		}
	}
	if last := strings.Split(view, "\n")[23]; !strings.Contains(last, "Y save") {
		t.Fatalf("bottom row = %q, want hint line", last)
	}
}

func TestDiffWrapToggle(t *testing.T) {
	for _, test := range []struct {
		name  string
		phase editPhase
	}{
		{name: "diff", phase: phaseDiff},
		{name: "conflict", phase: phaseConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
			flow.phase = test.phase
			flow.refreshContent()

			if flow.wrap || flow.viewport.SoftWrap {
				t.Fatalf("initial wrap state = %t viewport %t, want both false", flow.wrap, flow.viewport.SoftWrap)
			}
			if hints := plainFooter(t, flow, 1); !strings.Contains(hints, "w wrap") {
				t.Fatalf("initial Hints() = %q, want w wrap", hints)
			}

			h.keys("w")
			if !flow.wrap || !flow.viewport.SoftWrap {
				t.Fatalf("enabled wrap state = %t viewport %t, want both true", flow.wrap, flow.viewport.SoftWrap)
			}
			if hints := plainFooter(t, flow, 1); !strings.Contains(hints, "w unwrap") || strings.Contains(hints, "w wrap ") {
				t.Fatalf("enabled Hints() = %q, want w unwrap only", hints)
			}

			h.keys("w")
			if flow.wrap || flow.viewport.SoftWrap {
				t.Fatalf("disabled wrap state = %t viewport %t, want both false", flow.wrap, flow.viewport.SoftWrap)
			}
		})
	}
}

func TestWrappedDiffShowsWholeValueWithinWidth(t *testing.T) {
	value := strings.Repeat("abcdefghij", 30)
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"LONG_VALUE": []byte(value)})
	h := keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: true})
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.keys("D")
	if view := h.view(); strings.Contains(view, "-"+value) {
		t.Fatalf("unwrapped view unexpectedly contains the complete value:\n%s", view)
	}

	h.keys("w")
	view := h.view()
	for _, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 80 {
			t.Fatalf("wrapped line width = %d, want <= 80: %q", width, line)
		}
	}
	var rendered strings.Builder
	collecting := false
	for _, line := range strings.Split(view, "\n") {
		switch {
		case strings.HasPrefix(line, "  -"+value[:10]):
			collecting = true
			rendered.WriteString(line[diffGutterWidth:])
		case collecting && strings.HasPrefix(line, "> "):
			rendered.WriteString(line[diffGutterWidth:])
		case collecting:
			collecting = false
		}
	}
	if got, want := rendered.String(), "-"+value; got != want {
		t.Fatalf("wrapped value = %q, want %q", got, want)
	}
}

func TestWrappedDiffRowsCarryTheirOwnColour(t *testing.T) {
	value := `[{"id":"district-sodervik","organization_id":"org_0TESTPLACEHOLD01","name":"District Södervik"},{"id":"district-vasterlid","organization_id":"org_0TESTPLACEHOLD02","name":"District Västerlid"},{"id":"district-osterdal","organization_id":"org_0TESTPLACEHOLD03","name":"District Österdal"}]`
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"TENANTS": []byte(value)})
	h := keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: true})
	h.send(tea.WindowSizeMsg{Width: 100, Height: 30})
	h.keys("D", "w")
	flow := topEditFlow(t, h)

	red, _, _ := strings.Cut(flow.styles.diffDel.Render("x"), "x")
	continuations := 0
	for _, line := range strings.Split(h.m.View().Content, "\n") {
		if strings.HasPrefix(ansi.Strip(line), flow.styles.glyphs.wrapMarker+" ") {
			continuations++
			if !strings.Contains(line, red) {
				t.Fatalf("continuation row lacks its own deletion colour: %q", line)
			}
		}
	}
	if continuations == 0 {
		t.Fatal("wrapped diff has no continuation rows")
	}
}

func TestDiffContinuationFragmentKeepsItsLineColour(t *testing.T) {
	value := strings.Repeat("a", 77) + "-1,-2,-3"
	h, flow := proposedFlowHarness(t, nil, []byte(value))
	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.keys("w")

	green, _, _ := strings.Cut(flow.styles.diffAdd.Render("x"), "x")
	red, _, _ := strings.Cut(flow.styles.diffDel.Render("x"), "x")
	for _, line := range strings.Split(h.m.View().Content, "\n") {
		if strings.HasPrefix(ansi.Strip(line), flow.styles.glyphs.wrapMarker+" -1,-2,-3") {
			if !strings.Contains(line, green) || strings.Contains(line, red) {
				t.Fatalf("addition continuation has wrong colour: %q", line)
			}
			return
		}
	}
	t.Fatal("addition continuation row not found")
}

func TestDiffScrollAnchorSurvivesWrapToggle(t *testing.T) {
	oldLines := make([]string, 40)
	newLines := make([]string, 40)
	for i := range oldLines {
		oldLines[i] = fmt.Sprintf("line %02d old %s", i, strings.Repeat("x", 20))
		newLines[i] = fmt.Sprintf("line %02d new %s", i, strings.Repeat("y", 20))
	}
	oldLines[20] += wrapSampleValue
	h, _ := proposedFlowHarness(t, []byte(strings.Join(oldLines, "\n")), []byte(strings.Join(newLines, "\n")))
	h.send(tea.WindowSizeMsg{Width: 100, Height: 20})
	keys := make([]string, 45)
	for i := range keys {
		keys[i] = "down"
	}
	h.keys(keys...)
	topRow := func(wrapState string) string {
		t.Helper()
		lines := strings.Split(h.view(), "\n")
		ruleIndex := slices.IndexFunc(lines, func(line string) bool {
			return strings.Contains(ansi.Strip(line), "wrap "+wrapState+"  ")
		})
		if ruleIndex < 0 || ruleIndex+1 >= len(lines) || !strings.Contains(lines[ruleIndex], "%") {
			t.Fatalf("diff rule has no scroll percentage for wrap %s:\n%s", wrapState, h.view())
		}
		return lines[ruleIndex+1]
	}
	top := topRow("off")

	h.keys("w")
	if got := topRow("on"); got != top {
		t.Fatalf("wrapped top row = %q, want %q", got, top)
	}
	h.keys("w")
	if got := topRow("off"); got != top {
		t.Fatalf("unwrapped top row = %q, want %q", got, top)
	}
}

func TestStyledDiffClassifiesMetaAndBodyLines(t *testing.T) {
	st := testStyles(false)
	inputs := []string{
		"--- TLS_KEY (cluster)",
		"+++ TLS_KEY (deleted)",
		"@@ -1,2 +0,0 @@",
		"-----BEGIN CERTIFICATE-----",
		"-MIIBogIBAAJBAK",
		"+replacement",
		"  context",
		`\ No newline at end of file`,
	}
	wants := []string{
		st.dim.Render(inputs[0]),
		st.dim.Render(inputs[1]),
		st.tag.Render(inputs[2]),
		st.diffDel.Render(inputs[3]),
		st.diffDel.Render(inputs[4]),
		st.diffAdd.Render(inputs[5]),
		inputs[6],
		st.dim.Render(inputs[7]),
	}
	got := strings.Split(styledDiff(strings.Join(inputs, "\n"), st), "\n")
	for i, want := range wants {
		t.Run(fmt.Sprintf("line_%d", i), func(t *testing.T) {
			if got[i] != want {
				t.Fatalf("styled line = %q, want %q", got[i], want)
			}
		})
	}
}

func TestDiffHeaderPluralisesRemovedKeys(t *testing.T) {
	for _, test := range []struct {
		name     string
		original map[string]string
		edited   map[string]string
		want     string
		unwanted string
	}{
		{name: "singular", original: map[string]string{"one": "1"}, edited: map[string]string{}, want: "this edit removes 1 key", unwanted: "1 keys"},
		{name: "plural", original: map[string]string{"one": "1", "two": "2"}, edited: map[string]string{}, want: "this edit removes 2 keys"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, Options{ASCII: true})
			flow := newResourceEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, testSecret(corev1.SecretTypeOpaque, nil), h.m.(app).styles)
			flow.phase = phaseDiff
			flow.originalMap = test.original
			flow.editedMap = test.edited
			header := strings.Join(flow.diffHeader(), "\n")
			if !strings.Contains(header, test.want) || test.unwanted != "" && strings.Contains(header, test.unwanted) {
				t.Fatalf("diff header = %q, want %q without %q", header, test.want, test.unwanted)
			}
		})
	}
}

func TestLowercaseAcceptKeysDoNotConfirm(t *testing.T) {
	tests := []struct {
		name      string
		phase     editPhase
		target    flowTarget
		wantPhase editPhase
	}{
		{name: "save diff", phase: phaseDiff, target: targetKey, wantPhase: phaseDryRun},
		{name: "delete diff", phase: phaseDiff, target: targetDeleteKey, wantPhase: phaseDryRun},
		{name: "validation warning", phase: phaseValidateWarn, target: targetKey, wantPhase: phaseDryRun},
		{name: "conflict", phase: phaseConflict, target: targetKey, wantPhase: phaseDryRun},
		{name: "dry-run unsupported", phase: phaseDryRunUnsupported, target: targetKey, wantPhase: phaseCommitGate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
			flow.phase = test.phase
			flow.target = test.target
			flow.refreshContent()
			clientset := flow.client.Clientset.(*fake.Clientset)
			clientset.ClearActions()

			h.keys("y")
			if flow.phase != test.phase || !flow.nudge || len(clientset.Actions()) != 0 {
				t.Fatalf("lowercase y = phase %d nudge %t actions %d", flow.phase, flow.nudge, len(clientset.Actions()))
			}
			if view := h.view(); !strings.Contains(view, pressYToConfirm) {
				t.Fatalf("lowercase y view has no confirmation nudge:\n%s", view)
			}
			h.keys("enter")
			if flow.phase != test.phase || !flow.nudge || len(clientset.Actions()) != 0 {
				t.Fatalf("enter = phase %d nudge %t actions %d", flow.phase, flow.nudge, len(clientset.Actions()))
			}

			h.keys("Y")
			if flow.phase != test.wantPhase {
				t.Fatalf("uppercase Y phase = %d, want %d", flow.phase, test.wantPhase)
			}
		})
	}
}

func TestLowercaseKeyManagementIsInert(t *testing.T) {
	h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"existing": []byte("value")}))
	depth := len(h.m.(app).stack)

	h.keys("n")
	if len(h.m.(app).stack) != depth {
		t.Fatalf("lowercase n stack depth = %d, want %d", len(h.m.(app).stack), depth)
	}
	if _, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*keyScreen); !ok {
		t.Fatalf("lowercase n top screen = %T, want keyScreen", h.m.(app).stack[len(h.m.(app).stack)-1])
	}
	h.keys("d")
	if len(h.m.(app).stack) != depth {
		t.Fatalf("lowercase d stack depth = %d, want %d", len(h.m.(app).stack), depth)
	}
	if _, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*keyScreen); !ok {
		t.Fatalf("lowercase d top screen = %T, want keyScreen", h.m.(app).stack[len(h.m.(app).stack)-1])
	}

	h.keys("N")
	if len(h.m.(app).stack) != depth+1 {
		t.Fatalf("uppercase N stack depth = %d, want %d", len(h.m.(app).stack), depth+1)
	}
	if _, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*keyNamePrompt); !ok {
		t.Fatalf("uppercase N top screen = %T, want keyNamePrompt", h.m.(app).stack[len(h.m.(app).stack)-1])
	}
	h.keys("esc", "D")
	if len(h.m.(app).stack) != depth+1 {
		t.Fatalf("uppercase D stack depth = %d, want %d", len(h.m.(app).stack), depth+1)
	}
	flow, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*editFlow)
	if !ok || flow.target != targetDeleteKey {
		t.Fatalf("uppercase D top screen = %T target %v, want delete editFlow", h.m.(app).stack[len(h.m.(app).stack)-1], flow)
	}
}

func TestGolden_EditFlowDryRunRejected(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: h.topReqID(), result: k8s.DryRunResult{Outcome: k8s.DryRunRejected, Message: `admission webhook "policy" denied the request`}})
	h.golden("edit_flow_dry_run_rejected")
	h.keys("esc")
	value, err := flow.res.Get(flow.key)
	if err != nil || string(value) != "old" {
		t.Fatalf("resource after rejected dry-run abort = %q, err = %v", value, err)
	}
}

func TestGolden_EditFlowDryRunUnsupported(t *testing.T) {
	h, _ := proposedFlowHarness(t, []byte("old"), []byte("new"))
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: h.topReqID(), result: k8s.DryRunResult{Outcome: k8s.DryRunUnsupported, Message: `admission webhook "legacy" does not support dry run`}})
	h.golden("edit_flow_dry_run_unsupported")
}

func TestEditFlowDryRunUnsupportedProceedsWithoutDryRun(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	h.keys("Y")
	h.send(dryRunDoneMsg{
		reqID: flow.reqID,
		result: k8s.DryRunResult{
			Outcome: k8s.DryRunUnsupported,
			Message: `admission webhook "legacy" does not support dry run`,
		},
	})
	clientset := flow.client.Clientset.(*fake.Clientset)
	clientset.ClearActions()

	h.keys("Y")
	passCommitGate(h)

	if flow.phase != phaseSaving || !flow.pending {
		t.Fatalf("proceed state = phase %d pending %t, want saving and pending", flow.phase, flow.pending)
	}
	actions := clientset.Actions()
	if len(actions) != 1 {
		t.Fatalf("proceed client actions = %d, want 1", len(actions))
	}
	update, ok := actions[0].(clienttesting.UpdateActionImpl)
	if !ok {
		t.Fatalf("proceed client action = %T, want testing.UpdateActionImpl", actions[0])
	}
	if dryRun := update.GetUpdateOptions().DryRun; len(dryRun) != 0 {
		t.Fatalf("proceed update dry-run options = %v, want none", dryRun)
	}
}

func TestEditFlowDryRunConflict(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("mine"))
	h.keys("Y")

	h.send(dryRunDoneMsg{
		reqID: flow.reqID,
		result: k8s.DryRunResult{
			Outcome: k8s.DryRunConflict,
			Message: "resource changed",
			Cluster: editSecret("11", []byte("cluster")),
		},
	})

	if flow.phase != phaseConflict || flow.res.ResourceVersion() != "11" || string(flow.original) != "cluster" || string(flow.edited) != "mine" {
		t.Fatalf(
			"dry-run conflict state = phase %d rv %q original %q edited %q",
			flow.phase,
			flow.res.ResourceVersion(),
			flow.original,
			flow.edited,
		)
	}
}

func TestGolden_EditFlowConflict(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("mine"))
	shared := flow.res
	enterSaving(h)
	h.send(saveDoneMsg{reqID: h.topReqID(), result: k8s.SaveResult{Outcome: k8s.SaveConflict, Cluster: editSecret("11", []byte("cluster"))}})
	h.golden("edit_flow_conflict")
	value, err := shared.Get(flow.key)
	if err != nil || string(value) != "old" {
		t.Fatalf("shared resource after conflict = %q, err = %v", value, err)
	}
}

func TestGolden_BlastRadiusLine(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	h.send(tea.WindowSizeMsg{Width: 60, Height: 24})
	index := k8s.NewRefIndex()
	index.AddWorkload(k8s.Workload{Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Spec: podSpecWithRef("db-creds", k8s.TagEnv)})
	index.AddWorkload(k8s.Workload{Kind: k8s.KindStatefulSet, Name: "database", Namespace: "default", Spec: podSpecWithRef("db-creds", k8s.TagVolume)})
	index.AddPod(podWithRef("operator-pod", "db-creds", k8s.TagVolume))
	index.AddSourceError("pods")
	h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, index: index})
	view := h.view()
	for _, want := range []string{"[incomplete]", "pods not listable", "\n  --- DB_PASSWORD (cluster)"} {
		if !strings.Contains(view, want) {
			t.Fatalf("blast-radius view lost %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "2 workloads and 1 pod") {
		t.Fatalf("blast-radius counts did not yield before the caveat:\n%s", view)
	}
	assertRenderedLinesFitWidth(t, view, 60)
	h.golden("blast_radius_line")
}

func TestValidationWarningPreservesCompleteBlastRadiusWithoutNotes(t *testing.T) {
	_, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	flow.radiusLoader.stop()
	flow.radiusSummary = blastRadius{
		known:           true,
		workloads:       125,
		env:             100,
		pods:            44,
		serviceAccounts: 12,
	}
	flow.phase = phaseValidateWarn
	flow.warnings = []k8s.Warning{"validation warning"}
	flow.SetSize(60, 24)

	want := flow.radiusSummary.line()
	content := flow.dialogContent()
	if len(content.body) != 1 || content.body[0] != want {
		t.Fatalf("validation blast-radius body = %q, want %q", content.body, want)
	}
	view := ansi.Strip(flow.View())
	for _, detail := range []string{"125 workloads", "44 pods", "serviceaccounts", "100 via env"} {
		if !strings.Contains(view, detail) {
			t.Fatalf("validation dialog lost %q while wrapping blast radius:\n%s", detail, view)
		}
	}
	assertRenderedLinesFitWidth(t, view, 60)
}

func TestGolden_EditFlowForbidden(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("mine"))
	enterSaving(h)
	h.send(saveDoneMsg{reqID: h.topReqID(), result: k8s.SaveResult{Outcome: k8s.SaveForbidden, Message: "secrets db-creds is forbidden"}})
	h.golden("edit_flow_forbidden")
	h.keys("esc")
	value, err := flow.res.Get(flow.key)
	if err != nil || string(value) != "old" {
		t.Fatalf("resource after forbidden save abort = %q, err = %v", value, err)
	}
}

func TestGolden_EditorFailed(t *testing.T) {
	h := newHarness(t, Options{ASCII: true, Editor: "true"})
	flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, editSecret("10", []byte("old")), "DB_PASSWORD", nil, h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	h.send(editorFinishedMsg{err: errors.New("exit status 1: editor stderr")})
	h.golden("editor_failed")
}

func TestGolden_ExportPrompt(t *testing.T) {
	dir := filePromptFixture(t)
	t.Chdir(dir)
	h := keyHarness(t, editSecret("10", []byte("old")))
	h.m.(app).client.Server = longPathPrefixedTestServer
	h.send(tea.WindowSizeMsg{Width: 60, Height: 24})
	h.keys("x")
	h.golden("export_dir_picker")
}

func TestGolden_ExportNamePrompt(t *testing.T) {
	for _, test := range []struct {
		name   string
		ascii  bool
		golden string
	}{
		{name: "ASCII", ascii: true, golden: "export_name_prompt"},
		{name: "Unicode", golden: "export_name_prompt_unicode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t, Options{ASCII: test.ascii})
			prompt := newFilePrompt(t.Context(), h.m.(app).client, h.m.(app).editEnv, editSecret("10", []byte("old")), "DB_PASSWORD", fileExport, h.m.(app).styles)
			prompt.phase = filePhaseName
			prompt.dir = "/exports"
			prompt.stat = func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
			prompt.refreshNameFeedback()
			h.send(pushScreenMsg{s: prompt})
			h.send(tea.WindowSizeMsg{Width: 60, Height: 24})
			h.golden(test.golden)
		})
	}
}

func TestGolden_ImportPrompt(t *testing.T) {
	dir := importPromptFixture(t)
	t.Chdir(dir)
	h := keyHarness(t, editSecret("10", []byte("old")))
	h.send(tea.WindowSizeMsg{Width: 60, Height: 24})
	h.keys("i")
	h.golden("import_prompt")
}

func filePromptFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "configs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"notes.txt":   "notes\n",
		"values.yaml": "enabled: true\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func importPromptFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range map[string]string{
		"notes.txt":   "notes\n",
		"values.yaml": "enabled: true\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestGolden_KeyListReadOnly(t *testing.T) {
	h := keyHarnessOptions(t, editSecret("10", []byte("old")), Options{StartNamespace: "default", ASCII: true, ReadOnly: true})
	h.golden("key_list_read_only")
}

func TestGolden_KeyListEditable(t *testing.T) {
	h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"editable": []byte("value")}))
	h.golden("key_list_editable")
}

func TestGolden_ResourceEditDiff(t *testing.T) {
	h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{
		"delete": []byte("old"),
		"keep":   []byte("before"),
	}))
	h.keys("e")
	flow := topEditFlow(t, h)
	writeFlowFile(t, flow, "added: new\nkeep: after\n")
	h.send(editorFinishedMsg{})
	h.golden("resource_edit_diff")
}

func TestGolden_ResourceEditParseError(t *testing.T) {
	h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"port": []byte("text")}))
	h.keys("e")
	flow := topEditFlow(t, h)
	writeFlowFile(t, flow, "port: 8080\n")
	h.send(editorFinishedMsg{})
	h.golden("resource_edit_parse_error")
}

func TestGolden_ValidateWarn(t *testing.T) {
	resource := testSecret(corev1.SecretTypeTLS, map[string][]byte{corev1.TLSCertKey: []byte("old")})
	h := keyHarness(t, resource)
	flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, corev1.TLSCertKey, []byte("new"), h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	h.keys("Y")
	h.golden("validate_warn")
}

func TestGolden_KeyNamePrompt(t *testing.T) {
	h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"existing": []byte("value")}))
	h.keys("N")
	h.golden("key_name_prompt")
}

func TestGolden_DeleteKeyDiff(t *testing.T) {
	h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"obsolete": []byte("old")}))
	h.keys("D")
	h.golden("delete_key_diff")
}

const (
	wrapSampleASCIIValue = `[{"id":"district-sodervik","organization_id":"org_0TESTPLACEHOLD01","name":"District Sodervik"},{"id":"district-vasterlid","organization_id":"org_0TESTPLACEHOLD02","name":"District Vasterlid"}]`
	wrapSampleValue      = `[{"id":"district-sodervik","organization_id":"org_0TESTPLACEHOLD01","name":"District Södervik"},{"id":"district-vasterlid","organization_id":"org_0TESTPLACEHOLD02","name":"District Västerlid"}]`
)

func TestGolden_DiffWrapModes(t *testing.T) {
	for _, test := range []struct {
		name   string
		ascii  bool
		wrap   bool
		value  string
		golden string
	}{
		{name: "ascii wrap off", ascii: true, value: wrapSampleASCIIValue, golden: "delete_key_wrap_off"},
		{name: "ascii wrap on", ascii: true, wrap: true, value: wrapSampleASCIIValue, golden: "delete_key_wrap_on"},
		{name: "unicode wrap on", wrap: true, value: wrapSampleValue, golden: "delete_key_wrap_on_unicode"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"TENANTS": []byte(test.value)})
			h := keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: test.ascii})
			h.keys("D")
			if test.wrap {
				h.keys("w")
			}
			h.golden(test.golden)
		})
	}
}

func TestBlastRadiusLifecycle(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	if !flow.radiusLoader.pending {
		t.Fatal("blast-radius fetch did not start")
	}
	h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID - 1, index: k8s.NewRefIndex()})
	if !flow.radiusLoader.pending || flow.radius != nil {
		t.Fatal("stale blast-radius result changed the flow")
	}

	index := k8s.NewRefIndex()
	index.AddWorkload(k8s.Workload{Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Spec: podSpecWithRef("db-creds", k8s.TagEnv)})
	h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, index: index})
	if flow.radiusLoader.pending || flow.radius != index || !strings.Contains(h.view(), "consumed by 1 workload (1 via env)") {
		t.Fatalf("blast-radius result was not rendered: %q", h.view())
	}

	h.keys("Y")
	if flow.phase != phaseDryRun || flow.radius != index {
		t.Fatalf("dry-run state = phase %d radius %p, want phaseDryRun and radius %p", flow.phase, flow.radius, index)
	}
}

func TestRolloutOfferCommitKeys(t *testing.T) {
	tests := []struct {
		name        string
		configure   func(*editFlow)
		keys        []string
		wantPhase   editPhase
		wantPatches []string
	}{
		{
			name:      "queued save confirmation does not restart",
			keys:      []string{"Y"},
			wantPhase: phaseSaved,
		},
		{
			name: "restart commits selected subset",
			configure: func(flow *editFlow) {
				flow.rollout = append(flow.rollout, rolloutItem{kind: k8s.KindDeployment, name: "worker", selected: true})
				_ = flow.rolloutList.SetItems(flow.rolloutChecklistItems())
				flow.refreshContent()
			},
			keys:        []string{"down", "space", "R"},
			wantPhase:   phaseRollingOut,
			wantPatches: []string{"web"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, flow, _ := rolloutOfferHarness(t)
			if hints := plainFooter(t, flow, 1); hints != "space toggle  a all  R restart selected  esc skip" {
				t.Fatalf("restart hints = %q", hints)
			}
			if test.configure != nil {
				test.configure(flow)
			}

			h.keys(test.keys...)
			if test.wantPhase == phaseRollingOut {
				passCommitGate(h)
			}

			if flow.phase != test.wantPhase {
				t.Fatalf("restart key phase = %d, want %d", flow.phase, test.wantPhase)
			}
			if got := patchActionNames(h); !slices.Equal(got, test.wantPatches) {
				t.Fatalf("patched workloads = %v, want %v", got, test.wantPatches)
			}
		})
	}
}

func TestRolloutOfferAfterSave(t *testing.T) {
	t.Run("deselecting all skips patches", func(t *testing.T) {
		h, flow, keyScreen := rolloutOfferHarness(t)
		if len(flow.rollout) != 1 || flow.rollout[0].kind != k8s.KindDeployment || flow.rollout[0].name != "web" || !flow.rollout[0].selected {
			t.Fatalf("rollout candidates = %#v", flow.rollout)
		}
		if flow.capturesQuit() {
			t.Fatal("saved flow still captures quit")
		}
		if !keyScreen.pending {
			t.Fatal("editSavedMsg did not refresh the key screen")
		}
		if _, ok := keyScreen.env.ring.LatestFor("test-ctx", k8s.KindSecret, "default", "db-creds"); !ok {
			t.Fatal("save did not push an undo entry")
		}

		h.keys("space", "R")
		if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != keyScreen {
			t.Fatalf("deselecting all left top screen %T, want keyScreen", top)
		}
		if got := patchActionCount(h); got != 0 {
			t.Fatalf("deselecting all issued %d patch actions", got)
		}
	})

	const restartError = "restart forbidden"
	for _, test := range []struct {
		name       string
		results    []rolloutResult
		summary    string
		resultLine string
		wantNotice string
	}{
		{
			name:       "all success",
			results:    []rolloutResult{{kind: k8s.KindDeployment, name: "web"}},
			summary:    "1 restarted, 0 failed",
			resultLine: "[success] Deployment/web  restarted",
			wantNotice: "[success] saved Secret default/db-creds",
		},
		{
			name: "mixed failure",
			results: []rolloutResult{
				{kind: k8s.KindDeployment, name: "web"},
				{kind: k8s.KindStatefulSet, name: "database", err: errors.New(restartError)},
			},
			summary:    "1 restarted, 1 failed",
			resultLine: "[error] StatefulSet/database  failed: " + restartError,
			wantNotice: "[incomplete] saved Secret default/db-creds - workload restart incomplete",
		},
		{
			name:       "all failure",
			results:    []rolloutResult{{kind: k8s.KindDeployment, name: "web", err: errors.New(restartError)}},
			summary:    "0 restarted, 1 failed",
			resultLine: "[error] Deployment/web  failed: " + restartError,
			wantNotice: "[error] saved Secret default/db-creds - workload restart failed",
		},
	} {
		for _, key := range []string{"enter", "esc"} {
			t.Run(test.name+" "+key, func(t *testing.T) {
				h, flow, keyScreen := rolloutOfferHarness(t)
				h.keys("R")
				passCommitGate(h)
				if flow.phase != phaseRollingOut || patchActionCount(h) != 1 {
					t.Fatalf("rollout start = phase %d patches %d", flow.phase, patchActionCount(h))
				}
				h.send(rolloutDoneMsg{reqID: flow.reqID, results: test.results})
				view := h.view()
				if flow.phase != phaseRolloutDone || !strings.Contains(view, test.summary) || !strings.Contains(view, test.resultLine) {
					t.Fatalf("rollout result state = phase %d view %q", flow.phase, view)
				}
				if !strings.Contains(view, "enter done  esc close") || strings.Contains(view, "esc cancel") {
					t.Fatalf("rollout result footer implies cancellation: %q", view)
				}

				ordinaryNotice := "[success] saved Secret default/db-creds"
				if !keyScreen.pending || plainOutcomeNotice(keyScreen.outcome) != ordinaryNotice {
					t.Fatalf("saved state before dismissal = pending %t notice %q", keyScreen.pending, plainOutcomeNotice(keyScreen.outcome))
				}
				refreshReqID := keyScreen.reqID
				h.keys(key)
				if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != keyScreen {
					t.Fatalf("%s left top screen %T, want keyScreen", key, top)
				}
				if keyScreen.reqID != refreshReqID {
					t.Fatalf("%s dismissal started reload %d, want existing request %d", key, keyScreen.reqID, refreshReqID)
				}
				notice := plainOutcomeNotice(keyScreen.outcome)
				if !keyScreen.pending || notice != test.wantNotice || !strings.Contains(h.view(), test.wantNotice) {
					t.Fatalf("%s dismissal state = pending %t notice %q", key, keyScreen.pending, notice)
				}
				if strings.Contains(notice, restartError) {
					t.Fatalf("%s persistent notice contains rollout error detail", key)
				}
				resourceValue, err := flow.res.Get("DB_PASSWORD")
				if err != nil {
					t.Fatalf("read saved resource value: %v", err)
				}
				if strings.Contains(notice, string(resourceValue)) {
					t.Fatalf("%s persistent notice contains resource value bytes", key)
				}
				h.send(resourceLoadedMsg{reqID: refreshReqID, res: flow.res})
				if keyScreen.pending || plainOutcomeNotice(keyScreen.outcome) != test.wantNotice || !strings.Contains(h.view(), test.wantNotice) {
					t.Fatalf("%s reload lost saved notice:\n%s", key, h.view())
				}
			})
		}
	}
}

func TestRolloutResultRowsUseModeGlyphs(t *testing.T) {
	for _, ascii := range []bool{true, false} {
		st := testStyles(ascii)
		flow := &editFlow{
			dialog:         newDialog(st, false),
			rolloutResults: []rolloutResult{{kind: k8s.KindDeployment, name: "web"}, {kind: k8s.KindStatefulSet, name: "db", err: errors.New("forbidden")}},
		}
		lines := ansi.Strip(strings.Join(flow.rolloutResultLines(80), "\n"))
		for _, kind := range []stateLineKind{stateLineSuccess, stateLineError} {
			if !strings.Contains(lines, st.stateMarker(kind)+" ") {
				t.Fatalf("ascii=%t rollout lines = %q, want marker %q", ascii, lines, st.stateMarker(kind))
			}
		}
	}
}

func TestIncompleteConsumerWarningHasOneWarningMarker(t *testing.T) {
	st := testStyles(true)
	index := k8s.NewRefIndex()
	index.AddSourceError("pods")
	flow := newEditFlow(t.Context(), testClient(), editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", []byte("new"), st)
	flow.phase = phaseSaved
	flow.radius = index
	flow.rollout = []rolloutItem{{kind: k8s.KindDeployment, name: "web"}}
	content := flow.dialogContent()
	if len(content.criticalWarnings) != 1 || !strings.HasPrefix(content.criticalWarnings[0], "consumer check incomplete: ") {
		t.Fatalf("critical warnings = %q", content.criticalWarnings)
	}
	if strings.Contains(content.criticalWarnings[0], st.stateMarker(stateLineIncomplete)) {
		t.Fatalf("critical warning contains a second marker: %q", content.criticalWarnings[0])
	}
}

func TestRolloutSkip(t *testing.T) {
	h, _, keyScreen := rolloutOfferHarness(t)
	h.keys("esc")
	if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != keyScreen {
		t.Fatalf("rollout skip left top screen %T, want keyScreen", top)
	}
	if got := patchActionCount(h); got != 0 {
		t.Fatalf("rollout skip issued %d patch actions", got)
	}
}

func TestNoOfferWithoutEnvConsumers(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	index := k8s.NewRefIndex()
	index.AddWorkload(k8s.Workload{Kind: k8s.KindStatefulSet, Name: "database", Namespace: "default", Spec: podSpecWithRef("db-creds", k8s.TagVolume)})
	h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, index: index})
	enterSaving(h)
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, index: index})
	if flow.savedResolution() != savedNothingToRestart || len(flow.rollout) != 0 {
		t.Fatalf("volume-only consumer produced a rollout offer: %#v", flow.rollout)
	}
	h.keys("enter")
}

func TestSavedResolution(t *testing.T) {
	tests := []struct {
		name          string
		pending       bool
		radiusPresent bool
		radiusErr     error
		failedSources []string
		rolloutCount  int
		want          savedResolution
	}{
		{name: "checking", pending: true, want: savedChecking},
		{name: "no index", want: savedUnavailable},
		{name: "collection failed", radiusPresent: true, radiusErr: errors.New("consumer scan failed"), want: savedUnavailable},
		{name: "partial without candidates", radiusPresent: true, failedSources: []string{"pods"}, want: savedUnavailable},
		{name: "partial with candidates", radiusPresent: true, failedSources: []string{"pods"}, rolloutCount: 1, want: savedIncompleteRestartOffer},
		{name: "complete without candidates", radiusPresent: true, want: savedNothingToRestart},
		{name: "complete with candidates", radiusPresent: true, rolloutCount: 1, want: savedRestartOffer},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flow := &editFlow{radiusErr: test.radiusErr, rollout: make([]rolloutItem, test.rolloutCount)}
			flow.radiusLoader.pending = test.pending
			if test.radiusPresent {
				flow.radius = k8s.NewRefIndex()
				for _, source := range test.failedSources {
					flow.radius.AddSourceError(source)
				}
			}

			if got := flow.savedResolution(); got != test.want {
				t.Fatalf("saved resolution = %d, want %d", got, test.want)
			}
		})
	}
}

func TestPartialConsumerIndexOffersKnownRestartCandidates(t *testing.T) {
	t.Run("uppercase R restarts known candidate", func(t *testing.T) {
		t.Setenv("NO_COLOR", "1")
		h, flow, keyScreen := incompleteRolloutOfferHarness(t)
		h.send(tea.WindowSizeMsg{Width: 60, Height: 15})
		if got := flow.savedResolution(); got != savedIncompleteRestartOffer {
			t.Fatalf("saved resolution = %d, want %d", got, savedIncompleteRestartOffer)
		}
		if len(flow.rollout) != 1 || flow.rollout[0].kind != k8s.KindDeployment || flow.rollout[0].name != "web" {
			t.Fatalf("rollout candidates = %#v, want Deployment/web", flow.rollout)
		}
		view := h.view()
		for _, want := range []string{"Saved: restart affected workloads?", "consumer check incomplete: pods not listable", "> [x] Deployment/web", "restart Secret default/db-creds", "context test-ctx", "server https://test.example"} {
			if !strings.Contains(view, want) {
				t.Fatalf("incomplete restart offer lost %q:\n%s", want, view)
			}
		}
		if strings.Contains(view, "[incomplete] consumer check") || strings.Contains(view, "! ! consumer check") {
			t.Fatalf("incomplete restart offer double-marked its warning:\n%s", view)
		}
		if strings.Contains(view, "restart check unavailable") {
			t.Fatalf("incomplete restart offer rendered as unavailable:\n%s", view)
		}
		if lines := strings.Count(view, "\n") + 1; lines != 15 {
			t.Fatalf("incomplete restart offer height = %d, want 15:\n%s", lines, view)
		}
		assertRenderedLinesFitWidth(t, view, 60)
		notice := plainOutcomeNotice(keyScreen.outcome)
		if !strings.Contains(notice, "consumer check incomplete") || strings.Contains(notice, incompleteOfferSecretValue) {
			t.Fatalf("incomplete save notice = %q", notice)
		}

		h.keys("R")
		passCommitGate(h)
		if flow.phase != phaseRollingOut || patchActionCount(h) != 1 {
			t.Fatalf("restart commit = phase %d patches %d", flow.phase, patchActionCount(h))
		}
	})

	t.Run("esc skips restart", func(t *testing.T) {
		h, flow, keyScreen := incompleteRolloutOfferHarness(t)
		h.keys("esc")
		if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != keyScreen {
			t.Fatalf("restart skip left top screen %T, want keyScreen", top)
		}
		if flow.phase != phaseSaved || patchActionCount(h) != 0 {
			t.Fatalf("restart skip = phase %d patches %d", flow.phase, patchActionCount(h))
		}
	})
}

func TestPostSaveResolutionByConsumers(t *testing.T) {
	tests := []struct {
		name           string
		addConsumers   func(*k8s.RefIndex, string, string)
		wantCandidates int
	}{
		{name: "completed zero"},
		{
			name: "ordinary volume only",
			addConsumers: func(index *k8s.RefIndex, resourceKind, resourceName string) {
				index.AddWorkload(k8s.Workload{Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Spec: podSpecWithResourceRef(resourceKind, resourceName, k8s.TagVolume, false)})
			},
		},
		{
			name: "non-restartable only",
			addConsumers: func(index *k8s.RefIndex, resourceKind, resourceName string) {
				index.AddWorkload(k8s.Workload{Kind: k8s.KindJob, Name: "migrate", Namespace: "default", Spec: podSpecWithResourceRef(resourceKind, resourceName, k8s.TagEnv, false)})
				index.AddWorkload(k8s.Workload{Kind: k8s.KindCronJob, Name: "backup", Namespace: "default", Spec: podSpecWithResourceRef(resourceKind, resourceName, k8s.TagEnvFrom, false)})
				index.AddPod(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "debug", Namespace: "default"}, Spec: podSpecWithResourceRef(resourceKind, resourceName, k8s.TagEnv, false)})
				if resourceKind == k8s.KindSecret {
					index.AddServiceAccount(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "builder"}, Secrets: []corev1.ObjectReference{{Name: resourceName}}})
				}
			},
		},
		{
			name: "one restartable",
			addConsumers: func(index *k8s.RefIndex, resourceKind, resourceName string) {
				index.AddWorkload(k8s.Workload{Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Spec: podSpecWithResourceRef(resourceKind, resourceName, k8s.TagEnv, false)})
			},
			wantCandidates: 1,
		},
		{
			name: "many restartable",
			addConsumers: func(index *k8s.RefIndex, resourceKind, resourceName string) {
				index.AddWorkload(k8s.Workload{Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Spec: podSpecWithResourceRef(resourceKind, resourceName, k8s.TagEnv, false)})
				index.AddWorkload(k8s.Workload{Kind: k8s.KindStatefulSet, Name: "database", Namespace: "default", Spec: podSpecWithResourceRef(resourceKind, resourceName, k8s.TagEnvFrom, false)})
				index.AddWorkload(k8s.Workload{Kind: k8s.KindDaemonSet, Name: "agent", Namespace: "default", Spec: podSpecWithResourceRef(resourceKind, resourceName, k8s.TagProjected, true)})
			},
			wantCandidates: 3,
		},
	}
	for _, resourceKind := range []string{k8s.KindSecret, k8s.KindConfigMap} {
		for _, test := range tests {
			t.Run(resourceKind+"/"+test.name, func(t *testing.T) {
				h, flow := proposedPostSaveFlowHarness(t, resourceKind)
				index := k8s.NewRefIndex()
				if test.addConsumers != nil {
					test.addConsumers(index, flow.res.Kind(), flow.res.Name())
				}
				enterSaving(h)
				h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
				h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, index: index})

				top := h.m.(app).stack[len(h.m.(app).stack)-1]
				if test.wantCandidates == 0 {
					if top != flow || flow.savedResolution() != savedNothingToRestart || len(flow.rollout) != 0 {
						t.Fatalf("known-empty result = top %T resolution %d rollout %#v", top, flow.savedResolution(), flow.rollout)
					}
					h.keys("enter")
					return
				}
				if top != flow || flow.phase != phaseSaved || len(flow.rollout) != test.wantCandidates {
					t.Fatalf("restartable result = top %T phase %d candidates %#v", top, flow.phase, flow.rollout)
				}
				view := ansi.Strip(flow.View())
				for _, want := range []string{"Saved: restart", resourceKind + " default/" + flow.res.Name()} {
					if !strings.Contains(view, want) {
						t.Fatalf("saved restart offer lost %q:\n%s", want, view)
					}
				}
			})
		}
	}
}

func TestPostSaveConsumerCheckReplacesPreEditorSnapshot(t *testing.T) {
	deployment := func() *appsv1.Deployment {
		return &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
			Spec: appsv1.DeploymentSpec{
				Template: corev1.PodTemplateSpec{Spec: podSpecWithRef("db-creds", k8s.TagEnv)},
			},
		}
	}
	for _, test := range []struct {
		name            string
		preEditorIndex  func() *k8s.RefIndex
		seedConsumer    bool
		mutateConsumers func(*testing.T, *fake.Clientset)
		wantResolution  savedResolution
		wantCandidate   string
	}{
		{
			name:           "consumer appears while editor is open",
			preEditorIndex: k8s.NewRefIndex,
			mutateConsumers: func(t *testing.T, clientset *fake.Clientset) {
				t.Helper()
				if _, err := clientset.AppsV1().Deployments("default").Create(t.Context(), deployment(), metav1.CreateOptions{}); err != nil {
					t.Fatalf("create post-editor consumer: %v", err)
				}
			},
			wantResolution: savedRestartOffer,
			wantCandidate:  "web",
		},
		{
			name:         "consumer disappears while editor is open",
			seedConsumer: true,
			preEditorIndex: func() *k8s.RefIndex {
				index := k8s.NewRefIndex()
				index.AddWorkload(k8s.Workload{Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Spec: podSpecWithRef("db-creds", k8s.TagEnv)})
				return index
			},
			mutateConsumers: func(t *testing.T, clientset *fake.Clientset) {
				t.Helper()
				if err := clientset.AppsV1().Deployments("default").Delete(t.Context(), "web", metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
					t.Fatalf("delete pre-editor consumer: %v", err)
				}
			},
			wantResolution: savedNothingToRestart,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resource := editSecret("10", []byte("old"))
			h := keyHarness(t, resource)
			flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, "DB_PASSWORD", []byte("new"), h.m.(app).styles)
			h.send(pushScreenMsg{s: flow})
			preEditorIndex := test.preEditorIndex()
			h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, index: preEditorIndex})
			clientset := flow.client.Clientset.(*fake.Clientset)
			if test.seedConsumer {
				if _, err := clientset.AppsV1().Deployments("default").Create(t.Context(), deployment(), metav1.CreateOptions{}); err != nil {
					t.Fatalf("create pre-editor consumer: %v", err)
				}
			}
			test.mutateConsumers(t, clientset)
			enterSaving(h)
			preSaveReqID := flow.radiusLoader.reqID
			_, cmd := flow.Update(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
			if flow.radiusLoader.reqID == preSaveReqID {
				t.Fatal("save reused the pre-editor consumer request")
			}

			saveCommandMsg := cmd()
			batch, ok := saveCommandMsg.(tea.BatchMsg)
			if !ok {
				t.Fatalf("save command = %T, want tea.BatchMsg", saveCommandMsg)
			}
			var consumerResult blastRadiusMsg
			for _, command := range batch {
				for _, commandMsg := range testCommandMessages(command) {
					switch msg := commandMsg.(type) {
					case editSavedMsg:
						h.send(msg)
					case blastRadiusMsg:
						consumerResult = msg
					}
				}
			}
			if consumerResult.reqID != flow.radiusLoader.reqID || consumerResult.index == nil || consumerResult.err != nil {
				t.Fatalf("post-save consumer result = %#v", consumerResult)
			}
			h.send(consumerResult)

			if got := flow.savedResolution(); got != test.wantResolution {
				t.Fatalf("saved resolution = %d, want %d", got, test.wantResolution)
			}
			if test.wantCandidate == "" {
				if len(flow.rollout) != 0 {
					t.Fatalf("disappeared consumer remained a restart candidate: %#v", flow.rollout)
				}
				return
			}
			if len(flow.rollout) != 1 || flow.rollout[0].name != test.wantCandidate {
				t.Fatalf("post-editor consumer candidates = %#v, want %s", flow.rollout, test.wantCandidate)
			}
		})
	}
}

func TestReadOnlySaveLeavesImmediately(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	flow.env.readOnly = true
	enterSaving(h)
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top == flow {
		t.Fatal("read-only save kept the post-save flow open")
	}
	if flow.radiusLoader.pending {
		t.Fatal("read-only save left the consumer check running after dismissal")
	}
}

func TestSaveWaitsForPendingBlastRadius(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	enterSaving(h)
	preSaveReqID := flow.radiusLoader.reqID
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	radiusReqID := flow.radiusLoader.reqID
	if radiusReqID == preSaveReqID {
		t.Fatal("save reused the pre-editor consumer request")
	}

	if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != flow {
		t.Fatalf("pending consumer check left top screen %T, want edit flow", top)
	}
	if flow.phase != phaseSaved || !flow.radiusLoader.pending {
		t.Fatalf("saved state = phase %d pending %t, want phaseSaved with live check", flow.phase, flow.radiusLoader.pending)
	}
	if view := ansi.Strip(flow.View()); !strings.Contains(view, "Saved: checking for workloads to restart") || strings.Contains(view, "nothing to restart") {
		t.Fatalf("pending saved view used the wrong resolution:\n%s", view)
	}

	h.keys("enter")
	if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != flow {
		t.Fatalf("enter dismissed a still-running check to %T", top)
	}

	staleIndex := k8s.NewRefIndex()
	staleIndex.AddWorkload(k8s.Workload{Kind: k8s.KindDeployment, Name: "stale", Namespace: "default", Spec: podSpecWithRef("db-creds", k8s.TagEnv)})
	h.send(blastRadiusMsg{reqID: preSaveReqID, index: staleIndex})
	if !flow.radiusLoader.pending || len(flow.rollout) != 0 {
		t.Fatalf("stale pre-editor result changed post-save state: pending %t rollout %#v", flow.radiusLoader.pending, flow.rollout)
	}

	index := k8s.NewRefIndex()
	index.AddWorkload(k8s.Workload{Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Spec: podSpecWithRef("db-creds", k8s.TagEnv)})
	h.send(blastRadiusMsg{reqID: radiusReqID, index: index})
	if flow.phase != phaseSaved || len(flow.rollout) != 1 || !strings.Contains(ansi.Strip(flow.View()), "Saved: restart affected workloads?") {
		t.Fatalf("resolved saved state = phase %d rollout %#v view:\n%s", flow.phase, flow.rollout, ansi.Strip(flow.View()))
	}

	h.send(blastRadiusMsg{reqID: radiusReqID, index: k8s.NewRefIndex()})
	if len(flow.rollout) != 1 || flow.rollout[0].name != "web" {
		t.Fatalf("stale blast-radius result changed rollout candidates: %#v", flow.rollout)
	}

	h.keys("esc")
	value, err := flow.res.Get(flow.key)
	if err != nil || string(value) != "new" {
		t.Fatalf("saved value after skip = %q, err = %v", value, err)
	}
}

func TestPendingSaveResolvesToNothingInPlace(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	enterSaving(h)
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	radiusReqID := flow.radiusLoader.reqID
	h.send(blastRadiusMsg{reqID: radiusReqID, index: k8s.NewRefIndex()})

	if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != flow || flow.phase != phaseSaved {
		t.Fatalf("completed-empty result left top %T in phase %d", top, flow.phase)
	}
	view := ansi.Strip(flow.View())
	if !strings.Contains(view, "Saved: nothing to restart") || strings.Contains(view, "unavailable") {
		t.Fatalf("completed-empty saved view used the wrong resolution:\n%s", view)
	}

	h.keys("enter")
	if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top == flow {
		t.Fatal("enter did not complete the saved flow")
	}
	value, err := flow.res.Get(flow.key)
	if err != nil || string(value) != "new" {
		t.Fatalf("saved value after completion = %q, err = %v", value, err)
	}
}

func TestSavedConsumerCheckNonSuccessStates(t *testing.T) {
	degraded := k8s.NewRefIndex()
	degraded.AddSourceError("pods")
	tests := []struct {
		name    string
		result  blastRadiusMsg
		want    string
		pending bool
	}{
		{name: "still running", want: "Saved: checking for workloads to restart", pending: true},
		{name: "cancelled", result: blastRadiusMsg{err: context.Canceled}, want: "Saved: restart check unavailable"},
		{name: "failed", result: blastRadiusMsg{err: errors.New("consumer scan failed")}, want: "Saved: restart check unavailable"},
		{name: "degraded", result: blastRadiusMsg{index: degraded}, want: "Saved: restart check unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
			enterSaving(h)
			h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
			radiusReqID := flow.radiusLoader.reqID
			if !test.pending {
				result := test.result
				result.reqID = radiusReqID
				h.send(result)
			}

			if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != flow || flow.phase != phaseSaved {
				t.Fatalf("consumer-check result left top %T in phase %d", top, flow.phase)
			}
			view := ansi.Strip(flow.View())
			if !strings.Contains(view, test.want) || strings.Contains(view, "nothing to restart") {
				t.Fatalf("%s result used completed-empty wording:\n%s", test.name, view)
			}
			if test.pending {
				h.keys("enter")
				if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top != flow {
					t.Fatalf("enter dismissed running check to %T", top)
				}
				h.keys("esc")
				if flow.radiusLoader.pending {
					t.Fatal("esc left the consumer check running")
				}
				return
			}
			h.keys("enter")
			if top := h.m.(app).stack[len(h.m.(app).stack)-1]; top == flow {
				t.Fatal("enter did not dismiss resolved unavailable state")
			}
		})
	}
}

func TestResourceEditSave(t *testing.T) {
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{
		"binary": {0, 1, 2},
		"delete": []byte("old"),
		"keep":   []byte("before"),
	})
	h := keyHarness(t, resource)
	keyScreen := topKeyScreen(t, h)
	h.keys("e")
	flow := topEditFlow(t, h)
	resolveNoConsumers(h, flow)
	tempPath := flow.dir.Path
	writeFlowFile(t, flow, "added: new\nkeep: after\n")
	h.send(editorFinishedMsg{})
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
	passCommitGate(h)
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	resolveNoConsumers(h, flow)
	h.keys("enter")

	assertResourceValues(t, resource, map[string][]byte{
		"added":  []byte("new"),
		"binary": {0, 1, 2},
		"keep":   []byte("after"),
	})
	entry, ok := keyScreen.env.ring.LatestFor("test-ctx", k8s.KindSecret, "default", "edit-target")
	if !ok || string(entry.Previous["delete"]) != "old" || string(entry.Previous["keep"]) != "before" || !slices.Equal(entry.Added, []string{"added"}) {
		t.Fatalf("undo entry = %+v, found = %t", entry, ok)
	}
	if len(h.m.(app).stack) != 3 {
		t.Fatalf("stack depth = %d, want 3", len(h.m.(app).stack))
	}
	if flow.dir != nil {
		t.Fatal("flow temp directory was not cleared")
	}
	if _, err := os.Stat(tempPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temp directory still exists: %v", err)
	}
	if !keyScreen.pending {
		t.Fatal("editSavedMsg did not refresh the key screen")
	}
}

func TestResourceEditRejectsBinaryKeyCollision(t *testing.T) {
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{
		"binary": {0, 1, 2},
		"text":   []byte("before"),
	})
	h := keyHarness(t, resource)
	h.keys("e")
	flow := topEditFlow(t, h)
	editedDocument := "binary: replacement\ntext: after\n"
	writeFlowFile(t, flow, editedDocument)
	h.send(editorFinishedMsg{})

	if flow.phase != phaseBinaryCollision || !strings.Contains(flow.message, `key "binary" is binary`) {
		t.Fatalf("collision state = phase %d message %q", flow.phase, flow.message)
	}
	view := ansi.Strip(flow.View())
	for _, want := range []string{
		"Binary key cannot be edited as YAML",
		"This document names a binary key that YAML editing omits.",
		"e reopens the document with your edits kept; esc aborts this",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("collision dialog missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "YAML parse failed") {
		t.Fatalf("collision dialog still reports a parse failure:\n%s", view)
	}
	if hints := plainFooter(t, flow, 1); hints != "e re-edit  esc abort" {
		t.Fatalf("collision hints = %q", hints)
	}
	if flow.editedMap != nil || flow.applied {
		t.Fatalf("collision left edited map %#v or applied state %t", flow.editedMap, flow.applied)
	}
	depth := len(h.m.(app).stack)
	h.keys("e")
	reopened, err := os.ReadFile(flow.filePath)
	if err != nil || string(reopened) != editedDocument {
		t.Fatalf("reopened document = %q, err = %v, want %q", reopened, err, editedDocument)
	}
	h.send(editorFinishedMsg{})
	if flow.phase != phaseBinaryCollision {
		t.Fatalf("unchanged re-edit phase = %d, want %d", flow.phase, phaseBinaryCollision)
	}
	h.keys("esc")
	if len(h.m.(app).stack) != depth-1 {
		t.Fatalf("stack depth after abort = %d, want %d", len(h.m.(app).stack), depth-1)
	}
	assertResourceValues(t, resource, map[string][]byte{
		"binary": {0, 1, 2},
		"text":   []byte("before"),
	})
}

func TestResourceEditConflictRejectsFreshBinaryCollision(t *testing.T) {
	resource := testSecretWithVersion("10", corev1.SecretTypeOpaque, map[string][]byte{"keep": []byte("before")})
	h := keyHarness(t, resource)
	keyScreen := topKeyScreen(t, h)
	h.keys("e")
	flow := topEditFlow(t, h)
	writeFlowFile(t, flow, "added: new\nkeep: mine\n")
	h.send(editorFinishedMsg{})
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
	passCommitGate(h)
	fresh := testSecretWithVersion("11", corev1.SecretTypeOpaque, map[string][]byte{"added": {0, 1, 2}, "keep": []byte("theirs")})
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveConflict, Cluster: fresh}})

	if flow.phase != phaseBinaryCollision || !strings.Contains(flow.message, `key "added" became binary`) {
		t.Fatalf("conflict collision state = phase %d message %q", flow.phase, flow.message)
	}
	if flow.applied {
		t.Fatal("conflict collision left the applied flag set")
	}
	assertResourceValues(t, fresh, map[string][]byte{"added": {0, 1, 2}, "keep": []byte("theirs")})
	assertResourceValues(t, resource, map[string][]byte{"keep": []byte("before")})
	if _, ok := keyScreen.env.ring.LatestFor("test-ctx", k8s.KindSecret, "default", "edit-target"); ok {
		t.Fatal("rejected conflict recorded an undo entry")
	}
	h.keys("e")
	document, err := os.ReadFile(flow.filePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# resourceVersion: 11", "added: new", "keep: mine", "# omitted (binary): added [3 B]"} {
		if !strings.Contains(string(document), want) {
			t.Fatalf("conflict collision re-edit document missing %q:\n%s", want, document)
		}
	}
}

func TestResourceRevertRejectsBinaryCollision(t *testing.T) {
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"cred": {0, 1, 2}})
	flow := newResourceRevertFlow(t.Context(), testClient(), editEnv{}, resource, map[string]string{"cred": "plain"}, nil, testStyles(true))
	if flow.phase != phaseDiff {
		t.Fatalf("revert flow phase = %d, want %d", flow.phase, phaseDiff)
	}

	_, _ = flow.Update(key("Y"))

	if flow.phase != phaseBinaryCollision || !strings.Contains(flow.message, `key "cred" is binary`) {
		t.Fatalf("revert collision state = phase %d message %q", flow.phase, flow.message)
	}
	view := ansi.Strip(flow.View())
	for _, want := range []string{
		"Binary key cannot be edited as YAML",
		"This change cannot be applied from this flow.",
		"e returns to the diff; esc closes it.",
		"Use import (i) from the key screen to change a binary key.",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("revert collision dialog missing %q:\n%s", want, view)
		}
	}
	if hints := plainFooter(t, flow, 1); hints != "e back to diff  esc abort" {
		t.Fatalf("revert collision hints = %q", hints)
	}
	if strings.Contains(view, "reopens") {
		t.Fatalf("revert collision promises an editor: view %q hints %q", view, plainFooter(t, flow, 1))
	}
	if flow.applied {
		t.Fatal("revert collision left the applied flag set")
	}
	assertResourceValues(t, resource, map[string][]byte{"cred": {0, 1, 2}})

	_, _ = flow.Update(key("e"))
	if flow.phase != phaseDiff || flow.message != "" {
		t.Fatalf("revert collision re-edit state = phase %d message %q", flow.phase, flow.message)
	}
}

func TestResourceEditNoOp(t *testing.T) {
	t.Run("unchanged file", func(t *testing.T) {
		h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"keep": []byte("before")}))
		h.keys("e")
		flow := topEditFlow(t, h)
		tempPath := flow.dir.Path
		h.send(editorFinishedMsg{})
		if len(h.m.(app).stack) != 3 {
			t.Fatalf("stack depth = %d, want 3", len(h.m.(app).stack))
		}
		if _, err := os.Stat(tempPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("temp directory still exists: %v", err)
		}
	})

	t.Run("different document with equal values", func(t *testing.T) {
		h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"keep": []byte("before")}))
		h.keys("e")
		flow := topEditFlow(t, h)
		tempPath := flow.dir.Path
		writeFlowFile(t, flow, "# reformatted\nkeep: before\n")
		h.send(editorFinishedMsg{})
		if len(h.m.(app).stack) != 3 {
			t.Fatalf("stack depth = %d, want 3", len(h.m.(app).stack))
		}
		if _, err := os.Stat(tempPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("temp directory still exists: %v", err)
		}
	})
}

func TestResourceEditAbortRestoresAllKeys(t *testing.T) {
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{
		"delete": []byte("old"),
		"keep":   []byte("before"),
	})
	h := keyHarness(t, resource)
	h.keys("e")
	flow := topEditFlow(t, h)
	writeFlowFile(t, flow, "added: new\nkeep: after\n")
	h.send(editorFinishedMsg{})
	h.keys("Y")
	assertResourceValues(t, resource, map[string][]byte{"added": []byte("new"), "keep": []byte("after")})
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunRejected, Message: "rejected"}})
	h.keys("esc")
	assertResourceValues(t, resource, map[string][]byte{"delete": []byte("old"), "keep": []byte("before")})
	if len(h.m.(app).stack) != 3 {
		t.Fatalf("stack depth = %d, want 3", len(h.m.(app).stack))
	}
}

func TestDeleteKeyAbortRestoresOriginalValue(t *testing.T) {
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"obsolete": []byte("old")})
	h := keyHarness(t, resource)
	h.keys("D")
	flow := topEditFlow(t, h)
	h.keys("Y")
	if _, err := resource.Get("obsolete"); err == nil {
		t.Fatal("key still exists after applying the proposed deletion")
	}
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunRejected, Message: "rejected"}})
	h.keys("esc")

	value, err := resource.Get("obsolete")
	if err != nil || string(value) != "old" {
		t.Fatalf("restored key = %q, %v; want old", value, err)
	}
}

func TestResourceEditConflict(t *testing.T) {
	resource := testSecretWithVersion("10", corev1.SecretTypeOpaque, map[string][]byte{"keep": []byte("before")})
	h := keyHarness(t, resource)
	h.keys("e")
	flow := topEditFlow(t, h)
	writeFlowFile(t, flow, "added: new\nkeep: mine\n")
	h.send(editorFinishedMsg{})
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
	passCommitGate(h)
	fresh := testSecretWithVersion("11", corev1.SecretTypeOpaque, map[string][]byte{"fresh": []byte("cluster"), "keep": []byte("theirs")})
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveConflict, Cluster: fresh}})

	if flow.phase != phaseConflict || flow.rawDoc != nil || !maps.Equal(flow.originalMap, map[string]string{"fresh": "cluster", "keep": "theirs"}) {
		t.Fatalf("conflict state = phase %d raw %q original %#v", flow.phase, flow.rawDoc, flow.originalMap)
	}
	assertResourceValues(t, resource, map[string][]byte{"keep": []byte("before")})
	h.keys("e")
	document, err := os.ReadFile(flow.filePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# resourceVersion: 11", "added: new", "keep: mine"} {
		if !strings.Contains(string(document), want) {
			t.Fatalf("conflict re-edit document missing %q:\n%s", want, document)
		}
	}
}

func TestResourceEditParseFailedReEdit(t *testing.T) {
	h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"port": []byte("text")}))
	h.keys("e")
	flow := topEditFlow(t, h)
	broken := []byte("port: 8080\n# keep this comment\n")
	writeFlowFile(t, flow, string(broken))
	h.send(editorFinishedMsg{})
	if flow.phase != phaseParseFailed {
		t.Fatalf("phase = %d, want parse failed", flow.phase)
	}
	h.keys("ctrl+c")
	if h.sawQuit || !h.m.(app).quitArm.armed {
		t.Fatalf("first ctrl+c quit = %t, armed = %t", h.sawQuit, h.m.(app).quitArm.armed)
	}
	h.keys("e")
	if h.m.(app).quitArm.armed {
		t.Fatal("re-edit key did not disarm quit")
	}
	reseeded, err := os.ReadFile(flow.filePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reseeded, broken) {
		t.Fatalf("reseeded document = %q, want %q", reseeded, broken)
	}
	message := flow.editorReturnMessage
	depth := len(h.m.(app).stack)
	h.send(editorFinishedMsg{})
	if len(h.m.(app).stack) != depth || flow.phase != phaseParseFailed || flow.message != message || flow.editedMap != nil {
		t.Fatalf("unchanged re-edit = depth %d phase %d message %q edited %#v", len(h.m.(app).stack), flow.phase, flow.message, flow.editedMap)
	}
}

func TestResourceEditFailedReparsePreservesEditedMap(t *testing.T) {
	h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"port": []byte("text")}))
	h.keys("e")
	flow := topEditFlow(t, h)
	writeFlowFile(t, flow, "port: valid\n")
	h.send(editorFinishedMsg{})
	h.keys("e")
	writeFlowFile(t, flow, "port: 8080\n")
	h.send(editorFinishedMsg{})

	if flow.phase != phaseParseFailed || !maps.Equal(flow.editedMap, map[string]string{"port": "valid"}) || !flow.capturesQuit() {
		t.Fatalf("failed reparse state = phase %d edited %#v captures quit %t", flow.phase, flow.editedMap, flow.capturesQuit())
	}
}

func TestUnchangedReEditReturnsToFailure(t *testing.T) {
	tests := []struct {
		name    string
		phase   editPhase
		message string
	}{
		{name: "dry-run rejected", phase: phaseDryRunRejected, message: "denied"},
		{name: "save failed", phase: phaseDiff, message: "save failed"},
		{name: "conflict", phase: phaseConflict, message: "resource changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, flow := proposedFlowHarness(t, []byte("old"), []byte("mine"))
			flow.proposedMode = false
			flow.phase = test.phase
			flow.message = test.message
			flow.refreshContent()
			depth := len(h.m.(app).stack)

			h.keys("e")
			if flow.phase != phaseEditing {
				t.Fatalf("re-edit phase = %d, want editing", flow.phase)
			}
			h.send(editorFinishedMsg{})

			if len(h.m.(app).stack) != depth || flow.phase != test.phase || flow.message != test.message {
				t.Fatalf("unchanged re-edit = depth %d phase %d message %q", len(h.m.(app).stack), flow.phase, flow.message)
			}
		})
	}
}

func TestResourceConflictReadFailureKeepsOriginalSnapshot(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	original := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"key": []byte("original")})
	flow := newResourceEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, original, h.m.(app).styles)
	cluster := failingGetResource{Resource: testSecretWithVersion("11", corev1.SecretTypeOpaque, map[string][]byte{"key": []byte("cluster")})}

	flow.enterConflict(cluster)

	if flow.phase != phaseDiff || flow.res != original || !maps.Equal(flow.originalMap, map[string]string{"key": "original"}) {
		t.Fatalf("read failure state = phase %d resource %T original %#v", flow.phase, flow.res, flow.originalMap)
	}
}

func TestKeyAdd(t *testing.T) {
	t.Run("save", func(t *testing.T) {
		resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"existing": []byte("old")})
		h := keyHarness(t, resource)
		h.keys("N")
		typeText(h, "added")
		h.keys("enter")
		flow := topEditFlow(t, h)
		if flow.target != targetNewKey || flow.key != "added" {
			t.Fatalf("add flow target/key = %d/%q", flow.target, flow.key)
		}
		writeFlowFile(t, flow, "v\n")
		h.send(editorFinishedMsg{})
		h.keys("Y")
		h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
		passCommitGate(h)
		h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
		assertResourceValues(t, resource, map[string][]byte{"added": []byte("v"), "existing": []byte("old")})
		entry, ok := h.m.(app).editEnv.ring.LatestFor("test-ctx", k8s.KindSecret, "default", "edit-target")
		if !ok || !slices.Equal(entry.Added, []string{"added"}) || entry.Previous != nil {
			t.Fatalf("undo entry = %+v, found = %t", entry, ok)
		}
	})

	t.Run("empty value cancels", func(t *testing.T) {
		resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"existing": []byte("old")})
		h := keyHarness(t, resource)
		h.keys("N")
		typeText(h, "added")
		h.keys("enter")
		flow := topEditFlow(t, h)
		writeFlowFile(t, flow, "")
		h.send(editorFinishedMsg{})
		assertResourceValues(t, resource, map[string][]byte{"existing": []byte("old")})
		if len(h.m.(app).stack) != 3 {
			t.Fatalf("stack depth = %d, want 3", len(h.m.(app).stack))
		}
	})
}

func TestKeyAddPromptValidation(t *testing.T) {
	h := keyHarness(t, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"existing": []byte("old")}))
	h.keys("N")
	depth := len(h.m.(app).stack)
	assertPromptRejected := func(want string) {
		t.Helper()
		prompt, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*keyNamePrompt)
		if !ok || prompt.message == "" || !strings.Contains(prompt.message, want) || len(h.m.(app).stack) != depth {
			t.Fatalf("prompt rejection = top %T message %q depth %d", h.m.(app).stack[len(h.m.(app).stack)-1], prompt.message, len(h.m.(app).stack))
		}
	}
	h.keys("enter")
	assertPromptRejected("required")
	h.keys("ctrl+u")
	typeText(h, "bad/name")
	h.keys("enter")
	assertPromptRejected("consist of")
	h.keys("ctrl+u")
	typeText(h, "existing")
	h.keys("enter")
	assertPromptRejected("already exists")
	h.keys("ctrl+u")
	typeText(h, "new")
	h.keys("enter")
	flow := topEditFlow(t, h)
	if flow.target != targetNewKey || flow.key != "new" || len(h.m.(app).stack) != depth {
		t.Fatalf("valid prompt result = target %d key %q depth %d", flow.target, flow.key, len(h.m.(app).stack))
	}
}

func TestKeyDelete(t *testing.T) {
	t.Run("opaque", func(t *testing.T) {
		resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"obsolete": []byte("old")})
		h := keyHarness(t, resource)
		h.keys("D")
		flow := topEditFlow(t, h)
		h.keys("Y")
		h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
		passCommitGate(h)
		h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
		assertResourceValues(t, resource, map[string][]byte{})
		entry, ok := h.m.(app).editEnv.ring.LatestFor("test-ctx", k8s.KindSecret, "default", "edit-target")
		if !ok || string(entry.Previous["obsolete"]) != "old" {
			t.Fatalf("undo entry = %+v, found = %t", entry, ok)
		}
	})

	t.Run("required TLS key", func(t *testing.T) {
		resource := testSecret(corev1.SecretTypeTLS, map[string][]byte{
			corev1.TLSCertKey:       []byte("certificate"),
			corev1.TLSPrivateKeyKey: []byte("private key"),
		})
		h := keyHarness(t, resource)
		h.keys("down", "D")
		flow := topEditFlow(t, h)
		if flow.key != corev1.TLSPrivateKeyKey {
			t.Fatalf("delete key = %q, want %q", flow.key, corev1.TLSPrivateKeyKey)
		}
		h.keys("Y")
		if flow.phase != phaseValidateWarn || len(flow.warnings) != 1 || !strings.Contains(string(flow.warnings[0]), corev1.TLSPrivateKeyKey) {
			t.Fatalf("validation state = phase %d warnings %v", flow.phase, flow.warnings)
		}
		h.keys("Y")
		if flow.phase != phaseDryRun || !flow.pending {
			t.Fatalf("save-anyway state = phase %d pending %t", flow.phase, flow.pending)
		}
	})
}

func TestValidateWarnPaths(t *testing.T) {
	newWarningFlow := func(t *testing.T) (*harness, *editFlow, k8s.Resource) {
		t.Helper()
		resource := testSecret(corev1.SecretTypeTLS, map[string][]byte{corev1.TLSCertKey: []byte("old")})
		h := keyHarness(t, resource)
		flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, corev1.TLSCertKey, []byte("new"), h.m.(app).styles)
		h.send(pushScreenMsg{s: flow})
		h.keys("Y")
		return h, flow, resource
	}

	t.Run("abort restores", func(t *testing.T) {
		h, flow, resource := newWarningFlow(t)
		if flow.phase != phaseValidateWarn {
			t.Fatalf("phase = %d, want validation warning", flow.phase)
		}
		h.keys("esc")
		assertResourceValues(t, resource, map[string][]byte{corev1.TLSCertKey: []byte("old")})
		if len(h.m.(app).stack) != 3 {
			t.Fatalf("stack depth = %d, want 3", len(h.m.(app).stack))
		}
	})

	t.Run("save anyway", func(t *testing.T) {
		h, flow, _ := newWarningFlow(t)
		h.keys("Y")
		if flow.phase != phaseDryRun || !flow.pending {
			t.Fatalf("save-anyway state = phase %d pending %t", flow.phase, flow.pending)
		}
	})
}

func TestUndoWholeResource(t *testing.T) {
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{
		"added":   []byte("remove me"),
		"changed": []byte("current"),
		"stable":  []byte("keep"),
	})
	h := keyHarness(t, resource)
	h.m.(app).editEnv.ring.Push(undo.Entry{
		Context: "test-ctx", Kind: k8s.KindSecret, Namespace: "default", Name: "edit-target",
		Previous: map[string][]byte{"changed": []byte("previous"), "deleted": []byte("restore")},
		Added:    []string{"added"},
	})
	h.keys("ctrl+z")
	flow := topEditFlow(t, h)
	if flow.target != targetResource || !flow.proposedMode {
		t.Fatalf("undo flow target/proposed = %d/%t", flow.target, flow.proposedMode)
	}
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
	passCommitGate(h)
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	assertResourceValues(t, resource, map[string][]byte{
		"changed": []byte("previous"),
		"deleted": []byte("restore"),
		"stable":  []byte("keep"),
	})
}

func TestUndoReAddsDeletedKey(t *testing.T) {
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{})
	h := keyHarness(t, resource)
	h.m.(app).editEnv.ring.Push(undo.Entry{
		Context: "test-ctx", Kind: k8s.KindSecret, Namespace: "default", Name: "edit-target",
		Previous: map[string][]byte{"deleted": []byte("restore")},
	})
	h.keys("ctrl+z")
	flow := topEditFlow(t, h)
	if flow.target != targetNewKey || flow.key != "deleted" {
		t.Fatalf("undo flow target/key = %d/%q", flow.target, flow.key)
	}
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
	passCommitGate(h)
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	assertResourceValues(t, resource, map[string][]byte{"deleted": []byte("restore")})
}

func TestUndoOfAddDeletesKey(t *testing.T) {
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"added": []byte("current")})
	h := keyHarness(t, resource)
	h.m.(app).editEnv.ring.Push(undo.Entry{
		Context: "test-ctx", Kind: k8s.KindSecret, Namespace: "default", Name: "edit-target", Added: []string{"added"},
	})
	h.keys("ctrl+z")
	flow := topEditFlow(t, h)
	if flow.target != targetDeleteKey || flow.key != "added" {
		t.Fatalf("undo flow target/key = %d/%q", flow.target, flow.key)
	}
}

func TestReadOnlyBlocksKeyManagement(t *testing.T) {
	t.Run("read only", func(t *testing.T) {
		resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{"key": []byte("value")})
		h := keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: true, ReadOnly: true})
		depth := len(h.m.(app).stack)
		h.keys("e", "N", "D")
		if len(h.m.(app).stack) != depth {
			t.Fatalf("stack depth = %d, want %d", len(h.m.(app).stack), depth)
		}
	})

	t.Run("immutable", func(t *testing.T) {
		immutable := true
		resource := k8s.NewSecret(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "edit-target", Namespace: "default"},
			Immutable:  &immutable,
			Data:       map[string][]byte{"key": []byte("value")},
		})
		h := keyHarness(t, resource)
		depth := len(h.m.(app).stack)
		h.keys("e", "N", "D")
		if len(h.m.(app).stack) != depth {
			t.Fatalf("stack depth = %d, want %d", len(h.m.(app).stack), depth)
		}
	})
}

func TestEditFlowNoOp(t *testing.T) {
	h, flow := editorFlowHarness(t)
	path := flow.dir.Path
	content, err := os.ReadFile(flow.filePath)
	if err != nil || string(content) != "old" {
		t.Fatalf("seeded file = %q, err = %v", content, err)
	}
	h.send(editorFinishedMsg{})
	if len(h.m.(app).stack) != 1 {
		t.Fatalf("stack depth = %d, want 1", len(h.m.(app).stack))
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temp directory still exists: %v", err)
	}
}

func TestEditFlowWaitDelayReadsEditedFile(t *testing.T) {
	h, flow := editorFlowHarness(t)
	depth := len(h.m.(app).stack)

	h.send(editorFinishedMsg{err: exec.ErrWaitDelay})

	if len(h.m.(app).stack) != depth-1 || flow.phase == phaseEditorFailed {
		t.Fatalf("WaitDelay result = stack depth %d phase %d", len(h.m.(app).stack), flow.phase)
	}
}

func TestEditFlowTrailingNewlineNoOp(t *testing.T) {
	h, flow := editorFlowHarness(t)
	path := flow.dir.Path
	if err := os.WriteFile(flow.filePath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.send(editorFinishedMsg{})
	if len(h.m.(app).stack) != 1 {
		t.Fatalf("stack depth = %d, want 1", len(h.m.(app).stack))
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("temp directory still exists: %v", err)
	}
}

func TestEditFlowSaveSuccess(t *testing.T) {
	h := keyHarness(t, navigationSecret())
	keyScreen := h.m.(app).stack[len(h.m.(app).stack)-1].(*keyScreen)
	flow := newEditFlow(t.Context(), h.m.(app).client, keyScreen.env, keyScreen.resource, "config", []byte("new"), h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	enterSaving(h)
	h.send(saveDoneMsg{reqID: h.topReqID(), result: k8s.SaveResult{Outcome: k8s.SaveFailed, Message: "save failed"}})
	h.keys("esc")
	value, err := keyScreen.resource.Get("config")
	if err != nil || string(value) != "first line\nsecond line\nthird line" {
		t.Fatalf("resource after failed save abort = %q, err = %v", value, err)
	}

	flow = newEditFlow(t.Context(), h.m.(app).client, keyScreen.env, keyScreen.resource, "config", []byte("new"), h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	resolveNoConsumers(h, flow)
	enterSaving(h)
	h.send(saveDoneMsg{reqID: h.topReqID(), result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	resolveNoConsumers(h, flow)
	h.keys("enter")
	if len(h.m.(app).stack) != 3 {
		t.Fatalf("stack depth = %d, want 3", len(h.m.(app).stack))
	}
	if !keyScreen.pending {
		t.Fatal("key screen did not refresh")
	}
	entry, ok := keyScreen.env.ring.LatestFor("test-ctx", k8s.KindSecret, "default", "app-credentials")
	if !ok || string(entry.Previous["config"]) != "first line\nsecond line\nthird line" {
		t.Fatalf("undo entry = %+v, %t", entry, ok)
	}
	if flow.dir != nil {
		t.Fatal("flow temp directory was not cleaned")
	}
}

func TestEditFlowConflictReEdit(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("mine"))
	enterSaving(h)
	h.send(saveDoneMsg{reqID: h.topReqID(), result: k8s.SaveResult{Outcome: k8s.SaveConflict, Cluster: editSecret("11", []byte("cluster"))}})
	h.keys("e")
	if flow.phase != phaseDiff || flow.res.ResourceVersion() != "11" || string(flow.original) != "cluster" || string(flow.edited) != "mine" {
		t.Fatalf("flow after conflict re-edit = phase %d rv %s original %q edited %q", flow.phase, flow.res.ResourceVersion(), flow.original, flow.edited)
	}
}

func TestEditFlowConflictReappliesMine(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("mine"))
	enterSaving(h)
	h.send(saveDoneMsg{reqID: h.topReqID(), result: k8s.SaveResult{Outcome: k8s.SaveConflict, Cluster: editSecret("11", []byte("cluster"))}})

	h.keys("Y")

	value, err := flow.res.Get(flow.key)
	if err != nil || string(value) != "mine" || flow.res.ResourceVersion() != "11" || flow.phase != phaseDryRun {
		t.Fatalf("re-apply state = value %q err %v rv %s phase %d", value, err, flow.res.ResourceVersion(), flow.phase)
	}
}

func TestConflictHeaderKeepsOverwriteInstructionAtMinimumWidth(t *testing.T) {
	_, flow := proposedFlowHarness(t, []byte("old"), []byte("mine"))
	flow.phase = phaseConflict
	flow.res = editSecret("1234567890", []byte("cluster"))
	flow.SetSize(60, 13)

	header := strings.Join(flow.diffHeader(), "\n")
	if !strings.Contains(header, "Y overwrites the other writer's change") {
		t.Fatalf("conflict header truncated the overwrite instruction:\n%s", header)
	}
}

func TestEditFlowKeyboardScrollsDiffAndConflict(t *testing.T) {
	for _, test := range []struct {
		name  string
		phase editPhase
	}{
		{name: "diff", phase: phaseDiff},
		{name: "conflict", phase: phaseConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, flow := proposedFlowHarness(t, []byte(strings.Repeat("old\n", 30)), []byte(strings.Repeat("new\n", 30)))
			flow.phase = test.phase
			flow.SetSize(80, 4)
			flow.refreshContent()

			_, _ = flow.Update(key("down"))

			if flow.viewport.YOffset() == 0 {
				t.Fatalf("phase %d down key did not scroll viewport", test.phase)
			}
		})
	}
}

func TestRolloutDoneOrdersRowsAndMarksOverflow(t *testing.T) {
	for _, test := range []struct {
		name              string
		width             int
		height            int
		results           []rolloutResult
		wantTitle         string
		wantScrolledTitle string
		wantSummary       string
		wantOrder         []string
	}{
		{
			name:   "failures before successes keep dispatch order",
			width:  80,
			height: 22,
			results: []rolloutResult{
				{kind: k8s.KindDeployment, name: "worker-01"},
				{kind: k8s.KindStatefulSet, name: "database", err: errors.New("timed out")},
				{kind: k8s.KindDeployment, name: "worker-02"},
				{kind: k8s.KindDeployment, name: "payments", err: errors.New("forbidden")},
			},
			wantTitle:   "Rollout results",
			wantSummary: "2 restarted, 2 failed",
			wantOrder: []string{
				"[error] StatefulSet/database",
				"[error] Deployment/payments",
				"[success] Deployment/worker-01",
				"[success] Deployment/worker-02",
			},
		},
		{
			name:   "all successes fit",
			width:  80,
			height: 22,
			results: []rolloutResult{
				{kind: k8s.KindDeployment, name: "worker-01"},
				{kind: k8s.KindDeployment, name: "worker-02"},
			},
			wantTitle:   "Rollout results",
			wantSummary: "2 restarted, 0 failed",
			wantOrder: []string{
				"[success] Deployment/worker-01",
				"[success] Deployment/worker-02",
			},
		},
		{
			name:   "successes overflow",
			width:  80,
			height: 4,
			results: []rolloutResult{
				{kind: k8s.KindDeployment, name: "worker-01"},
				{kind: k8s.KindDeployment, name: "worker-02"},
				{kind: k8s.KindDeployment, name: "worker-03"},
			},
			wantTitle:         "Rollout results  1-1 of 3 shown",
			wantScrolledTitle: "Rollout results  2-2 of 3 shown",
			wantSummary:       "3 restarted, 0 failed",
			wantOrder: []string{
				"[success] Deployment/worker-01",
				"[success] Deployment/worker-02",
				"[success] Deployment/worker-03",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
			flow.phase = phaseRolloutDone
			flow.rolloutResults = test.results
			flow.SetSize(test.width, test.height)
			flow.refreshContent()

			content := ansi.Strip(flow.viewport.GetContent())
			previous := -1
			for _, want := range test.wantOrder {
				index := strings.Index(content, want)
				if index <= previous {
					t.Fatalf("rollout row %q index = %d after %d:\n%s", want, index, previous, content)
				}
				previous = index
			}
			if got := flow.dialogContent(); got.title != test.wantTitle || got.summary != test.wantSummary {
				t.Fatalf("rollout content title/summary = %q/%q, want %q/%q", got.title, got.summary, test.wantTitle, test.wantSummary)
			}
			if test.wantScrolledTitle != "" {
				_, _ = flow.Update(key("down"))
				if got := flow.dialogContent().title; got != test.wantScrolledTitle {
					t.Fatalf("scrolled rollout title = %q, want %q", got, test.wantScrolledTitle)
				}
			}
		})
	}
}

func TestRolloutOfferPagination(t *testing.T) {
	_, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	flow.radiusLoader.stop()
	flow.radius = k8s.NewRefIndex()
	flow.phase = phaseSaved
	flow.rollout = make([]rolloutItem, 20)
	for i := range flow.rollout {
		flow.rollout[i] = rolloutItem{kind: k8s.KindDeployment, name: strings.Repeat("w", i+1), selected: true}
	}
	_ = flow.rolloutList.SetItems(flow.rolloutChecklistItems())
	flow.SetSize(80, 4)
	flow.refreshContent()
	if title := flow.dialogContent().title; title != "Saved: restart affected workloads?  20/20 selected" {
		t.Fatalf("overflowing rollout title = %q", title)
	}

	_, _ = flow.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if flow.rolloutList.Index() != 1 {
		t.Fatalf("mouse wheel cursor = %d, want 1", flow.rolloutList.Index())
	}
	flow.rolloutList.Select(0)
	for range 8 {
		_, _ = flow.Update(key("down"))
	}
	view := ansi.Strip(flow.rolloutList.View())
	if flow.rolloutList.Index() != 8 || flow.rolloutList.Paginator.Page == 0 || !strings.Contains(view, "> [x] "+k8s.KindDeployment+"/") {
		t.Fatalf("rollout cursor/page/view = %d/%d/%q", flow.rolloutList.Index(), flow.rolloutList.Paginator.Page, view)
	}
	_, _ = flow.Update(key("space"))
	if flow.rollout[8].selected || !strings.Contains(ansi.Strip(flow.rolloutList.View()), "> [ ] "+k8s.KindDeployment+"/") {
		t.Fatalf("rollout toggle at absolute index 8 = selected %t, view %q", flow.rollout[8].selected, flow.rolloutList.View())
	}
}

func TestResourceRevertHintDoesNotAdvertiseReEdit(t *testing.T) {
	flow := newResourceRevertFlow(t.Context(), testClient(), editEnv{}, testSecret(corev1.SecretTypeOpaque, map[string][]byte{"key": []byte("old")}), map[string]string{"key": "new"}, nil, testStyles(true))
	if hint := plainFooter(t, flow, 1); strings.Contains(hint, "re-edit") {
		t.Fatalf("whole-resource undo hint = %q", hint)
	}
}

func TestNewKeyConflictUsesClusterLabels(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	resource := testSecret(corev1.SecretTypeOpaque, map[string][]byte{})
	flow := newKeyRestoreFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, "added", []byte("mine"), h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	enterSaving(h)
	cluster := testSecretWithVersion("11", corev1.SecretTypeOpaque, map[string][]byte{"added": []byte("cluster")})
	h.send(saveDoneMsg{reqID: h.topReqID(), result: k8s.SaveResult{Outcome: k8s.SaveConflict, Cluster: cluster}})

	view := h.view()
	if !strings.Contains(view, "added (cluster now)") || !strings.Contains(view, "added (mine)") || strings.Contains(view, "added (absent)") {
		t.Fatalf("new-key conflict labels are misleading: %q", view)
	}
}

func TestEditFlowDryRunEscCancels(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("mine"))
	h.keys("Y")
	requestID := flow.reqID
	h.keys("esc")
	if flow.phase != phaseDiff || flow.pending {
		t.Fatalf("flow phase = %d pending = %t", flow.phase, flow.pending)
	}
	h.send(dryRunDoneMsg{reqID: requestID, result: k8s.DryRunResult{Outcome: k8s.DryRunRejected, Message: "stale"}})
	if flow.phase != phaseDiff {
		t.Fatalf("stale result changed phase to %d", flow.phase)
	}
}

func TestEditFlowAsyncDispatchUsesSnapshot(t *testing.T) {
	tests := []struct {
		name  string
		start func(*editFlow) tea.Cmd
	}{
		{name: "dry run", start: (*editFlow).startDryRun},
		{name: "save", start: (*editFlow).startSaving},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, flow := proposedFlowHarness(t, []byte("old"), []byte("mine"))
			if err := flow.applyChanges(); err != nil {
				t.Fatal(err)
			}
			cmd := test.start(flow)
			if err := flow.res.Set(flow.key, []byte("changed after dispatch")); err != nil {
				t.Fatal(err)
			}
			clientset := flow.client.Clientset.(*fake.Clientset)
			clientset.ClearActions()

			msg := cmd()
			batch, ok := msg.(tea.BatchMsg)
			if !ok || len(batch) == 0 {
				t.Fatalf("async command = %T, want non-empty tea.BatchMsg", msg)
			}
			batch[0]()

			actions := clientset.Actions()
			if len(actions) != 1 {
				t.Fatalf("client actions = %d, want 1", len(actions))
			}
			update, ok := actions[0].(clienttesting.UpdateAction)
			if !ok {
				t.Fatalf("client action = %T, want testing.UpdateAction", actions[0])
			}
			secret := update.GetObject().(*corev1.Secret)
			if got := string(secret.Data[flow.key]); got != "mine" {
				t.Fatalf("dispatched value = %q, want snapshot value %q", got, "mine")
			}
		})
	}
}

func TestEditFlowAsyncDispatchCapturesNamespace(t *testing.T) {
	originalNamespace := "original"
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: originalNamespace, ResourceVersion: "10"},
		Data:       map[string][]byte{"DB_PASSWORD": []byte("old")},
	})
	otherResource := k8s.NewSecret(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: "changed"}})

	t.Run("blast radius", func(t *testing.T) {
		clientset := fake.NewClientset()
		flow := newEditFlow(t.Context(), &k8s.Client{Clientset: clientset}, editEnv{}, resource, "DB_PASSWORD", []byte("new"), testStyles(true))
		cmd := flow.startBlastRadius()
		flow.res = otherResource

		result, ok := testBlastRadiusMessage(cmd)
		if !ok || result.err != nil {
			t.Fatalf("blast-radius command did not produce a result")
		}
		actions := clientset.Actions()
		if len(actions) == 0 {
			t.Fatal("blast-radius command issued no client actions")
		}
		for _, action := range actions {
			if action.GetNamespace() != originalNamespace {
				t.Fatalf("action namespace = %q, want %q", action.GetNamespace(), originalNamespace)
			}
		}
	})

	t.Run("rollout", func(t *testing.T) {
		clientset := fake.NewClientset(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: originalNamespace}})
		flow := newEditFlow(t.Context(), &k8s.Client{Clientset: clientset}, editEnv{}, resource, "DB_PASSWORD", []byte("new"), testStyles(true))
		flow.rollout = []rolloutItem{{kind: k8s.KindDeployment, name: "web", selected: true}}
		cmd := flow.startRollout()
		flow.res = otherResource

		message := cmd()
		batch, ok := message.(tea.BatchMsg)
		if !ok || len(batch) == 0 {
			t.Fatalf("rollout command = %T, want non-empty tea.BatchMsg", message)
		}
		batch[0]()
		actions := clientset.Actions()
		if got := actions[len(actions)-1].GetNamespace(); got != originalNamespace {
			t.Fatalf("rollout namespace = %q, want %q", got, originalNamespace)
		}
	})

	t.Run("saved message", func(t *testing.T) {
		h, flow := proposedFlowHarness(t, []byte("old"), []byte("mine"))
		resolveNoConsumers(h, flow)
		enterSaving(h)
		_, cmd := flow.Update(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
		flow.res = k8s.NewConfigMap(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "changed", Namespace: "changed"}})

		commandMessage := cmd()
		batch, ok := commandMessage.(tea.BatchMsg)
		if !ok || len(batch) == 0 {
			t.Fatalf("save command = %T, want non-empty tea.BatchMsg", commandMessage)
		}
		savedMessage := batch[0]()
		message, ok := savedMessage.(editSavedMsg)
		if !ok {
			t.Fatalf("save message = %T, want editSavedMsg", savedMessage)
		}
		if message.operationID == 0 {
			t.Fatal("save message has zero operation id")
		}
		if message.outcome.kind != k8s.KindSecret || message.outcome.namespace != "default" || message.outcome.name != "db-creds" {
			t.Fatalf("save message = %+v, want original resource identity", message)
		}
	})
}

func TestEditFlowQuitConfirm(t *testing.T) {
	h, _ := proposedFlowHarness(t, []byte("old"), []byte("mine"))
	h.keys("ctrl+c")
	if h.sawQuit || !strings.Contains(h.view(), "unsaved edit - ctrl+c again to quit") {
		t.Fatalf("first ctrl+c quit = %t, view = %q", h.sawQuit, h.view())
	}
	if rows := strings.Count(h.view(), "\n") + 1; rows != 24 {
		t.Fatalf("armed view rows = %d, want terminal height 24:\n%s", rows, h.view())
	}
	h.keys("ctrl+c")
	if !h.sawQuit {
		t.Fatal("second ctrl+c did not quit")
	}
}

// A pending "press Y" nudge must not hide the quit warning: the second ctrl+c
// discards the edit, so the warning is the only thing standing between a stray
// keystroke and lost work.
func TestEditFlowQuitConfirmOutranksNudge(t *testing.T) {
	resource := testSecret(corev1.SecretTypeTLS, map[string][]byte{corev1.TLSCertKey: []byte("old")})
	h := keyHarness(t, resource)
	flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, corev1.TLSCertKey, []byte("new"), h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	h.keys("Y")
	if flow.phase != phaseValidateWarn {
		t.Fatalf("phase = %d, want phaseValidateWarn", flow.phase)
	}

	h.keys("y")
	if !flow.nudge {
		t.Fatal("lowercase y did not raise the nudge")
	}
	h.keys("ctrl+c")
	if view := h.view(); !strings.Contains(view, "unsaved edit - ctrl+c again to quit") || strings.Contains(view, pressYToConfirm) {
		t.Fatalf("armed view still shows the nudge instead of the quit warning:\n%s", view)
	}
}

func TestReadOnlyAndImmutableBlockEdit(t *testing.T) {
	t.Run("read only", func(t *testing.T) {
		h := keyHarnessOptions(t, editSecret("10", []byte("old")), Options{StartNamespace: "default", ASCII: true, ReadOnly: true})
		depth := len(h.m.(app).stack)
		h.keys("x")
		if _, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*filePromptScreen); !ok {
			t.Fatalf("read-only export top = %T, want filePromptScreen", h.m.(app).stack[len(h.m.(app).stack)-1])
		}
		h.keys("esc", "i", "ctrl+z")
		if len(h.m.(app).stack) != depth {
			t.Fatal("read-only mutation key changed the stack")
		}
		h.keys("enter")
		if _, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*valueScreen); !ok {
			t.Fatalf("enter pushed %T, want valueScreen", h.m.(app).stack[len(h.m.(app).stack)-1])
		}
	})
	t.Run("immutable", func(t *testing.T) {
		immutable := true
		resource := k8s.NewSecret(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: "default", ResourceVersion: "10"},
			Immutable:  &immutable,
			Data:       map[string][]byte{"DB_PASSWORD": []byte("old")},
		})
		h := keyHarness(t, resource)
		depth := len(h.m.(app).stack)
		h.keys("i", "ctrl+z")
		if len(h.m.(app).stack) != depth {
			t.Fatal("immutable mutation key changed the stack")
		}
		h.keys("enter")
		if _, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*valueScreen); !ok {
			t.Fatalf("enter pushed %T, want valueScreen", h.m.(app).stack[len(h.m.(app).stack)-1])
		}
	})
}

func TestCtrlZ(t *testing.T) {
	h := keyHarness(t, editSecret("10", []byte("old")))
	depth := len(h.m.(app).stack)
	h.keys("ctrl+z")
	if len(h.m.(app).stack) != depth {
		t.Fatal("empty undo ring changed the stack")
	}
	screen := h.m.(app).stack[len(h.m.(app).stack)-1].(*keyScreen)
	screen.env.ring.Push(undo.Entry{Context: "test-ctx", Kind: k8s.KindSecret, Namespace: "default", Name: "db-creds", Previous: map[string][]byte{"DB_PASSWORD": []byte("previous")}})
	h.keys("ctrl+z")
	flow, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*editFlow)
	if !ok {
		t.Fatalf("undo pushed %T, want editFlow", h.m.(app).stack[len(h.m.(app).stack)-1])
	}
	if flow.key != "DB_PASSWORD" || string(flow.edited) != "previous" {
		t.Fatalf("undo flow key/value = %q/%q", flow.key, flow.edited)
	}
}

func TestFilePromptsKeepFullSubjectIdentity(t *testing.T) {
	tests := []struct {
		name      string
		resource  k8s.Resource
		key       string
		mode      fileMode
		operation string
		phase     filePromptPhase
		message   string
		warning   string
	}{
		{
			name:      "Secret export completion",
			resource:  editSecret("10", []byte("raw value")),
			key:       "DB_PASSWORD",
			mode:      fileExport,
			operation: "export",
			phase:     filePhaseDone,
			message:   "exported 9 bytes",
			warning:   "plaintext secret",
		},
		{
			name:      "Secret import error",
			resource:  editSecret("10", []byte("old")),
			key:       "DB_PASSWORD",
			mode:      fileImport,
			operation: "import",
			message:   "error: read import",
		},
		{
			name:      "ConfigMap export error",
			resource:  searchConfigMap("default", "app-config", "SETTING"),
			key:       "SETTING",
			mode:      fileExport,
			operation: "export",
			message:   "file exists",
		},
		{
			name:      "ConfigMap import error",
			resource:  searchConfigMap("default", "app-config", "SETTING"),
			key:       "SETTING",
			mode:      fileImport,
			operation: "import",
			message:   "error: inspect import",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := testClient()
			prompt := newFilePrompt(t.Context(), client, editEnv{}, test.resource, test.key, test.mode, testStyles(true))
			prompt.phase = test.phase
			prompt.message = test.message
			prompt.messageIsError = strings.HasPrefix(test.message, "error:")
			prompt.SetSize(80, 22)
			view := ansi.Strip(prompt.View())
			for _, want := range []string{
				test.operation,
				test.resource.Kind() + " " + test.resource.Namespace() + "/" + test.resource.Name(),
				"key " + test.key,
				"context " + client.Context,
				"server " + client.Server,
				test.message,
			} {
				if !strings.Contains(view, want) {
					t.Fatalf("file prompt lost %q:\n%s", want, view)
				}
			}
			if test.warning != "" && !strings.Contains(view, test.warning) {
				t.Fatalf("file prompt lost warning %q:\n%s", test.warning, view)
			}
		})
	}
}

func TestSecretExportKeepsWarningAndIdentityAtMinimumSize(t *testing.T) {
	client := testClient()
	resource := editSecret("10", []byte("raw value"))
	prompt := newFilePrompt(t.Context(), client, editEnv{}, resource, "DB_PASSWORD", fileExport, testStyles(true))
	prompt.phase = filePhaseName
	prompt.dir = "/exports"
	prompt.stat = func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist }
	prompt.refreshNameFeedback()
	prompt.SetSize(60, 13)
	view := ansi.Strip(prompt.View())
	for _, want := range []string{
		"export Secret default/db-creds",
		"key DB_PASSWORD",
		"context test-ctx",
		"test.example",
		"plaintext secret",
		"into",
		"name:",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("minimum-size Secret export lost %q:\n%s", want, view)
		}
	}
}

func TestExportWritesFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	h := keyHarness(t, editSecret("10", []byte("raw value")))
	path := filepath.Join(dir, "exported")
	h.keys("x", "s", "ctrl+u")
	typeText(h, "exported")
	h.keys("enter")
	passCommitGate(h)
	data, err := os.ReadFile(path) // #nosec G304 -- path is created under this test's temporary directory.
	if err != nil || string(data) != "raw value" {
		t.Fatalf("exported data = %q, err = %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("export mode = %v", info.Mode())
	}
	h.keys("esc", "x", "s", "ctrl+u")
	typeText(h, "exported")
	h.keys("enter")
	prompt := h.m.(app).stack[len(h.m.(app).stack)-1].(*filePromptScreen)
	if prompt.phase != filePhaseName || !strings.Contains(h.view(), "file exists - export never overwrites") {
		t.Fatalf("existing-file phase/view = %d/%q", prompt.phase, h.view())
	}
	h.keys("ctrl+u")
	typeText(h, "raced")
	h.keys("enter")
	racePath := filepath.Join(dir, "raced")
	if err := os.WriteFile(racePath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	passCommitGate(h)
	if prompt.phase != filePhaseName || !strings.Contains(h.view(), "file exists - choose another name") {
		t.Fatalf("write-time collision phase/view = %d/%q", prompt.phase, h.view())
	}
	data, _ = os.ReadFile(path) // #nosec G304 -- path is created under this test's temporary directory.
	if string(data) != "raw value" {
		t.Fatalf("existing file changed to %q", data)
	}
	raceData, _ := os.ReadFile(racePath) // #nosec G304 -- path is created under this test's temporary directory.
	if string(raceData) != "existing" {
		t.Fatalf("write-time collision changed file to %q", raceData)
	}
}

func TestImportTooLarge(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	h := keyHarness(t, editSecret("10", []byte("old")))
	path := filepath.Join(dir, "large")
	if err := os.WriteFile(path, make([]byte, maxValueSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	h.keys("i", "enter")
	if _, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*filePromptScreen); !ok || !strings.Contains(h.view(), "exceeds the 1 MiB") {
		t.Fatalf("large import top = %T view = %q", h.m.(app).stack[len(h.m.(app).stack)-1], h.view())
	}
}

func TestImportPushesFlow(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	h := keyHarness(t, editSecret("10", []byte("old")))
	path := filepath.Join(dir, "small")
	if err := os.WriteFile(path, []byte("imported"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.keys("i")
	depth := len(h.m.(app).stack)
	h.keys("enter")
	flow, ok := h.m.(app).stack[len(h.m.(app).stack)-1].(*editFlow)
	if !ok {
		t.Fatalf("import top = %T, want editFlow", h.m.(app).stack[len(h.m.(app).stack)-1])
	}
	if len(h.m.(app).stack) != depth {
		t.Fatalf("import stack depth = %d, want replacement depth %d", len(h.m.(app).stack), depth)
	}
	if !bytes.Equal(flow.edited, []byte("imported")) {
		t.Fatalf("import edited = %q", flow.edited)
	}
}

func TestExportPickerStartsNamePhaseInCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	prompt := newFilePrompt(t.Context(), testClient(), editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", fileExport, testStyles(true))

	updated, _ := prompt.Update(key("s"))
	prompt = updated.(*filePromptScreen)
	if prompt.phase != filePhaseName || prompt.dir != dir || prompt.input.Value() != "DB_PASSWORD" {
		t.Fatalf("export selection phase/dir/name = %d/%q/%q", prompt.phase, prompt.dir, prompt.input.Value())
	}
}

func TestExportNameRoundTripPreservesName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	prompt := newFilePrompt(t.Context(), testClient(), editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", fileExport, testStyles(true))
	updated, _ := prompt.Update(key("s"))
	prompt = updated.(*filePromptScreen)
	prompt.input.SetValue("custom-name")

	updated, _ = prompt.Update(key("tab"))
	prompt = updated.(*filePromptScreen)
	updated, _ = prompt.Update(key("enter"))
	prompt = updated.(*filePromptScreen)
	if prompt.phase != filePhasePick {
		t.Fatalf("directory row enter phase = %d, want pick", prompt.phase)
	}
	updated, _ = prompt.Update(key("s"))
	prompt = updated.(*filePromptScreen)
	if prompt.phase != filePhaseName || prompt.input.Value() != "custom-name" {
		t.Fatalf("re-picked phase/name = %d/%q", prompt.phase, prompt.input.Value())
	}
}

func TestFilePromptEscChain(t *testing.T) {
	prompt := newFilePrompt(t.Context(), testClient(), editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", fileExport, testStyles(true))
	prompt.phase = filePhaseName
	prompt.dir = t.TempDir()

	updated, _ := prompt.Update(key("esc"))
	prompt = updated.(*filePromptScreen)
	if prompt.phase != filePhasePick {
		t.Fatalf("name esc phase = %d, want pick", prompt.phase)
	}
	_, cmd := prompt.Update(key("esc"))
	if cmd == nil {
		t.Fatal("picker esc did not request a pop")
	}

	prompt.phase = filePhaseGate
	updated, _ = prompt.Update(key("esc"))
	prompt = updated.(*filePromptScreen)
	if prompt.phase != filePhaseName || !prompt.input.Focused() {
		t.Fatalf("gate esc phase/focus = %d/%t, want name/true", prompt.phase, prompt.input.Focused())
	}
}

func TestExportPickerSwallowsFileOpen(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "value.txt"), []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	prompt := initializedFilePrompt(t, fileExport)
	originalDir := prompt.picker.CurrentDirectory

	updated, _ := prompt.Update(key("enter"))
	prompt = updated.(*filePromptScreen)
	if prompt.picker.CurrentDirectory != originalDir || prompt.message != "pick a directory - s exports into this directory" || !prompt.messageIsWarning {
		t.Fatalf("file open dir/message/warning = %q/%q/%t", prompt.picker.CurrentDirectory, prompt.message, prompt.messageIsWarning)
	}
}

func TestFilePickerUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 000 directories")
	}
	dir := t.TempDir()
	unreadable := filepath.Join(dir, "private")
	if err := os.Mkdir(unreadable, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	prompt := initializedFilePrompt(t, fileExport)
	if err := os.Chmod(unreadable, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadable, 0o700) }) // #nosec G302 -- the fixture is a private directory that needs owner traversal for cleanup.
	originalDir := prompt.picker.CurrentDirectory

	updated, _ := prompt.Update(key("enter"))
	prompt = updated.(*filePromptScreen)
	if prompt.picker.CurrentDirectory != originalDir || !prompt.messageIsError || !strings.Contains(prompt.message, "[error] cannot open private:") {
		t.Fatalf("unreadable dir/message/error = %q/%q/%t", prompt.picker.CurrentDirectory, prompt.message, prompt.messageIsError)
	}
}

func TestFilePromptNameFeedback(t *testing.T) {
	for _, ascii := range []bool{true, false} {
		mode := "unicode"
		if ascii {
			mode = "ASCII"
		}
		for _, test := range []struct {
			name    string
			exists  bool
			ascii   string
			unicode string
		}{
			{name: "new", ascii: "[success] file is new", unicode: "✓ file is new"},
			{name: "exists", exists: true, ascii: "! file exists - export never overwrites", unicode: "⚠ file exists · export never overwrites"},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				prompt := newFilePrompt(t.Context(), testClient(), editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", fileExport, testStyles(ascii))
				prompt.phase = filePhaseName
				prompt.dir = "/exports"
				prompt.stat = func(string) (fs.FileInfo, error) {
					if test.exists {
						return nil, nil
					}
					return nil, fs.ErrNotExist
				}
				prompt.refreshNameFeedback()
				want := test.unicode
				if ascii {
					want = test.ascii
				}
				if prompt.message != want {
					t.Fatalf("feedback = %q, want %q", prompt.message, want)
				}
			})
		}
	}
}

func TestFilePromptRejectsInvalidNames(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     string
		message   string
		isWarning bool
	}{
		{name: "empty", message: "enter a name first"},
		{name: "slash", value: "nested/file", message: "name must not contain /", isWarning: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			prompt := newFilePrompt(t.Context(), testClient(), editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", fileExport, testStyles(true))
			prompt.phase = filePhaseName
			prompt.dir = t.TempDir()
			prompt.input.SetValue(test.value)

			updated, _ := prompt.Update(key("enter"))
			prompt = updated.(*filePromptScreen)
			if prompt.phase != filePhaseName || prompt.message != test.message || prompt.messageIsWarning != test.isWarning {
				t.Fatalf("phase/message/warning = %d/%q/%t", prompt.phase, prompt.message, prompt.messageIsWarning)
			}
		})
	}
}

func initializedFilePrompt(t *testing.T, mode fileMode) *filePromptScreen {
	t.Helper()
	prompt := newFilePrompt(t.Context(), testClient(), editEnv{}, editSecret("10", []byte("old")), "DB_PASSWORD", mode, testStyles(true))
	msg := prompt.Init()()
	updated, cmd := prompt.Update(msg)
	if cmd != nil {
		t.Fatal("picker initialization returned an unexpected follow-up command")
	}
	return updated.(*filePromptScreen)
}

func proposedPostSaveFlowHarness(t *testing.T, kind string) (*harness, *editFlow) {
	t.Helper()
	t.Cleanup(editor.CleanupAll)
	h := newHarness(t, Options{ASCII: true})
	var resource k8s.Resource
	key := "DB_PASSWORD"
	if kind == k8s.KindConfigMap {
		key = "SETTING"
		resource = k8s.NewConfigMap(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "default", ResourceVersion: "10"},
			Data:       map[string]string{key: "old"},
		})
	} else {
		resource = editSecret("10", []byte("old"))
	}
	flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, key, []byte("new"), h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	return h, flow
}

func proposedFlowHarness(t *testing.T, original, proposed []byte) (*harness, *editFlow) {
	t.Helper()
	t.Cleanup(editor.CleanupAll)
	h := newHarness(t, Options{ASCII: true})
	flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, editSecret("10", original), "DB_PASSWORD", proposed, h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	return h, flow
}

const incompleteOfferSecretValue = "partial-secret-value-that-must-not-enter-the-notice"

func incompleteRolloutOfferHarness(t *testing.T) (*harness, *editFlow, *keyScreen) {
	t.Helper()
	resource := editSecret("10", []byte("old"))
	h := keyHarness(t, resource)
	keyScreen := topKeyScreen(t, h)
	flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, "DB_PASSWORD", []byte(incompleteOfferSecretValue), h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})

	clientset := fake.NewClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{Spec: podSpecWithRef("db-creds", k8s.TagEnv)},
		},
	})
	clientset.PrependReactor("list", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied"))
	})
	flow.client.Clientset = clientset
	result, ok := testBlastRadiusMessage(flow.startBlastRadius())
	if !ok || result.err != nil || result.index == nil {
		t.Fatalf("blast-radius command did not produce a complete result")
	}
	if got := result.index.FailedSources(); !slices.Equal(got, []string{"pods"}) {
		t.Fatalf("failed sources = %v, want [pods]", got)
	}
	h.send(result)
	enterSaving(h)
	preSaveReqID := flow.radiusLoader.reqID
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	if flow.radiusLoader.reqID == preSaveReqID {
		t.Fatal("save reused the pre-editor consumer request")
	}
	result.reqID = flow.radiusLoader.reqID
	h.send(result)
	if flow.phase != phaseSaved || flow.savedResolution() != savedIncompleteRestartOffer || !keyScreen.pending {
		t.Fatalf("save state = phase %d resolution %d key pending %t", flow.phase, flow.savedResolution(), keyScreen.pending)
	}
	return h, flow, keyScreen
}

func testBlastRadiusMessage(cmd tea.Cmd) (blastRadiusMsg, bool) {
	for _, msg := range testCommandMessages(cmd) {
		if result, ok := msg.(blastRadiusMsg); ok {
			return result, true
		}
	}
	return blastRadiusMsg{}, false
}

func testCommandMessages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var messages []tea.Msg
	for _, nested := range batch {
		messages = append(messages, testCommandMessages(nested)...)
	}
	return messages
}

func rolloutOfferHarness(t *testing.T) (*harness, *editFlow, *keyScreen) {
	t.Helper()
	return rolloutOfferHarnessOptions(t, true)
}

func rolloutOfferHarnessOptions(t *testing.T, ascii bool) (*harness, *editFlow, *keyScreen) {
	t.Helper()
	resource := editSecret("10", []byte("old"))
	h := keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: ascii})
	keyScreen := topKeyScreen(t, h)
	flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, "DB_PASSWORD", []byte("new"), h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})

	index := k8s.NewRefIndex()
	index.AddWorkload(k8s.Workload{Kind: k8s.KindDeployment, Name: "web", Namespace: "default", Spec: podSpecWithRef("db-creds", k8s.TagEnv)})
	index.AddWorkload(k8s.Workload{Kind: k8s.KindStatefulSet, Name: "database", Namespace: "default", Spec: podSpecWithRef("db-creds", k8s.TagVolume)})
	h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, index: index})
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
	passCommitGate(h)

	preSaveReqID := flow.radiusLoader.reqID
	h.send(saveDoneMsg{reqID: flow.reqID, result: k8s.SaveResult{Outcome: k8s.SaveSucceeded}})
	if flow.radiusLoader.reqID == preSaveReqID {
		t.Fatal("save reused the pre-editor consumer request")
	}
	h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, index: index})
	if flow.phase != phaseSaved || !keyScreen.pending {
		t.Fatalf("save state = phase %d key pending %t", flow.phase, keyScreen.pending)
	}
	return h, flow, keyScreen
}

func patchActionCount(h *harness) int {
	return len(patchActionNames(h))
}

func patchActionNames(h *harness) []string {
	var names []string
	for _, action := range h.m.(app).client.Clientset.(*fake.Clientset).Actions() {
		patch, ok := action.(clienttesting.PatchAction)
		if ok {
			names = append(names, patch.GetName())
		}
	}
	return names
}

func editorFlowHarness(t *testing.T) (*harness, *editFlow) {
	t.Helper()
	t.Cleanup(editor.CleanupAll)
	h := newHarness(t, Options{ASCII: true, Editor: "true"})
	flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, editSecret("10", []byte("old")), "DB_PASSWORD", nil, h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	return h, flow
}

func resolveNoConsumers(h *harness, flow *editFlow) {
	h.t.Helper()
	h.send(blastRadiusMsg{reqID: flow.radiusLoader.reqID, index: k8s.NewRefIndex()})
}

func enterSaving(h *harness) {
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: h.topReqID(), result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
	passCommitGate(h)
}

// passCommitGate types the literal YES confirmation that every mutating
// dispatch now requires.
func passCommitGate(h *harness) {
	typeText(h, "YES")
	h.keys("enter")
}

func editSecret(resourceVersion string, value []byte) k8s.Resource {
	return k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: "default", ResourceVersion: resourceVersion},
		Data:       map[string][]byte{"DB_PASSWORD": bytes.Clone(value)},
	})
}

func testSecret(secretType corev1.SecretType, data map[string][]byte) k8s.Resource {
	return testSecretWithVersion("10", secretType, data)
}

func testSecretWithVersion(resourceVersion string, secretType corev1.SecretType, data map[string][]byte) k8s.Resource {
	return k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "edit-target", Namespace: "default", ResourceVersion: resourceVersion},
		Type:       secretType,
		Data:       data,
	})
}

type failingGetResource struct {
	k8s.Resource
}

func (failingGetResource) Get(string) ([]byte, error) {
	return nil, errors.New("read failed")
}

func topEditFlow(t *testing.T, h *harness) *editFlow {
	t.Helper()
	top := h.m.(app).stack[len(h.m.(app).stack)-1]
	flow, ok := top.(*editFlow)
	if !ok {
		t.Fatalf("top screen = %T, want editFlow", top)
	}
	return flow
}

func topKeyScreen(t *testing.T, h *harness) *keyScreen {
	t.Helper()
	top := h.m.(app).stack[len(h.m.(app).stack)-1]
	screen, ok := top.(*keyScreen)
	if !ok {
		t.Fatalf("top screen = %T, want keyScreen", top)
	}
	return screen
}

func writeFlowFile(t *testing.T, flow *editFlow, content string) {
	t.Helper()
	if err := os.WriteFile(flow.filePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertResourceValues(t *testing.T, resource k8s.Resource, want map[string][]byte) {
	t.Helper()
	if keys := resource.Keys(); !slices.Equal(keys, slices.Sorted(maps.Keys(want))) {
		t.Fatalf("resource keys = %v, want %v", keys, slices.Sorted(maps.Keys(want)))
	}
	for key, wantValue := range want {
		value, err := resource.Get(key)
		if err != nil || !bytes.Equal(value, wantValue) {
			t.Fatalf("resource key %q = %q, err = %v; want %q", key, value, err, wantValue)
		}
	}
}

func typeText(h *harness, value string) {
	for _, char := range value {
		model, _ := h.m.Update(key(string(char)))
		h.m = model
	}
}

func TestCommitGateBlocksSaveUntilTypedYes(t *testing.T) {
	h, flow := proposedFlowHarness(t, []byte("old"), []byte("new"))
	clientset := flow.client.Clientset.(*fake.Clientset)
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})
	if flow.phase != phaseCommitGate {
		t.Fatalf("post-dry-run phase = %d, want commit gate", flow.phase)
	}
	clientset.ClearActions()

	typeText(h, "yes")
	h.keys("enter")
	if flow.phase != phaseCommitGate || len(clientset.Actions()) != 0 {
		t.Fatalf("lowercase yes phase/actions = %d/%d, want gate and none", flow.phase, len(clientset.Actions()))
	}
	if content := flow.dialogContent(); !content.isWarning || content.message != "type YES in capitals to confirm" {
		t.Fatalf("lowercase yes dialog message/warning = %q/%t", content.message, content.isWarning)
	}

	h.keys("ctrl+u")
	passCommitGate(h)
	if flow.phase != phaseSaving || len(clientset.Actions()) != 1 {
		t.Fatalf("typed YES phase/actions = %d/%d, want saving and one write", flow.phase, len(clientset.Actions()))
	}
}

func TestCommitGateEscLeavesNoTraceInSharedResource(t *testing.T) {
	resource := editSecret("10", []byte("cluster value"))
	h := keyHarness(t, resource)
	flow := newEditFlow(t.Context(), h.m.(app).client, h.m.(app).editEnv, resource, "DB_PASSWORD", []byte("edited value"), h.m.(app).styles)
	h.send(pushScreenMsg{s: flow})
	resolveNoConsumers(h, flow)
	h.keys("Y")
	h.send(dryRunDoneMsg{reqID: flow.reqID, result: k8s.DryRunResult{Outcome: k8s.DryRunOK}})

	h.keys("esc")
	if flow.phase != phaseDiff {
		t.Fatalf("gate esc phase = %d, want diff", flow.phase)
	}
	h.keys("esc")
	value, err := resource.Get("DB_PASSWORD")
	if err != nil || string(value) != "cluster value" {
		t.Fatalf("aborted resource value = %q, err %v; want the cluster value", value, err)
	}
}

func TestRolloutGateGuardsRestartAndKeepsSelection(t *testing.T) {
	h, flow, _ := rolloutOfferHarness(t)
	h.keys("R")
	if flow.phase != phaseRolloutGate || patchActionCount(h) != 0 {
		t.Fatalf("R phase/patches = %d/%d, want rollout gate and none", flow.phase, patchActionCount(h))
	}
	if view := h.view(); !strings.Contains(view, "Restart 1 workload?") || !strings.Contains(view, "Deployment/web") {
		t.Fatalf("rollout gate view missing workload identity:\n%s", view)
	}

	h.keys("esc")
	if flow.phase != phaseSaved || !flow.rollout[0].selected || patchActionCount(h) != 0 {
		t.Fatalf("gate esc phase/selected/patches = %d/%t/%d", flow.phase, flow.rollout[0].selected, patchActionCount(h))
	}

	h.keys("R")
	passCommitGate(h)
	if flow.phase != phaseRollingOut || patchActionCount(h) != 1 {
		t.Fatalf("typed YES phase/patches = %d/%d, want rolling out and one patch", flow.phase, patchActionCount(h))
	}
}
