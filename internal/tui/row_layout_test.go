package tui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/project"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/charmbracelet/x/ansi"
)

type alignmentTestItem struct {
	identity string
	columns  []rowColumn
}

func (i alignmentTestItem) FilterValue() string { return i.identity }
func (i alignmentTestItem) listColumns() (string, []rowColumn) {
	return i.identity, i.columns
}

func TestHandRenderedRowsRespectCellBudgets(t *testing.T) {
	st := testStyles(true)
	longName := strings.Repeat("界", 24)
	longNamespace := strings.Repeat("namespace", 5)
	longPath := strings.Repeat("deep/", 20) + "manifest.yaml"

	tests := []struct {
		name    string
		render  func(int) string
		keeps   string
		drops   string
		atomic  string
		leading string
	}{
		{
			name: "project origin mismatch",
			render: func(width int) string {
				screen := &projectScreen{
					dialog:   newDialog(st, false),
					project:  store.Project{KubeServer: "https://saved.example"},
					ctxState: projectCtxActive,
				}
				link := store.ResourceLink{
					Kind: k8s.KindSecret, Namespace: longNamespace, Name: longName,
					Source:        "manual-provenance-that-is-decorative",
					OriginContext: "other-context-with-a-long-name", OriginServer: "https://other.example",
				}
				item := projectLinkItem{resource: &link, columns: screen.resourceColumns(link), styles: st}
				return renderColumnedTestRow(st, item, width)
			},
			keeps:   st.glyphs.originMismatchTag,
			drops:   "manual-provenance",
			leading: "> [S]",
		},
		{
			name: "project rollout state",
			render: func(width int) string {
				link := store.WorkloadLink{Kind: k8s.KindDeployment, Namespace: longNamespace, Name: longName}
				item := projectLinkItem{workload: &link, styles: st, columns: []rowColumn{
					{text: "refs: 999"},
					{text: st.glyphs.rolloutMarker, critical: true},
				}}
				return renderColumnedTestRow(st, item, width)
			},
			keeps:   "[rollout]",
			drops:   "refs:",
			leading: "> Deployment/",
		},
		{
			name: "search key metadata",
			render: func(width int) string {
				hit := searchHit{
					entry: searchEntry{namespace: longNamespace, kind: k8s.KindSecret, name: longName},
					key:   strings.Repeat("key", 30),
				}
				return renderColumnedTestRow(st, searchItem{hit: hit}, width)
			},
			drops:   "(key:",
			leading: "> namespace",
		},
		{
			name: "suggestion found",
			render: suggestionBudgetedRow(st, suggestionRow{
				sug: project.Suggestion{Kind: k8s.KindSecret, Name: longName, File: longPath, Line: 999, Mode: project.ModeRendered, Detail: strings.Repeat("renderer", 10)},
				ns:  longNamespace, state: rowFound, matched: strings.Repeat("generated-name", 8),
			}),
			keeps:   "[found]",
			drops:   "deep/",
			leading: "> [S]",
		},
		{
			name: "suggestion linked",
			render: suggestionBudgetedRow(st, suggestionRow{
				sug: project.Suggestion{Kind: k8s.KindConfigMap, Name: longName, File: longPath, Mode: project.ModeRendered, Detail: strings.Repeat("overlay", 10)},
				ns:  longNamespace, state: rowLinked,
			}),
			keeps:   "[linked]",
			drops:   "deep/",
			leading: "> [C]",
		},
		{
			name: "suggestion error",
			render: suggestionBudgetedRow(st, suggestionRow{
				sug: project.Suggestion{Kind: k8s.KindSecret, Name: longName, File: longPath, Mode: project.ModeRendered, Detail: strings.Repeat("chart", 10)},
				ns:  longNamespace, state: rowLinkFailed, err: errors.New(strings.Repeat("permission denied ", 10)),
			}),
			keeps:   "link failed",
			drops:   "deep/",
			leading: "> [S]",
		},
	}

	for _, width := range []int{60, 80} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					row := test.render(width)
					if got := ansi.StringWidth(row); got > width {
						t.Fatalf("row width = %d, want <= %d: %q", got, width, ansi.Strip(row))
					}
					plain := ansi.Strip(row)
					if !strings.HasPrefix(plain, test.leading) {
						t.Fatalf("row = %q, want leading identity %q", plain, test.leading)
					}
					if test.keeps != "" && !strings.Contains(plain, test.keeps) {
						t.Fatalf("row = %q, want critical state %q", plain, test.keeps)
					}
					if test.atomic != "" && strings.Contains(plain, "(origin") && !strings.Contains(plain, test.atomic) {
						t.Fatalf("row contains a partial provenance token instead of %q or no token: %q", test.atomic, plain)
					}
					if test.drops != "" && strings.Contains(plain, test.drops) {
						t.Fatalf("row = %q, decorative metadata %q was not shed", plain, test.drops)
					}
				})
			}
		})
	}
}

