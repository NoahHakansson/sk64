package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/NoahHakansson/sk64/internal/editor"
	"github.com/charmbracelet/x/ansi"
)

func TestStyledHexLinesPreserveDumpBytes(t *testing.T) {
	value := []byte{'A', '.', 0, 0x1f, 0x20, 0x7e, 0x7f, 0x80, 'z'}
	for _, ascii := range []bool{true, false} {
		got := styledHexLines(value, testStyles(ascii))
		plain := make([]string, len(got))
		for i, line := range got {
			plain[i] = ansi.Strip(line)
		}
		if want := editor.HexDump(value); !reflect.DeepEqual(plain, want) {
			t.Fatalf("ascii=%t styled dump = %q, want %q", ascii, plain, want)
		}
	}
}

func TestStyledHexLinesDistinguishLiteralAndPlaceholderDots(t *testing.T) {
	st := testStyles(true)
	line := styledHexLines([]byte{'.', 0}, st)[0]
	faint := lipgloss.NewStyle().Foreground(st.palette.fgFaint)
	if !strings.Contains(line, st.jsonKey.Render(".")) {
		t.Fatalf("literal dot is not cyan: %q", line)
	}
	if !strings.Contains(line, faint.Render(".")) {
		t.Fatalf("placeholder dot is not faint: %q", line)
	}
}

func TestHexViewErrorUsesStateLine(t *testing.T) {
	for _, ascii := range []bool{true, false} {
		st := testStyles(ascii)
		screen := newHexScreen(valueGetErrorResource{Resource: secretResource("credentials"), err: errors.New("read failed")}, "password", editEnv{}, st)
		screen.SetSize(80, 20)
		view := ansi.Strip(screen.View())
		want := st.stateMarker(stateLineError) + " value unavailable: read failed"
		if !strings.Contains(view, want) {
			t.Fatalf("ascii=%t view = %q, want %q", ascii, view, want)
		}
	}
}
