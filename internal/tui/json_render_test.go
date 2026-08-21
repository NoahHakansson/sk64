package tui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/charmbracelet/x/ansi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPrettyJSONDetection(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "object", value: `{"a":1}`, want: true},
		{name: "array", value: `[1,2]`, want: true},
		{name: "surrounding whitespace", value: "  {\"a\":1}\n ", want: true},
		{name: "number", value: `123`},
		{name: "string", value: `"str"`},
		{name: "true", value: `true`},
		{name: "null", value: `null`},
		{name: "empty"},
		{name: "trailing garbage", value: `{"a":1} trailing`},
		{name: "incomplete object", value: `{`},
		{name: "plain text", value: `not json`},
		{name: "PEM", value: "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----"},
		{name: "YAML", value: "a: 1\nb: two\n"},
		{name: "brackets in string", value: `{"a":"text with { and [ inside"}`, want: true},
		{name: "deep brackets after escaped quote in string", value: `{"a":"\"` + strings.Repeat("[", maxPrettyJSONDepth+1) + `"}`, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, got := prettyJSON([]byte(test.value))
			if got != test.want {
				t.Fatalf("prettyJSON(%q) detected = %t, want %t", test.value, got, test.want)
			}
		})
	}
}

func TestPrettyJSONPreservesSemantics(t *testing.T) {
	values := []string{
		`{"number":1e3,"decimal":1.0,"negativeZero":-0}`,
		`{"z":1,"a":2,"z":3}`,
		`{"unicode":"\u263A","slash":"\/","backslash":"\\"}`,
		` [ { "nested" : true }, null, false ] `,
	}
	for _, value := range values {
		pretty, ok := prettyJSON([]byte(value))
		if !ok {
			t.Fatalf("prettyJSON(%q) was not detected", value)
		}
		var got, want bytes.Buffer
		if err := json.Compact(&got, []byte(pretty)); err != nil {
			t.Fatal(err)
		}
		if err := json.Compact(&want, bytes.TrimSpace([]byte(value))); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Bytes(), want.Bytes()) {
			t.Fatalf("compact pretty JSON = %q, want %q", got.Bytes(), want.Bytes())
		}
	}
}

func TestPrettyJSONSizeLimit(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{
			name:  "input exceeds limit before indenting",
			value: `{"value":"` + strings.Repeat("x", maxPrettyJSONInputBytes) + `"}`,
		},
		{
			name:  "indented output exceeds limit",
			value: strings.Repeat("[", maxPrettyJSONDepth) + strings.Repeat("0,", 8192) + "0" + strings.Repeat("]", maxPrettyJSONDepth),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := prettyJSON([]byte(test.value)); ok {
				t.Fatal("prettyJSON() detected oversized JSON")
			}
		})
	}
}

// The depth guard exists to bound work before it is done, so asserting only
// that the value is rejected proves nothing — the output cap rejects it too,
// after json.Indent has already built a gigabyte. Allocation is the property
// that actually distinguishes them: ~3 KB with the guard, ~1.3 GB without.
func TestPrettyJSONRejectsDeepNestingCheaply(t *testing.T) {
	deep := []byte(strings.Repeat("[", 10000) + "0" + strings.Repeat("]", 10000))

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if _, ok := prettyJSON(deep); ok {
		t.Fatal("prettyJSON() accepted deeply nested JSON")
	}
	runtime.ReadMemStats(&after)

	const budget = 1 << 20
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > budget {
		t.Fatalf("prettyJSON() allocated %d bytes rejecting deep nesting, want under %d: json.Indent ran before the depth guard", allocated, budget)
	}
}

func TestColorizeJSONAddsOnlyStyling(t *testing.T) {
	pretty := "{\n  \"key:with-colon\": \"escaped \\\"quote\\\" and {\",\n  \"number\": -1.2e+3,\n  \"values\": [true, false, null, {\"unicode\": \"höj 😀\"}]\n}"
	if got := ansi.Strip(colorizeJSON(pretty, testStyles(false))); got != pretty {
		t.Fatalf("colorizeJSON() stripped = %q, want %q", got, pretty)
	}
}

