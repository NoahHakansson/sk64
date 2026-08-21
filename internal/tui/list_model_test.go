package tui

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/config"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/project"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFilteredResourceBadgesKeepANSIWellFormed(t *testing.T) {
	for _, mode := range []struct {
		name  string
		ascii bool
	}{
		{name: "unicode"},
		{name: "ASCII", ascii: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			st := newStyles(true, newGlyphs(mode.ascii))
			link := store.ResourceLink{Kind: k8s.KindSecret, Namespace: "default", Name: "app-secret"}
			suggestion := suggestionRow{sug: project.Suggestion{Kind: k8s.KindSecret, Name: "app-secret"}, ns: "default"}
			for _, test := range []struct {
				name      string
				item      list.Item
				separator string
			}{
				{name: "resource", item: resourceItem{resource: badgeTestResource(k8s.KindSecret, "app-secret"), styles: st}, separator: " "},
				{name: "project", item: projectLinkItem{resource: &link, styles: st}, separator: rowColumnSeparator},
				{name: "suggestion", item: suggestionItem{row: &suggestion, styles: st, checkCluster: true}, separator: " "},
			} {
				t.Run(test.name, func(t *testing.T) {
					model := newListModel(st, packageDefaultKeyMaps.list)
					model.SetSize(40, 10)
					_ = model.SetItems([]list.Item{test.item})
					model.SetFilterText("app")

					plain := ansi.Strip(model.View())
					if !strings.Contains(plain, st.glyphs.secretBadge+test.separator+"app-secret") {
						t.Fatalf("filtered row = %q, want intact badge and name", plain)
					}
					if strings.Contains(plain, "[1;38;") {
						t.Fatalf("filtered row contains a split ANSI sequence: %q", plain)
					}
					if underlined := underlinedANSIText(model.View()); !strings.Contains(underlined, "app") || strings.Contains(underlined, st.glyphs.secretBadge) {
						t.Fatalf("underlined text = %q, want matched name without badge", underlined)
					}
				})
			}
		})
	}
}

func TestColumnedListRowsPreserveCriticalTokens(t *testing.T) {
	st := testStyles(true)
	longName := strings.Repeat("long-name-", 8) + "tail"
	immutable := true
	tests := []struct {
		name     string
		item     list.Item
		optional string
		critical []string
	}{
		{
			name: "resource",
			item: resourceItem{resource: k8s.NewSecret(&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: longName, Namespace: "default"},
				Type:       corev1.SecretTypeOpaque,
				Immutable:  &immutable,
			}), styles: st},
			optional: string(corev1.SecretTypeOpaque),
			critical: []string{st.glyphs.immutableTag},
		},
		{
			name:     "key",
			item:     keyItem{key: longName, size: 4200, binary: true, styles: st},
			optional: "4.1 KiB",
			critical: []string{st.glyphs.binaryTag},
		},
		{
			name: "workload reference",
			item: refItem{row: refRow{ref: k8s.ResourceRef{
				Kind:          k8s.KindSecret,
				Name:          longName,
				Tags:          []k8s.RefTag{k8s.TagVolume},
				SubPath:       true,
				RolloutNeeded: true,
			}, missing: true}, styles: st},
			optional: string(k8s.TagVolume),
			critical: []string{st.glyphs.subPathMarker, st.glyphs.rolloutMarker, st.glyphs.missingTag},
		},
		{
			name: "consumer",
			item: consumerItem{consumer: k8s.Consumer{
				Kind: k8s.KindDeployment,
				Name: longName,
				Ref: k8s.ResourceRef{
					Tags:          []k8s.RefTag{k8s.TagEnv},
					Keys:          []string{"SETTING"},
					SubPath:       true,
					RolloutNeeded: true,
				},
			}, styles: st},
			optional: "env(SETTING)",
			critical: []string{st.glyphs.subPathMarker, st.glyphs.rolloutMarker},
		},
		{
			name: "workload",
			item: workloadItem{entry: k8s.WorkloadEntry{
				Workload: k8s.Workload{Kind: k8s.KindDeployment, Name: longName, Ready: "1/1 ready"},
				Refs:     []k8s.ResourceRef{{Name: "one"}, {Name: "two"}},
			}, styles: st},
			optional: "1/1 ready",
			critical: []string{"refs: 2"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := renderedListItem(t, st, test.item, 60)
			if width := ansi.StringWidth(row); width > 60 {
				t.Fatalf("row width = %d, want <= 60: %q", width, row)
			}
			if strings.Contains(row, test.optional) {
				t.Fatalf("row retained optional column %q before critical state: %q", test.optional, row)
			}
			for _, token := range test.critical {
				if !strings.Contains(row, token) {
					t.Fatalf("row lost critical token %q: %q", token, row)
				}
			}
			if strings.Count(row, "[") != strings.Count(row, "]") {
				t.Fatalf("row contains an unterminated bracket token: %q", row)
			}
		})
	}
}

