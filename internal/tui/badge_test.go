package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/project"
	"github.com/NoahHakansson/sk64/internal/store"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResourceBadgesAlignAcrossRowsAndGlyphModes(t *testing.T) {
	rowTypes := []struct {
		name string
		gap  int
		row  func(*testing.T, *styles, string, string) string
	}{
		{
			name: "resource",
			gap:  1,
			row: func(_ *testing.T, st *styles, kind, name string) string {
				return resourceItem{resource: badgeTestResource(kind, name), styles: st}.Title()
			},
		},
		{
			name: "workload reference",
			gap:  1,
			row: func(_ *testing.T, st *styles, kind, name string) string {
				return refItem{row: refRow{ref: k8s.ResourceRef{Kind: kind, Name: name}}, styles: st}.Title()
			},
		},
		{
			name: "project",
			gap:  2,
			row: func(t *testing.T, st *styles, kind, name string) string {
				link := store.ResourceLink{Kind: kind, Namespace: "default", Name: name}
				return renderedListItem(t, st, projectLinkItem{resource: &link, styles: st}, 80)
			},
		},
		{
			name: "suggestion",
			gap:  1,
			row: func(t *testing.T, st *styles, kind, name string) string {
				row := suggestionRow{sug: project.Suggestion{Kind: kind, Name: name}, ns: "default"}
				return renderedListItem(t, st, suggestionItem{row: &row, styles: st}, 80)
			},
		},
	}

	for _, mode := range []struct {
		name       string
		ascii      bool
		badgeWidth int
	}{
		{name: "unicode", badgeWidth: 2},
		{name: "ASCII", ascii: true, badgeWidth: 3},
	} {
		t.Run(mode.name, func(t *testing.T) {
			st := newStyles(true, newGlyphs(mode.ascii))
			for _, kind := range []string{k8s.KindSecret, k8s.KindConfigMap} {
				t.Run(kind, func(t *testing.T) {
					badge := st.resourceBadge(kind)
					if width := lipgloss.Width(badge); width != mode.badgeWidth {
						t.Fatalf("badge %q width = %d, want %d", badge, width, mode.badgeWidth)
					}
					if height := lipgloss.Height(badge); height != 1 {
						t.Fatalf("badge %q height = %d, want 1", badge, height)
					}
					for _, rowType := range rowTypes {
						t.Run(rowType.name, func(t *testing.T) {
							const name = "item"
							row := rowType.row(t, st, kind, name)
							prefix, _, found := strings.Cut(row, name)
							if !found {
								t.Fatalf("row %q does not contain name %q", ansi.Strip(row), name)
							}
							wantPrefix := badge + strings.Repeat(" ", rowType.gap)
							if plain := ansi.Strip(prefix); plain != wantPrefix {
								t.Fatalf("row prefix = %q, want %q", plain, wantPrefix)
							}
							if width := lipgloss.Width(prefix); width != mode.badgeWidth+rowType.gap {
								t.Fatalf("row prefix width = %d, want %d", width, mode.badgeWidth+rowType.gap)
							}
							if height := lipgloss.Height(row); height != 1 {
								t.Fatalf("row height = %d, want 1: %q", height, ansi.Strip(row))
							}
						})
					}
				})
			}
		})
	}
}

