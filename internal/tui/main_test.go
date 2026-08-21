package tui

import (
	"fmt"
	"os"
	"testing"

	"charm.land/bubbles/v2/textinput"
	"github.com/NoahHakansson/sk64/internal/kubetest"
)

func TestMain(m *testing.M) {
	if err := kubetest.IsolateAmbientCluster(); err != nil {
		fmt.Fprintf(os.Stderr, "isolate ambient cluster: %v\n", err)
		os.Exit(1)
	}
	makeTextInput = func() textinput.Model {
		input := textinput.New()
		input.SetVirtualCursor(false)
		return input
	}
	os.Exit(m.Run())
}

func testStyles(ascii bool) *styles {
	return newStyles(true, newGlyphs(ascii))
}