func TestColumnedListRowDropsTokenWholeWhenItCannotFit(t *testing.T) {
	st := testStyles(true)
	immutable := true
	item := resourceItem{resource: k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("identity", 4), Namespace: "default"},
		Type:       corev1.SecretTypeOpaque,
		Immutable:  &immutable,
	}), styles: st}

	row := renderedListItem(t, st, item, 18)
	if strings.Contains(row, st.glyphs.immutableTag) || strings.Contains(row, "[immut") {
		t.Fatalf("row retained a partial critical token: %q", row)
	}
	if strings.Count(row, "[") != strings.Count(row, "]") {
		t.Fatalf("row contains an unterminated bracket token: %q", row)
	}
}

func TestAppliedFilterMatchesDisplayedNameRunes(t *testing.T) {
	const name = "alpha-needle-omega"
	st := testStyles(true)
	tests := []struct {
		name          string
		item          list.Item
		displayPrefix string
	}{
		{
			name: "workload",
			item: workloadItem{entry: k8s.WorkloadEntry{Workload: k8s.Workload{
				Kind: k8s.KindDeployment, Name: name, Ready: "1/1 ready",
			}}, styles: st, kindColumn: k8s.KindDeployment},
			displayPrefix: "",
		},
		{
			name: "consumer",
			item: consumerItem{consumer: k8s.Consumer{
				Kind: k8s.KindStatefulSet, Name: name,
			}, styles: st},
			displayPrefix: k8s.KindStatefulSet + "/",
		},
		{
			name:          "reference",
			item:          refItem{row: refRow{ref: k8s.ResourceRef{Kind: k8s.KindSecret, Name: name}}, styles: st},
			displayPrefix: st.resourceBadge(k8s.KindSecret) + " ",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := newListModel(st, packageDefaultKeyMaps.list)
			model.SetSize(80, 5)
			_ = model.SetItems([]list.Item{test.item})
			model.SetFilterText("needle")
			if model.FilterState() != list.FilterApplied {
				t.Fatalf("filter state = %v, want applied", model.FilterState())
			}

			matched := displayMatchRunesWithBadgePrefix(test.item, model.MatchesForItem(0), 0)
			start := utf8.RuneCountInString(test.displayPrefix + "alpha-")
			want := []int{start, start + 1, start + 2, start + 3, start + 4, start + 5}
			if !slices.Equal(matched, want) {
				t.Fatalf("highlighted runes = %v, want name range %v", matched, want)
			}
			prefixWidth := utf8.RuneCountInString(test.displayPrefix)
			for _, matchedRune := range matched {
				if matchedRune < prefixWidth {
					t.Fatalf("highlighted rune %d falls in display prefix %q", matchedRune, test.displayPrefix)
				}
			}
			underlined := underlinedANSIText(model.View())
			if !strings.Contains(underlined, "needle") || test.displayPrefix != "" && strings.Contains(underlined, test.displayPrefix) {
				t.Fatalf("underlined text = %q, want name match only", underlined)
			}
		})
	}
}