func TestGlyphModesPreserveCellBudgets(t *testing.T) {
	unicode := newGlyphs(false)
	ascii := newGlyphs(true)
	glyphPairs := []struct {
		name                     string
		unicode, ascii           string
		unicodeWidth, asciiWidth int
	}{
		{name: "secret badge", unicode: unicode.secretBadge, ascii: ascii.secretBadge, unicodeWidth: 2, asciiWidth: 3},
		{name: "config map badge", unicode: unicode.configMapBadge, ascii: ascii.configMapBadge, unicodeWidth: 2, asciiWidth: 3},
		{name: "cursor", unicode: unicode.cursorMarker, ascii: ascii.cursorMarker, unicodeWidth: 1, asciiWidth: 1},
		{name: "rollout", unicode: unicode.rolloutMarker, ascii: ascii.rolloutMarker, unicodeWidth: 9, asciiWidth: 9},
		{name: "subPath", unicode: unicode.subPathMarker, ascii: ascii.subPathMarker, unicodeWidth: 9, asciiWidth: 9},
		{name: "warning", unicode: unicode.warnMarker, ascii: ascii.warnMarker, unicodeWidth: 1, asciiWidth: 1},
		{name: "wrap", unicode: unicode.wrapMarker, ascii: ascii.wrapMarker, unicodeWidth: 1, asciiWidth: 1},
		{name: "rule", unicode: unicode.ruleMarker, ascii: ascii.ruleMarker, unicodeWidth: 1, asciiWidth: 1},
		{name: "found", unicode: unicode.foundTag, ascii: ascii.foundTag, unicodeWidth: 7, asciiWidth: 7},
		{name: "not found", unicode: unicode.notFoundTag, ascii: ascii.notFoundTag, unicodeWidth: 16, asciiWidth: 16},
		{name: "active page", unicode: unicode.activePage, ascii: ascii.activePage, unicodeWidth: 1, asciiWidth: 1},
		{name: "inactive page", unicode: unicode.inactivePage, ascii: ascii.inactivePage, unicodeWidth: 1, asciiWidth: 1},
		{name: "divider", unicode: unicode.divider, ascii: ascii.divider, unicodeWidth: 3, asciiWidth: 3},
	}
	for _, pair := range glyphPairs {
		t.Run(pair.name, func(t *testing.T) {
			for _, value := range []byte(pair.ascii) {
				if value > 0x7f {
					t.Fatalf("ASCII marker %q contains non-ASCII byte %#x", pair.ascii, value)
				}
			}
			if width := lipgloss.Width(pair.unicode); width != pair.unicodeWidth {
				t.Fatalf("unicode marker %q width = %d, want %d", pair.unicode, width, pair.unicodeWidth)
			}
			if width := lipgloss.Width(pair.ascii); width != pair.asciiWidth {
				t.Fatalf("ASCII marker %q width = %d, want %d", pair.ascii, width, pair.asciiWidth)
			}
			for _, marker := range []string{pair.unicode, pair.ascii} {
				if height := lipgloss.Height(marker); height != 1 {
					t.Fatalf("marker %q height = %d, want 1", marker, height)
				}
			}
		})
	}

	unicodeBorder := lipgloss.NewStyle().Border(unicode.border)
	asciiBorder := lipgloss.NewStyle().Border(ascii.border)
	if unicodeBorder.GetHorizontalFrameSize() != asciiBorder.GetHorizontalFrameSize() ||
		unicodeBorder.GetVerticalFrameSize() != asciiBorder.GetVerticalFrameSize() {
		t.Fatalf("border frames = unicode %dx%d ASCII %dx%d", unicodeBorder.GetHorizontalFrameSize(), unicodeBorder.GetVerticalFrameSize(), asciiBorder.GetHorizontalFrameSize(), asciiBorder.GetVerticalFrameSize())
	}
}

func TestCursorGuttersPreserveRowColumns(t *testing.T) {
	for _, mode := range []struct {
		name  string
		ascii bool
	}{
		{name: "unicode"},
		{name: "ASCII", ascii: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			st := newStyles(true, newGlyphs(mode.ascii))
			normalGutter := st.listItemStyle.NormalTitle.GetHorizontalFrameSize()
			plainHandGutter := lipgloss.Width("  ")
			selectedHandGutter := lipgloss.Width(st.glyphs.cursorMarker + " ")
			if plainHandGutter != selectedHandGutter || selectedHandGutter != normalGutter {
				t.Fatalf("gutters = plain %d selected %d list %d", plainHandGutter, selectedHandGutter, normalGutter)
			}
			selected := ansi.Strip(st.renderSelectionBand("selected", 20))
			unselected := st.selectionGutter() + "plain"
			selectedPrefix, _, _ := strings.Cut(selected, "selected")
			unselectedPrefix, _, _ := strings.Cut(unselected, "plain")
			if lipgloss.Width(selectedPrefix) != lipgloss.Width(unselectedPrefix) {
				t.Fatalf("label columns differ: selected %q unselected %q", selected, unselected)
			}
		})
	}
}

func badgeTestResource(kind, name string) k8s.Resource {
	metadata := metav1.ObjectMeta{Name: name, Namespace: "default"}
	if kind == k8s.KindSecret {
		return k8s.NewSecret(&corev1.Secret{ObjectMeta: metadata, Type: corev1.SecretTypeOpaque})
	}
	return k8s.NewConfigMap(&corev1.ConfigMap{ObjectMeta: metadata})
}