func TestProjectOriginMismatchMarkerSurvivesSupportedWidths(t *testing.T) {
	modes := []struct {
		name          string
		ascii         bool
		originContext string
		namePart      string
		namespace     string
	}{
		{
			name:          "ASCII",
			ascii:         true,
			originContext: strings.Repeat("other-production-context-", 8),
			namePart:      strings.Repeat("component-", 8),
			namespace:     strings.Repeat("namespace-", 5),
		},
		{
			name:          "Unicode",
			originContext: strings.Repeat("別環境コンテキスト-", 8),
			namePart:      strings.Repeat("サービス-", 8),
			namespace:     strings.Repeat("名前空間-", 5),
		},
	}
	for _, mode := range modes {
		for _, linkType := range []string{"workload", "resource"} {
			for _, width := range []int{60, 80} {
				t.Run(fmt.Sprintf("%s/%s/%d", mode.name, linkType, width), func(t *testing.T) {
					st := testStyles(mode.ascii)
					screen := &projectScreen{
						dialog:   newDialog(st, false),
						project:  store.Project{KubeServer: "https://saved.example"},
						ctxState: projectCtxActive,
					}
					var item projectLinkItem
					if linkType == "workload" {
						link := store.WorkloadLink{
							Kind: k8s.KindDeployment, Namespace: mode.namespace, Name: mode.namePart,
							OriginContext: mode.originContext, OriginServer: "https://other.example",
						}
						index := k8s.NewRefIndex()
						workload := workloadWithRef(link.Kind, link.Name, "1/1 ready", "linked-resource", k8s.TagEnv)
						workload.Namespace = link.Namespace
						index.AddWorkload(workload)
						screen.collectors = map[string]*refsCollector{link.Namespace: {index: index}}
						item = projectLinkItem{workload: &link, columns: screen.workloadColumns(link), styles: st}
					} else {
						link := store.ResourceLink{
							Kind: k8s.KindSecret, Namespace: mode.namespace, Name: mode.namePart, Source: store.SourceManual,
							OriginContext: mode.originContext, OriginServer: "https://other.example",
						}
						index := k8s.NewRefIndex()
						workload := workloadWithRef(k8s.KindDeployment, "consumer", "1/1 ready", link.Name, k8s.TagEnv)
						workload.Namespace = link.Namespace
						index.AddWorkload(workload)
						screen.collectors = map[string]*refsCollector{link.Namespace: {index: index}}
						item = projectLinkItem{resource: &link, columns: screen.resourceColumns(link), styles: st}
					}

					rendered := renderColumnedTestRow(st, item, width)
					plain := ansi.Strip(rendered)
					if got := ansi.StringWidth(rendered); got > width {
						t.Fatalf("row width = %d, want <= %d: %q", got, width, plain)
					}
					if !strings.Contains(plain, st.glyphs.originMismatchTag) {
						t.Fatalf("row lost origin mismatch marker %q: %q", st.glyphs.originMismatchTag, plain)
					}
					if !strings.Contains(plain, st.glyphs.rolloutMarker) {
						t.Fatalf("row lost rollout marker %q: %q", st.glyphs.rolloutMarker, plain)
					}
					if strings.Contains(plain, mode.originContext) {
						t.Fatalf("row retained the full long origin context instead of eliding it: %q", plain)
					}
					if strings.Count(plain, "[") != strings.Count(plain, "]") {
						t.Fatalf("row contains a partial bracket token: %q", plain)
					}
				})
			}
		}
	}
}

func TestRowBudgetingDoesNotMutateStoredColumns(t *testing.T) {
	columns := []rowColumn{
		{text: "decorative metadata"},
		{text: "[linked]", critical: true},
	}
	_ = renderRowColumns(20, strings.Repeat("identity", 5), "...", columns...)
	if got := ansi.Strip(renderRowColumns(80, "identity", "...", columns...)); got != "identity  decorative metadata  [linked]" {
		t.Fatalf("wide render after truncation = %q", got)
	}
}