func TestSuggestionFilterMatchesRenderedNamespaceRunes(t *testing.T) {
	st := testStyles(true)
	row := suggestionRow{sug: project.Suggestion{Kind: k8s.KindSecret, Name: "app-secret"}, ns: "zzspace"}
	item := suggestionItem{row: &row, styles: st, checkCluster: true}
	model := newListModel(st, packageDefaultKeyMaps.list)
	model.SetSize(80, 5)
	_ = model.SetItems([]list.Item{item})
	model.SetFilterText(row.ns)

	matched := displayMatchRunesWithBadgePrefix(item, model.MatchesForItem(0), 0)
	start := utf8.RuneCountInString(row.sug.Name + rowColumnSeparator)
	want := make([]int, utf8.RuneCountInString(row.ns))
	for index := range want {
		want[index] = start + index
	}
	if !slices.Equal(matched, want) {
		t.Fatalf("namespace match runes = %v, want %v", matched, want)
	}

	badgePrefixRunes := utf8.RuneCountInString(item.kindBadge() + " ")
	model.SetFilterText("app")
	matched = displayMatchRunesWithBadgePrefix(item, model.MatchesForItem(0), badgePrefixRunes)
	want = []int{badgePrefixRunes, badgePrefixRunes + 1, badgePrefixRunes + 2}
	if !slices.Equal(matched, want) {
		t.Fatalf("folded badge match runes = %v, want %v", matched, want)
	}
}

func underlinedANSIText(value string) string {
	var result strings.Builder
	underlined := false
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			end := strings.IndexByte(value[index+2:], 'm')
			if end >= 0 {
				for _, parameter := range strings.Split(value[index+2:index+2+end], ";") {
					code, _ := strconv.Atoi(parameter)
					switch code {
					case 0, 24:
						underlined = false
					case 4:
						underlined = true
					}
				}
				index += end + 3
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		if underlined {
			result.WriteRune(r)
		}
		index += size
	}
	return result.String()
}

func TestListStatusRow(t *testing.T) {
	for _, mode := range []struct {
		name      string
		ascii     bool
		separator string
		ellipsis  string
	}{
		{name: "unicode", separator: " · ", ellipsis: "…"},
		{name: "ASCII", ascii: true, separator: " - ", ellipsis: "..."},
	} {
		t.Run(mode.name, func(t *testing.T) {
			st := testStyles(mode.ascii)
			model := newListModel(st, packageDefaultKeyMaps.list)
			model.SetSize(80, 8)
			_ = model.SetItems([]list.Item{namespaceItem("x-one"), namespaceItem("two"), namespaceItem("three")})
			model.SetFilterText("x")

			segments := listStatusSegments(&model, "namespace")
			got := statusRowText(st, 80, append(segments, "all namespaces")...)
			want := `filter "x"` + mode.separator + "1 of 3" + mode.separator + "all namespaces"
			if got != want {
				t.Fatalf("status row = %q, want %q", got, want)
			}

			truncated := statusRowText(st, 20, "a deliberately long status segment")
			if !strings.HasSuffix(truncated, mode.ellipsis) || lipgloss.Width(truncated) > 14 {
				t.Fatalf("truncated status = %q width %d", truncated, lipgloss.Width(truncated))
			}
			if mode.ascii && strings.IndexFunc(truncated, func(r rune) bool { return r > 127 }) >= 0 {
				t.Fatalf("ASCII status leaked Unicode: %q", truncated)
			}

			empty := newListModel(st, packageDefaultKeyMaps.list)
			if got := statusRowText(st, 80, listStatusSegments(&empty, "namespace")...); got != "" {
				t.Fatalf("empty status = %q", got)
			}
			if got := statusRowText(st, 6, "one"); got != "" {
				t.Fatalf("zero-budget status = %q", got)
			}
		})
	}
}

func TestGroupedFilterKeepsHeadingsAndSourceOrder(t *testing.T) {
	targets := []string{"", "alpha needle", "", "needle first-ranked", "unmatched", ""}
	ranks := groupedFilter("needle", targets)
	indexes := make([]int, len(ranks))
	for index, rank := range ranks {
		indexes[index] = rank.Index
	}
	if want := []int{0, 1, 2, 3, 5}; !slices.Equal(indexes, want) {
		t.Fatalf("grouped indexes = %v, want %v", indexes, want)
	}
	defaultMatches := make(map[int][]int)
	for _, rank := range list.DefaultFilter("needle", targets) {
		defaultMatches[rank.Index] = rank.MatchedIndexes
	}
	for _, rank := range ranks {
		if targets[rank.Index] != "" && !slices.Equal(rank.MatchedIndexes, defaultMatches[rank.Index]) {
			t.Fatalf("matches for index %d = %v, want %v", rank.Index, rank.MatchedIndexes, defaultMatches[rank.Index])
		}
	}
}

