package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestConfirmGateAcceptsOnlyExactYes(t *testing.T) {
	tests := []struct {
		name        string
		typed       string
		wantConfirm bool
		wantMessage string
	}{
		{name: "exact word", typed: "YES", wantConfirm: true},
		{name: "padded word", typed: "  YES  ", wantConfirm: true},
		{name: "lowercase", typed: "yes", wantMessage: "type YES in capitals to confirm"},
		{name: "mixed case", typed: "Yes", wantMessage: "type YES in capitals to confirm"},
		{name: "single letter", typed: "y", wantMessage: "type YES in capitals to confirm"},
		{name: "empty", typed: "", wantMessage: "type YES to confirm"},
		{name: "other word", typed: "no", wantMessage: "type YES to confirm"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gate := newConfirmGate(testStyles(true))
			_ = gate.arm()
			for _, char := range test.typed {
				_, _ = gate.handleKey(key(string(char)))
			}
			confirmed, _ := gate.handleKey(key("enter"))
			if confirmed != test.wantConfirm {
				t.Fatalf("confirmed = %t, want %t", confirmed, test.wantConfirm)
			}
			if gate.message != test.wantMessage {
				t.Fatalf("message = %q, want %q", gate.message, test.wantMessage)
			}
		})
	}
}

func TestConfirmGateRearmClearsPreviousAttempt(t *testing.T) {
	gate := newConfirmGate(testStyles(true))
	_ = gate.arm()
	for _, char := range "yes" {
		_, _ = gate.handleKey(key(string(char)))
	}
	if confirmed, _ := gate.handleKey(key("enter")); confirmed {
		t.Fatal("lowercase yes confirmed")
	}
	_ = gate.arm()
	if value := gate.input.Value(); value != "" {
		t.Fatalf("rearmed input value = %q, want empty", value)
	}
	if gate.message != "" {
		t.Fatalf("rearmed message = %q, want empty", gate.message)
	}
	st := testStyles(true)
	if !strings.Contains(ansi.Strip(gate.promptLines(st, false)), "type YES and press enter to confirm") {
		t.Fatalf("prompt lines = %q", gate.promptLines(st, false))
	}
}

func TestConfirmGatePromptStylesYesByRisk(t *testing.T) {
	st := testStyles(true)
	gate := newConfirmGate(st)
	for _, test := range []struct {
		name   string
		danger bool
		want   string
	}{
		{name: "warning", want: st.warnText.Bold(true).Render(confirmGateWord)},
		{name: "danger", danger: true, want: st.errText.Bold(true).Render(confirmGateWord)},
	} {
		t.Run(test.name, func(t *testing.T) {
			prompt := gate.promptLines(st, test.danger)
			if !strings.Contains(prompt, test.want) {
				t.Fatalf("prompt = %q, want styled YES %q", prompt, test.want)
			}
			if plain := ansi.Strip(prompt); !strings.Contains(plain, "type YES and press enter to confirm") {
				t.Fatalf("plain prompt changed: %q", plain)
			}
		})
	}
}