func TestRenderAlignedRowColumns(t *testing.T) {
	st := testStyles(true)
	alignment := &rowAlignment{identity: 5, columns: []int{4, 0, 3}}
	columns := []rowColumn{{text: "x"}, {}, {text: "z"}}
	fallback := renderRowColumns(8, "id", st.glyphs.ellipsis, columns...)
	tests := []struct {
		name      string
		width     int
		identity  string
		align     *rowAlignment
		columns   []rowColumn
		want      string
		wantPlain string
	}{
		{name: "identity and columns padded", width: 40, identity: "id", align: alignment, columns: columns, wantPlain: "id     x     z"},
		{name: "empty placeholder keeps later column aligned", width: 40, identity: "abcde", align: alignment, columns: []rowColumn{{}, {}, {text: "end"}}, wantPlain: "abcde        end"},
		{name: "all-empty position skipped", width: 40, identity: "id", align: alignment, columns: columns, wantPlain: "id     x     z"},
		{name: "trailing spaces trimmed", width: 40, identity: "id", align: &rowAlignment{identity: 5, columns: []int{4}}, columns: []rowColumn{{}}, wantPlain: "id"},
		{name: "overflow falls back", width: 8, identity: "id", align: alignment, columns: columns, want: fallback},
		{name: "nil alignment falls back", width: 40, identity: "id", columns: columns, want: renderRowColumns(40, "id", st.glyphs.ellipsis, columns...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := renderAlignedRowColumns(test.width, test.identity, st.glyphs.ellipsis, test.align, test.columns...)
			if test.want != "" && got != test.want {
				t.Fatalf("rendered row = %q, want %q", got, test.want)
			}
			if test.wantPlain != "" && ansi.Strip(got) != test.wantPlain {
				t.Fatalf("rendered row = %q, want %q", ansi.Strip(got), test.wantPlain)
			}
		})
	}
}

func TestMeasureRowAlignment(t *testing.T) {
	st := testStyles(true)
	items := []list.Item{
		alignmentTestItem{identity: "one", columns: []rowColumn{{text: st.dim.Render("tiny")}, {}}},
		namespaceItem("not-columned"),
		alignmentTestItem{identity: "longest", columns: []rowColumn{{text: st.errText.Render("wide text")}, {text: "end"}}},
	}
	got := measureRowAlignment(items)
	if got.identity != 7 || !slices.Equal(got.columns, []int{9, 3}) {
		t.Fatalf("alignment = %#v, want identity 7 columns [9 3]", got)
	}
}

func TestRenderRowColumnsKeepsCriticalTokensAtomic(t *testing.T) {
	identity := strings.Repeat("long-identity/", 10) + "resource"
	for _, ascii := range []bool{true, false} {
		st := testStyles(ascii)
		mode := "unicode"
		if ascii {
			mode = "ascii"
		}
		for _, token := range []string{st.glyphs.rolloutMarker, st.glyphs.missingTag, st.glyphs.foundTag, "[linked]"} {
			for _, width := range []int{40, 60, 80} {
				t.Run(fmt.Sprintf("%s/%s/%d", mode, token, width), func(t *testing.T) {
					row := ansi.Strip(renderRowColumns(
						width,
						identity,
						"...",
						rowColumn{text: "decorative metadata"},
						rowColumn{text: token + " " + strings.Repeat("state-detail", 10), critical: true},
					))
					if got := ansi.StringWidth(row); got > width {
						t.Fatalf("row width = %d, want <= %d: %q", got, width, row)
					}
					if !strings.Contains(row, token) {
						t.Fatalf("row lost whole critical token %q: %q", token, row)
					}
					if strings.Count(row, "[") != strings.Count(row, "]") {
						t.Fatalf("row contains a partial bracket token: %q", row)
					}
					if strings.Contains(row, "decorative metadata") {
						t.Fatalf("row retained optional metadata before truncating identity: %q", row)
					}
				})
			}
		}
	}
}

func TestRenderRowColumnsKeepsExactWidthLeadingCriticalToken(t *testing.T) {
	row := ansi.Strip(renderRowColumns(11, "abcdef", "...", rowColumn{text: "[x] detail", critical: true}))
	if row != "abcdef  [x]" {
		t.Fatalf("row = %q, want exact-width critical token", row)
	}
}