func firstSelectable(items []list.Item) int {
	for index, item := range items {
		unselectable, ok := item.(unselectableListItem)
		if !ok || !unselectable.unselectableRow() {
			return index
		}
	}
	return 0
}

func TestClampToSelectableSkipsHeadings(t *testing.T) {
	st := testStyles(true)
	items := []list.Item{
		projectHeadingItem{text: "top"},
		namespaceItem("one"),
		projectHeadingItem{text: "middle"},
		namespaceItem("two"),
		projectHeadingItem{text: "bottom"},
	}
	model := newListModel(st, packageDefaultKeyMaps.list)
	model.SetSize(80, 10)
	_ = model.SetItems(items)
	model.Select(firstSelectable(items))
	if got := model.SelectedItem(); got != namespaceItem("one") {
		t.Fatalf("initial selection = %v, want one", got)
	}

	tests := []struct {
		name, want string
		selected   int
		previous   int
	}{
		{name: "down through middle", selected: 2, previous: 1, want: "two"},
		{name: "up through middle", selected: 2, previous: 3, want: "one"},
		{name: "top edge falls forward", selected: 0, previous: 1, want: "one"},
		{name: "bottom edge falls backward", selected: 4, previous: 3, want: "two"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model.Select(test.selected)
			clampToSelectable(&model, test.previous)
			if got := string(model.SelectedItem().(namespaceItem)); got != test.want {
				t.Fatalf("selected = %q, want %q", got, test.want)
			}
		})
	}

	headingsOnly := []list.Item{projectHeadingItem{text: "one"}, projectHeadingItem{text: "two"}}
	_ = model.SetItems(headingsOnly)
	model.Select(0)
	clampToSelectable(&model, 0)
	if model.Index() != 0 {
		t.Fatalf("headings-only selection moved to %d", model.Index())
	}
}

func TestFilterMatchesStayWithOwningStackedList(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	model := h.m.(app)
	projectView := newProjectScreen(
		t.Context(), model.client, nil, "",
		store.Project{Name: "api", RootPath: "/repo", KubeContext: model.client.Context, Namespace: "default"},
		"", scanConfig{}, model.editEnv, model.styles,
	)
	projectView.ctxState = projectCtxActive
	projectView.loaded = true
	projectView.workloads = []store.WorkloadLink{{Kind: k8s.KindDeployment, Namespace: "default", Name: "web"}}
	projectView.resources = []store.ResourceLink{{Kind: k8s.KindSecret, Namespace: "default", Name: "credentials"}}
	_ = projectView.setItems()
	projectView.list.SetFilterText("web")
	projectView.SetSize(80, 22)

	suggestions := newSuggestionScreen(t.Context(), model.client, nil, store.Project{Name: "api"}, scanConfig{}, false, model.editEnv, model.styles)
	suggestions.scanned = true
	suggestions.rows = []suggestionRow{
		{sug: project.Suggestion{Kind: k8s.KindSecret, Name: "credentials"}, ns: "default"},
		{sug: project.Suggestion{Kind: k8s.KindConfigMap, Name: "config"}, ns: "default"},
	}
	_ = suggestions.setItems()
	suggestions.SetSize(80, 22)

	before := filterValues(projectView.list.VisibleItems())
	model.stack = []screen{projectView, suggestions}
	model.stackGeneration++
	h.m = model
	h.send(tea.KeyPressMsg{Code: '/'}, tea.KeyPressMsg{Code: 'c'})

	if got := filterValues(projectView.list.VisibleItems()); !slices.Equal(got, before) {
		t.Fatalf("underlying project items = %v, want unchanged %v", got, before)
	}
	if got := filterValues(suggestions.list.VisibleItems()); !slices.Equal(got, []string{"Secret/credentials  default", "ConfigMap/config  default"}) {
		t.Fatalf("suggestion filter items = %v, want both c matches", got)
	}
}