func TestColorizeJSONPreservesNonASCIIOutsideStrings(t *testing.T) {
	input := "å"
	if got := ansi.Strip(colorizeJSON(input, testStyles(false))); got != input {
		t.Fatalf("colorizeJSON() stripped = %q, want %q", got, input)
	}
}

func TestColorizeJSONTokenStyles(t *testing.T) {
	st := testStyles(false)
	colored := colorizeJSON(`{"a":"value","n":1,"b":false,"x":null}`, st)
	for name, want := range map[string]string{
		"key":         st.jsonKey.Render(`"a"`),
		"string":      st.jsonString.Render(`"value"`),
		"number":      st.jsonNumber.Render("1"),
		"boolean":     st.jsonLiteral.Render("false"),
		"punctuation": st.dim.Render("{"),
		"null":        st.jsonLiteral.Render("null"),
	} {
		if !strings.Contains(colored, want) {
			t.Errorf("colorizeJSON() missing %s token %q in %q", name, want, colored)
		}
	}
}

func TestValueScreenIsDisplayOnly(t *testing.T) {
	plain := []byte(strings.Repeat(" ", 100))
	plainResource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "plain-secret", Namespace: "default"},
		Data:       map[string][]byte{"value": bytes.Clone(plain)},
	})
	plainViewer := newValueScreen(plainResource, "value", editEnv{}, testStyles(true))
	plainViewer.SetSize(79, 24)
	wrappedPlain := ansi.Strip(plainViewer.viewport.GetContent())
	if got := strings.ReplaceAll(wrappedPlain, "\n", ""); got != string(plain) {
		t.Fatalf("wrapped plain display = %q, want %q", got, plain)
	}

	original := []byte(`[{"id":"district-sodervik","organization_id":"org_0TEST"}]`)
	resource := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "json-secret", Namespace: "default", ResourceVersion: "10"},
		Data:       map[string][]byte{"TENANTS": bytes.Clone(original)},
	})
	readOnly := keyHarnessOptions(t, resource, Options{StartNamespace: "default", ASCII: true, ReadOnly: true})
	readOnly.keys("enter")
	viewer := readOnly.m.(app).stack[len(readOnly.m.(app).stack)-1].(*valueScreen)
	if ansi.Strip(viewer.display) == string(original) {
		t.Fatal("display buffer contains the stored compact JSON instead of the display-only form")
	}

	stored, err := resource.Get("TENANTS")
	if err != nil || !bytes.Equal(stored, original) {
		t.Fatalf("stored value = %q, %v; want %q", stored, err, original)
	}
	view := readOnly.view()
	if !strings.Contains(view, "json - pretty-printed for display; the stored value is unchanged") || !strings.Contains(view, "\"id\": \"district-sodervik\"") || strings.Contains(view, string(original)) {
		t.Fatalf("JSON viewer did not show only the display form:\n%s", view)
	}

	readOnly.keys("esc", "x")
	prompt := readOnly.m.(app).stack[len(readOnly.m.(app).stack)-1].(*filePromptScreen)
	exportDir := t.TempDir()
	prompt.phase = filePhaseName
	prompt.dir = exportDir
	prompt.input.SetValue("tenants.json")
	prompt.refreshNameFeedback()
	exportPath := filepath.Join(exportDir, "tenants.json")
	readOnly.keys("enter")
	passCommitGate(readOnly)
	exported, err := os.ReadFile(exportPath) // #nosec G304 -- the path is created under this test's private temporary directory.
	if err != nil || !bytes.Equal(exported, original) {
		t.Fatalf("exported value = %q, %v; want %q", exported, err, original)
	}

	writable := keyHarness(t, resource)
	writable.keys("enter")
	flow := topEditFlow(t, writable)
	seeded, err := os.ReadFile(flow.filePath)
	if err != nil || !bytes.Equal(seeded, original) {
		t.Fatalf("editor seed = %q, %v; want %q", seeded, err, original)
	}
}