func TestTruncateCriticalColumnKeepsLeadingTokensAtomic(t *testing.T) {
	serverMismatchTag := newGlyphs(true).serverMismatchTag
	tests := []struct {
		name  string
		text  string
		width int
		want  string
	}{
		{
			name:  "server mismatch token fits without detail",
			text:  serverMismatchTag + " active https://active.example",
			width: lipgloss.Width(serverMismatchTag),
			want:  serverMismatchTag,
		},
		{
			name:  "server mismatch token is dropped when it cannot fit",
			text:  serverMismatchTag + " active https://active.example",
			width: lipgloss.Width(serverMismatchTag) - 1,
		},
		{
			name:  "provenance token fits without detail",
			text:  "(manual) source detail",
			width: lipgloss.Width("(manual)"),
			want:  "(manual)",
		},
		{
			name:  "provenance token is dropped when it cannot fit",
			text:  "(manual) source detail",
			width: lipgloss.Width("(manual)") - 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ansi.Strip(truncateCriticalColumn(test.text, test.width, "...")); got != test.want {
				t.Fatalf("truncateCriticalColumn() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRenderRowColumnsNeverSplitsMultipleCriticalTokens(t *testing.T) {
	identity := strings.Repeat("identity/", 10) + "resource"
	for _, ascii := range []bool{true, false} {
		st := testStyles(ascii)
		critical := st.glyphs.subPathMarker + " " + st.glyphs.rolloutMarker + " " + strings.Repeat("detail", 20)
		for width := 1; width <= 80; width++ {
			row := ansi.Strip(renderRowColumns(width, identity, "...", rowColumn{text: critical, critical: true}))
			if got := ansi.StringWidth(row); got > width {
				t.Fatalf("ascii=%t width=%d row width = %d: %q", ascii, width, got, row)
			}
			assertWholeBracketTokens(t, row, st.glyphs.subPathMarker, st.glyphs.rolloutMarker)
		}
	}
}

func assertWholeBracketTokens(t *testing.T, row string, allowed ...string) {
	t.Helper()
	if strings.Count(row, "[") != strings.Count(row, "]") {
		t.Fatalf("row contains an unbalanced bracket token: %q", row)
	}
	allowedTokens := make(map[string]struct{}, len(allowed))
	for _, token := range allowed {
		allowedTokens[token] = struct{}{}
	}
	for offset := 0; offset < len(row); {
		open := strings.IndexByte(row[offset:], '[')
		if open < 0 {
			return
		}
		open += offset
		close := strings.IndexByte(row[open:], ']')
		if close < 0 {
			t.Fatalf("row contains an unterminated bracket token: %q", row)
		}
		close += open
		token := row[open : close+1]
		if _, ok := allowedTokens[token]; !ok {
			t.Fatalf("row contains partial or unknown bracket token %q: %q", token, row)
		}
		offset = close + 1
	}
}

func TestRenderRowColumnsDropsCriticalTokenOnlyAfterIdentityFloor(t *testing.T) {
	identity := strings.Repeat("identity", 5)
	for _, ascii := range []bool{true, false} {
		st := testStyles(ascii)
		token := st.glyphs.rolloutMarker
		minimumWidth := lipgloss.Width(token) + lipgloss.Width(rowColumnSeparator) + min(lipgloss.Width(identity), minimumRowIdentityWidth)
		for _, test := range []struct {
			name      string
			width     int
			wantToken bool
		}{
			{name: "identity floor fits", width: minimumWidth, wantToken: true},
			{name: "identity floor does not fit", width: minimumWidth - 1, wantToken: false},
		} {
			t.Run(fmt.Sprintf("ascii=%t/%s", ascii, test.name), func(t *testing.T) {
				row := ansi.Strip(renderRowColumns(test.width, identity, "...", rowColumn{text: token, critical: true}))
				if got := ansi.StringWidth(row); got > test.width {
					t.Fatalf("row width = %d, want <= %d: %q", got, test.width, row)
				}
				if got := strings.Contains(row, token); got != test.wantToken {
					t.Fatalf("whole token present = %t, want %t: %q", got, test.wantToken, row)
				}
				if strings.Count(row, "[") != strings.Count(row, "]") {
					t.Fatalf("row contains a partial bracket token: %q", row)
				}
			})
		}
	}
}

func TestSelectionTreatmentMatchesEveryRowDialect(t *testing.T) {
	st := testStyles(true)
	listModel := newListModel(st, packageDefaultKeyMaps.list)
	listModel.SetSize(40, 10)
	_ = listModel.SetItems([]list.Item{namespaceItem("selected-list"), namespaceItem("plain-list")})

	choice := newCreatePrompt(t.Context(), testClient(), editEnv{}, "default", nil, st)

	rollout := &editFlow{
		dialog: newDialog(st, false),
		phase:  phaseSaved,
		radius: k8s.NewRefIndex(),
		rollout: []rolloutItem{
			{kind: k8s.KindDeployment, name: "selected-rollout", selected: true},
			{kind: k8s.KindDeployment, name: "plain-rollout"},
		},
	}
	rollout.rolloutList = newListModel(st, packageDefaultKeyMaps.list)
	rollout.rolloutList.SetFilteringEnabled(false)
	rollout.rolloutList.SetShowTitle(false)
	rollout.rolloutList.SetShowPagination(false)
	_ = rollout.rolloutList.SetItems(rollout.rolloutChecklistItems())
	rollout.rolloutList.SetSize(40, 2)

	dialects := []struct {
		name            string
		view            string
		selectedLabel   string
		unselectedLabel string
	}{
		{
			name: "Bubbles delegate", view: listModel.View(),
			selectedLabel: "selected-list", unselectedLabel: "plain-list",
		},
		{
			name: "hand-rendered", view: strings.Join([]string{
				st.renderSelectableRow("selected-hand", true, 40),
				st.renderSelectableRow("plain-hand", false, 40),
			}, "\n"),
			selectedLabel: "selected-hand", unselectedLabel: "plain-hand",
		},
		{
			name: "choice prompt", view: choice.View(),
			selectedLabel: k8s.KindSecret, unselectedLabel: k8s.KindConfigMap,
		},
		{
			name: "rollout checklist", view: rollout.rolloutList.View(),
			selectedLabel: "selected-rollout", unselectedLabel: "plain-rollout",
		},
	}

	for _, dialect := range dialects {
		t.Run(dialect.name, func(t *testing.T) {
			selected := ansi.Strip(lineContaining(t, dialect.view, dialect.selectedLabel))
			unselected := ansi.Strip(lineContaining(t, dialect.view, dialect.unselectedLabel))
			if !strings.HasPrefix(selected, "> ") {
				t.Fatalf("selected row = %q, want shared cursor gutter", selected)
			}
			if !strings.HasPrefix(unselected, "  ") {
				t.Fatalf("unselected row = %q, want reserved cursor gutter", unselected)
			}
			if strings.Index(selected, dialect.selectedLabel) != strings.Index(unselected, dialect.unselectedLabel) {
				t.Fatalf("label columns differ: selected %q unselected %q", selected, unselected)
			}
		})
	}

	requireSameColor(t, st.selectedRow.GetForeground(), st.palette.fg)
	requireSameColor(t, st.selectedRow.GetBackground(), st.palette.chromeHigher)
	requireSameColor(t, st.listItemStyle.SelectedTitle.GetBackground(), st.palette.chromeHigher)
	if !st.selectedRow.GetBold() || !st.listItemStyle.SelectedTitle.GetBold() {
		t.Fatal("selected rows must retain bold emphasis when color is disabled")
	}
	const width = 40
	band := st.renderSelectionBand("selected "+st.errText.Render("error"), width)
	if got := lipgloss.Width(band); got != width {
		t.Fatalf("selected band width = %d, want %d", got, width)
	}
	restore := ansi.NewStyle().Bold().ForegroundColor(st.palette.fg).BackgroundColor(st.palette.chromeHigher).String()
	if !strings.Contains(band, ansi.ResetStyle+restore) {
		t.Fatalf("selected band did not restore its background after embedded reset: %q", band)
	}
}

func suggestionBudgetedRow(st *styles, row suggestionRow) func(int) string {
	return func(width int) string {
		return renderColumnedTestRow(st, suggestionItem{row: &row, styles: st, checkCluster: true}, width)
	}
}

func renderColumnedTestRow(st *styles, item interface {
	list.Item
	columnedListItem
}, width int) string {
	model := newListModel(st, packageDefaultKeyMaps.list)
	model.SetSize(width, 3)
	_ = model.SetItems([]list.Item{item})
	var rendered strings.Builder
	newListDelegate(st).Render(&rendered, model, 0, item)
	return rendered.String()
}

func assertLinesFitWidth(t *testing.T, view string, width int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("line width = %d, want <= %d: %q", got, width, ansi.Strip(line))
		}
	}
}

func lineContaining(t *testing.T, view, label string) string {
	t.Helper()
	for _, line := range strings.Split(ansi.Strip(view), "\n") {
		if strings.Contains(line, label) {
			return line
		}
	}
	t.Fatalf("view does not contain %q:\n%s", label, ansi.Strip(view))
	return ""
}