func TestForeignFilterMatchesDoNotContaminateResourceList(t *testing.T) {
	h := newHarness(t, Options{ASCII: true})
	model := h.m.(app)

	resources := newResourceScreen(t.Context(), model.client, "default", model.editEnv, model.styles)
	resources.all = []k8s.Resource{
		badgeTestResource(k8s.KindSecret, "alpha"),
		badgeTestResource(k8s.KindSecret, "beta"),
	}
	_ = resources.setVisibleItems()
	resources.list.SetFilterText("alpha")
	resources.SetSize(80, 22)

	keys := newKeyScreen(t.Context(), model.client, k8s.KindSecret, "default", "credentials", model.editEnv, model.styles)
	keys.resource = k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "default"},
		Data:       map[string][]byte{"token": []byte("value"), "ca.crt": []byte("certificate")},
	})
	_ = keys.setItems()
	keys.SetSize(80, 22)

	before := filterValues(resources.list.VisibleItems())
	model.stack = []screen{resources, keys}
	model.stackGeneration++
	h.m = model
	h.send(tea.KeyPressMsg{Code: '/'}, tea.KeyPressMsg{Code: 't'})

	if got := filterValues(resources.list.VisibleItems()); !slices.Equal(got, before) {
		t.Fatalf("underlying resource items = %v, want unchanged %v", got, before)
	}
	if got := filterValues(keys.list.VisibleItems()); !slices.Equal(got, []string{"ca.crt", "token"}) {
		t.Fatalf("key filter items = %v, want both t matches", got)
	}
}

func filterValues(items []list.Item) []string {
	values := make([]string, len(items))
	for index, item := range items {
		values[index] = item.FilterValue()
	}
	return values
}

func TestSecondaryColumnsRenderMuted(t *testing.T) {
	st := testStyles(true)
	immutable := true
	resource := resourceItem{resource: k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "resource", Namespace: "default"},
		Type:       corev1.SecretTypeOpaque,
		Immutable:  &immutable,
	}), styles: st}
	_, resourceColumns := resource.listColumns()
	if resourceColumns[0].text != st.dim.Render(string(corev1.SecretTypeOpaque)) {
		t.Fatalf("resource type is not muted: %q", resourceColumns[0].text)
	}
	if resourceColumns[1].text != st.tag.Render(st.glyphs.immutableTag) {
		t.Fatalf("immutable tag gained secondary muting: %q", resourceColumns[1].text)
	}

	key := keyItem{key: "payload", size: 1024, binary: true, styles: st}
	_, keyColumns := key.listColumns()
	if keyColumns[0].text != st.dim.Render("1.0 KiB") {
		t.Fatalf("key size is not muted: %q", keyColumns[0].text)
	}
	if keyColumns[1].text != st.tag.Render(st.glyphs.binaryTag) {
		t.Fatalf("binary tag gained secondary muting: %q", keyColumns[1].text)
	}

	reference := k8s.ResourceRef{Tags: []k8s.RefTag{k8s.TagVolume}, SubPath: true}
	referenceColumns := referenceListColumns(reference, st)
	if referenceColumns[0].text != st.dim.Render(string(k8s.TagVolume)) {
		t.Fatalf("reference tags are not muted: %q", referenceColumns[0].text)
	}
	if referenceColumns[1].text != st.warnText.Render(st.glyphs.subPathMarker) {
		t.Fatalf("critical reference marker gained secondary muting: %q", referenceColumns[1].text)
	}
}

func renderedListItem(t *testing.T, st *styles, item list.Item, width int) string {
	t.Helper()
	model := newListModel(st, packageDefaultKeyMaps.list)
	model.SetSize(width, 3)
	_ = model.SetItems([]list.Item{item})
	for _, line := range strings.Split(ansi.Strip(model.View()), "\n") {
		if strings.HasPrefix(line, st.glyphs.cursorMarker) {
			return strings.TrimPrefix(line, st.glyphs.cursorMarker+" ")
		}
	}
	t.Fatalf("list item did not render: %q", ansi.Strip(model.View()))
	return ""
}

func drainScopedListFilterCmd(t *testing.T, update func(tea.Msg) tea.Cmd, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, nested := range msg {
			drainScopedListFilterCmd(t, update, nested)
		}
	case scopedListFilterMatchesMsg:
		drainScopedListFilterCmd(t, update, update(msg))
	case list.FilterMatchesMsg:
		t.Fatalf("filter command returned unscoped %T", msg)
	}
}

func TestStateLinesUseSharedLabelMessageActionShape(t *testing.T) {
	kinds := []struct {
		name  string
		kind  stateLineKind
		style func(*styles) lipgloss.Style
	}{
		{name: "success", kind: stateLineSuccess, style: func(st *styles) lipgloss.Style { return st.successText }},
		{name: "error", kind: stateLineError, style: func(st *styles) lipgloss.Style { return st.errText }},
		{name: "empty", kind: stateLineEmpty, style: func(st *styles) lipgloss.Style { return st.dim }},
		{name: "incomplete", kind: stateLineIncomplete, style: func(st *styles) lipgloss.Style { return st.warnText }},
		{name: "unknown", kind: stateLineUnknown, style: func(st *styles) lipgloss.Style { return st.warnText }},
	}
	for _, mode := range []struct {
		name  string
		ascii bool
	}{
		{name: "unicode"},
		{name: "ASCII", ascii: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			st := testStyles(mode.ascii)
			for _, action := range []struct {
				name  string
				value string
			}{
				{name: "without action"},
				{name: "with action", value: "ctrl+r to retry"},
			} {
				t.Run(action.name, func(t *testing.T) {
					for _, kind := range kinds {
						t.Run(kind.name, func(t *testing.T) {
							rendered := renderStateLine(st, kind.kind, "state message", action.value, 80)
							line := ansi.Strip(rendered)
							want := st.stateMarker(kind.kind) + " state message"
							if action.value != "" {
								want += st.glyphs.separator + action.value
							}
							if line != want {
								t.Fatalf("state line = %q, want %q", line, want)
							}
							styledMessage := kind.style(st).Render(st.stateMarker(kind.kind) + " state message")
							if !strings.Contains(rendered, styledMessage) {
								t.Fatalf("state line = %q, want semantic run %q", rendered, styledMessage)
							}
						})
					}
				})
			}
		})
	}

	line := renderStateLine(testStyles(true), stateLineError, strings.Repeat("unavailable ", 10), "ctrl+r to retry", 32)
	if width := ansi.StringWidth(line); width > 32 {
		t.Fatalf("state line width = %d, want <= 32: %q", width, ansi.Strip(line))
	}
}

func TestRenderLoadingLineUsesModeMarker(t *testing.T) {
	tests := []struct {
		name, frame, wantMarker string
		ascii                   bool
	}{
		{name: "ASCII ignores frame", ascii: true, frame: "frame", wantMarker: "[loading]"},
		{name: "unicode uses frame", frame: "FRAME", wantMarker: "FRAME"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := testStyles(test.ascii)
			got := ansi.Strip(renderLoadingLine(st, test.frame, "loading resources", "esc to cancel", 80))
			want := test.wantMarker + " loading resources" + st.glyphs.separator + "esc to cancel"
			if got != want {
				t.Fatalf("loading line = %q, want %q", got, want)
			}
		})
	}
}

func TestStateLineGlyphModesKeepSameLayout(t *testing.T) {
	for _, test := range []struct {
		name   string
		action string
		width  int
	}{
		{name: "without action", width: 24},
		{name: "with action", action: "ctrl+r to retry", width: 32},
	} {
		t.Run(test.name, func(t *testing.T) {
			unicodeLine := ansi.Strip(renderStateLine(testStyles(false), stateLineIncomplete, "scan incomplete", test.action, test.width))
			asciiLine := ansi.Strip(renderStateLine(testStyles(true), stateLineIncomplete, "scan incomplete", test.action, test.width))
			if unicodeLines, asciiLines := strings.Count(unicodeLine, "\n"), strings.Count(asciiLine, "\n"); unicodeLines != asciiLines {
				t.Fatalf("line breaks differ: Unicode %d, ASCII %d", unicodeLines, asciiLines)
			}
			if unicodeWidth, asciiWidth := ansi.StringWidth(unicodeLine), ansi.StringWidth(asciiLine); unicodeWidth > asciiWidth {
				t.Fatalf("Unicode line width = %d, want <= ASCII width %d", unicodeWidth, asciiWidth)
			}
		})
	}
}

func TestDetailedListElisionUsesActiveMarker(t *testing.T) {
	for _, test := range []struct {
		name       string
		ascii      bool
		marker     string
		unexpected string
	}{
		{name: "unicode", marker: "…", unexpected: "..."},
		{name: "ASCII", ascii: true, marker: "...", unexpected: "…"},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := newListModel(testStyles(test.ascii), packageDefaultKeyMaps.list)
			applyDetailedListStyles(&model, testStyles(test.ascii))
			model.SetSize(24, 5)
			_ = model.SetItems([]list.Item{contextItem{styles: testStyles(test.ascii), info: k8s.ContextInfo{
				Name: "ctx", Cluster: "cluster", Server: "https://gateway.example/very/long/path", Namespace: "default",
			}}})

			view := ansi.Strip(model.View())
			description := lineContaining(t, view, "server:")
			if !strings.Contains(description, test.marker) || strings.Contains(description, test.unexpected) {
				t.Fatalf("description elision = %q, want %q without %q", description, test.marker, test.unexpected)
			}
			if width := ansi.StringWidth(description); width > model.Width() {
				t.Fatalf("description width = %d, want <= %d: %q", width, model.Width(), description)
			}
		})
	}
}

func TestListStateSuppressesBubblesEmptyOutput(t *testing.T) {
	model := newListModel(testStyles(true), packageDefaultKeyMaps.list)
	model.SetSize(40, 5)
	stateLine := "[loading] loading namespaces - esc to cancel"

	view := renderListWithoutPrematureEmpty(model, stateLine != "")
	if strings.Contains(view, "No items.") {
		t.Fatalf("pending list rendered Bubbles empty output: %q", view)
	}
	if lines := strings.Count(view, "\n") + 1; lines != model.Height() {
		t.Fatalf("replacement body height = %d, want %d", lines, model.Height())
	}
}

func TestAsyncStateKeepsActiveFilterVisible(t *testing.T) {
	model := newListModel(testStyles(true), packageDefaultKeyMaps.list)
	model.SetSize(40, 5)
	model.SetFilterState(list.Filtering)

	view := ansi.Strip(renderListWithoutPrematureEmpty(model, true))
	if !strings.Contains(view, "Filter:") || strings.Contains(view, "No items.") {
		t.Fatalf("active filter hidden by async state: %q", view)
	}
}

func TestFilteredListStateNamesRefineKey(t *testing.T) {
	st := testStyles(true)
	km := defaultKeyMaps()
	applyKeybinds(km, config.Overrides{config.ActionFilter: {"alt+f"}})
	model := newListModel(st, km.list)
	model.SetSize(40, 5)
	_ = model.SetItems([]list.Item{namespaceItem("default")})
	model.SetFilterText("missing")

	line := ansi.Strip(filteredListState(model, st, 40))
	if line != "[empty] no matches - alt+f to refine" {
		t.Fatalf("filtered state line = %q", line)
	}
	if view := renderListWithoutPrematureEmpty(model, line != ""); strings.Contains(view, "No items.") {
		t.Fatalf("filtered list rendered Bubbles empty output: %q", view)
	}
}

func TestListRowsAreCompact(t *testing.T) {
	for _, test := range []struct {
		name  string
		ascii bool
	}{
		{name: "unicode"},
		{name: "ASCII", ascii: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			st := testStyles(test.ascii)
			model := newListModel(st, packageDefaultKeyMaps.list)
			model.SetSize(40, 10)
			_ = model.SetItems([]list.Item{namespaceItem("one"), namespaceItem("two"), namespaceItem("three")})
			lines := strings.Split(ansi.Strip(model.View()), "\n")
			var rows []string
			for _, line := range lines {
				if strings.Contains(line, "one") || strings.Contains(line, "two") || strings.Contains(line, "three") {
					rows = append(rows, line)
				}
			}
			if len(rows) != 3 {
				t.Fatalf("list rows = %q, want three rows", rows)
			}
			for index := range 2 {
				first := slices.Index(lines, rows[index])
				second := slices.Index(lines, rows[index+1])
				if second != first+1 {
					t.Fatalf("rows are not adjacent: %q", lines)
				}
			}
			if !strings.HasPrefix(rows[0], st.glyphs.cursorMarker) {
				t.Fatalf("selected row = %q, want marker %q", rows[0], st.glyphs.cursorMarker)
			}
		})
	}
}
