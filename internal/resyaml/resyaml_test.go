package resyaml

import (
	"bytes"
	"fmt"
	"maps"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/NoahHakansson/sk64/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRoundTripProperty(t *testing.T) {
	fragments := []string{"", "\n", "\r\n", "\r", "\u2028", "\u2029", "true", "no", "y", "null", "~", "0755", "0x1A", "1e3", ".inf", "*a", "@x", "#c", "- x", "a: b", " lead", "trail ", "\t", "\x00", "unicode 😀", "line1\nline2", "ends\n", "ends\n\n", `"`, `\`, `[{"id":"x"}]`, `a"b `, ` "lead`, `{"k":"v"}`}
	keys := []string{"y", "n", "no", "true", "123", "alpha", "a.b", "with_key", "dash-key"}
	unicodeRunes := []rune("abcåäö世界🙂")
	for iteration := range 300 {
		random := rand.New(rand.NewSource(int64(iteration))) // #nosec G404 -- deterministic test data, not security-sensitive randomness.
		values := make(map[string]string)
		count := random.Intn(7)
		for len(values) < count {
			key := keys[random.Intn(len(keys))]
			if _, exists := values[key]; exists {
				key = fmt.Sprintf("key-%d-%d", iteration, len(values))
			}
			value := fragments[random.Intn(len(fragments))]
			for range random.Intn(5) {
				value += string(unicodeRunes[random.Intn(len(unicodeRunes))])
			}
			values[key] = value
		}
		serialized, err := SerializeValues(values)
		if err != nil {
			t.Fatalf("iteration %d SerializeValues(%q): %v", iteration, values, err)
		}
		parsed, err := Parse(serialized)
		if err != nil || !maps.Equal(parsed, values) {
			t.Fatalf("iteration %d round trip map %q: got %q, err %v\n%s", iteration, values, parsed, err, serialized)
		}
	}
}

func TestSerializeCoercionFixtures(t *testing.T) {
	fixtures := []string{"true", "false", "yes", "no", "y", "n", "on", "off", "null", "~", "", "0755", "0x1A", "1e3", "1.5", "123", "-7", ".inf", "#c", "- x", "a: b", "*a", "@x", " lead", "trail "}
	for _, fixture := range fixtures {
		t.Run(fmt.Sprintf("%q", fixture), func(t *testing.T) {
			serialized, err := SerializeValues(map[string]string{"k": fixture})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := Parse(serialized)
			if err != nil || parsed["k"] != fixture {
				t.Fatalf("round trip = %q, %v; document %q", parsed["k"], err, serialized)
			}
			if !bytes.Contains(serialized, []byte(`"`)) {
				t.Fatalf("coercion-prone scalar was not quoted: %q", serialized)
			}
		})
	}
}

func TestSerializeQuotedStringDoesNotEscapeHTMLCharacters(t *testing.T) {
	value := "a&b<c: d>"
	serialized, err := SerializeValues(map[string]string{"k": value})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(serialized, []byte(value)) {
		t.Fatalf("serialized value was HTML-escaped: %q", serialized)
	}
	parsed, err := Parse(serialized)
	if err != nil || parsed["k"] != value {
		t.Fatalf("round trip = %q, %v; document %q", parsed["k"], err, serialized)
	}
}

// encoding/json escapes only below U+0020, so these runes reached the document
// as raw bytes and YAML rejected them. Found by FuzzRoundTrip on U+007F; the
// rest of the set shares the cause, and U+0080-U+009F are not flagged by
// IsBinaryValue, so they survive FromResource and reach a real edit.
func TestSerializeRunesYAMLRejectsLiteral(t *testing.T) {
	runes := []rune{0x7f, 0x80, 0x85, 0x9f, 0xfffe, 0xffff}
	for _, r := range runes {
		t.Run(fmt.Sprintf("U+%04X", r), func(t *testing.T) {
			value := "before" + string(r) + "after"
			serialized, err := SerializeValues(map[string]string{"k": value})
			if err != nil {
				t.Fatalf("SerializeValues() error = %v", err)
			}
			parsed, err := Parse(serialized)
			if err != nil || parsed["k"] != value {
				t.Fatalf("round trip = %q, %v; document %q", parsed["k"], err, serialized)
			}
		})
	}
}

func TestSerializeBlockScalars(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "strip indicator", value: "a\nb", want: "|-"},
		{name: "clip indicator", value: "a\nb\n", want: "|\n"},
		{name: "keep indicator", value: "a\nb\n\n", want: "|+"},
		{name: "blank line", value: "a\n\nb\n", want: "|\n"},
		{name: "CRLF falls back to quoting", value: "a\r\nb"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serialized, err := SerializeValues(map[string]string{"k": test.value})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := Parse(serialized)
			if err != nil || parsed["k"] != test.value {
				t.Fatalf("round trip %q = %q, %v; document %q", test.value, parsed["k"], err, serialized)
			}
			if test.want != "" && !strings.Contains(string(serialized), "k: "+test.want) {
				t.Fatalf("SerializeValues(%q) = %q, want %q", test.value, serialized, test.want)
			}
			if strings.Contains(test.value, "\r") && strings.Contains(string(serialized), "k: |") {
				t.Fatalf("CRLF value used a block scalar: %q", serialized)
			}
		})
	}
}

func TestSerializeSingleLineBlockScalars(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantStyle string
	}{
		{name: "JSON array", value: `[{"id":"district-sodervik","organization_id":"org_0TEST"}]`, wantStyle: "block"},
		{name: "JSON object", value: `{"a":"b"}`, wantStyle: "block"},
		{name: "plain quote and backslash", value: `a"b\c`, wantStyle: "plain"},
		{name: "quote", value: `"`, wantStyle: "block"},
		{name: "plain quote", value: `he said "hi"`, wantStyle: "plain"},
		{name: "empty", value: "", wantStyle: "quoted"},
		{name: "leading space", value: ` "lead`, wantStyle: "quoted"},
		{name: "trailing space", value: `x" `, wantStyle: "quoted"},
		{name: "carriage return", value: "a\"b\r", wantStyle: "quoted"},
		{name: "tab", value: "a\"b\tc", wantStyle: "quoted"},
		{name: "coercion guard", value: "true", wantStyle: "quoted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			serialized, err := SerializeValues(map[string]string{"k": test.value})
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := Parse(serialized)
			if err != nil || parsed["k"] != test.value {
				t.Fatalf("round trip %q = %q, %v; document %q", test.value, parsed["k"], err, serialized)
			}
			document := string(serialized)
			switch test.wantStyle {
			case "block":
				if !strings.Contains(document, "k: |-\n") {
					t.Fatalf("SerializeValues(%q) = %q, want block scalar", test.value, serialized)
				}
			case "plain":
				if strings.Contains(document, "k: |") || strings.Contains(document, `k: "`) {
					t.Fatalf("SerializeValues(%q) = %q, want plain scalar", test.value, serialized)
				}
			case "quoted":
				if !strings.Contains(document, `k: "`) || strings.Contains(document, "k: |") {
					t.Fatalf("SerializeValues(%q) = %q, want quoted scalar", test.value, serialized)
				}
			}
		})
	}
}

func TestSerializeLongKey(t *testing.T) {
	key := strings.Repeat("k", 1025)
	serialized, err := SerializeValues(map[string]string{key: "8"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(serialized, []byte("? ")) {
		t.Fatalf("SerializeValues() = %q, want explicit mapping key", serialized)
	}
	parsed, err := Parse(serialized)
	if err != nil || parsed[key] != "8" {
		t.Fatalf("round trip = %q, %v; document %q", parsed[key], err, serialized)
	}
}

func TestSerializeHeaderAndBinaryComments(t *testing.T) {
	header := Header{Kind: k8s.KindSecret, Name: "db-creds", Namespace: "prod", ResourceVersion: "12345678"}
	document, err := Serialize(header, map[string]string{"password": "secret"}, []BinaryKey{{Name: "session.db", Size: 4200}}, " — ")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"# resource: secret/db-creds (namespace: prod)",
		"# resourceVersion: 12345678",
		"# Binary keys are omitted (use x/i export/import) — listed below.",
		"# Deleting a mapping deletes the key on save; adding one adds a key.",
		"# omitted (binary): session.db [4.1 KiB]",
	} {
		if !bytes.Contains(document, []byte(line)) {
			t.Fatalf("document missing %q:\n%s", line, document)
		}
	}
	parsed, err := Parse(document)
	if err != nil || !reflect.DeepEqual(parsed, map[string]string{"password": "secret"}) {
		t.Fatalf("Parse() = %v, %v", parsed, err)
	}
	withoutBinary, err := Serialize(header, map[string]string{}, nil, " — ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(withoutBinary, []byte("Binary keys are omitted")) {
		t.Fatalf("empty binary list emitted binary header: %s", withoutBinary)
	}
}

func TestSerializeUsesConfiguredSeparator(t *testing.T) {
	for _, test := range []struct {
		name      string
		separator string
	}{
		{name: "unicode", separator: " — "},
		{name: "ASCII", separator: " - "},
	} {
		t.Run(test.name, func(t *testing.T) {
			document, err := Serialize(
				Header{Kind: k8s.KindSecret, Name: "db-creds", Namespace: "default"},
				nil,
				[]BinaryKey{{Name: "payload", Size: 4}},
				test.separator,
			)
			if err != nil {
				t.Fatal(err)
			}
			want := "# Binary keys are omitted (use x/i export/import)" + test.separator + "listed below."
			if !strings.Contains(string(document), want) {
				t.Fatalf("document missing %q:\n%s", want, document)
			}
		})
	}
}

func TestSerializeOmitsEmptyResourceVersion(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		wantHeader bool
	}{
		{name: "empty"},
		{name: "set", version: "123", wantHeader: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, err := Serialize(Header{Kind: k8s.KindSecret, Name: "new", Namespace: "default", ResourceVersion: test.version}, map[string]string{"key": "value"}, nil, " — ")
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(document), "# resourceVersion:"); got != test.wantHeader {
				t.Fatalf("resourceVersion header present = %t, want %t\n%s", got, test.wantHeader, document)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{name: "number", doc: "port: 8080\n", want: []string{"port", "number"}},
		{name: "null", doc: "k:\n", want: []string{"k", `use ""`}},
		{name: "mapping", doc: "k:\n  nested: value\n", want: []string{"k", "mapping"}},
		{name: "top-level list", doc: "- value\n", want: []string{"top level", "mapping"}},
		{name: "duplicate", doc: "k: one\nk: two\n", want: []string{"already set"}},
		{name: "syntax", doc: "k: [\n", want: []string{"parse YAML"}},
		{name: "sorted bad keys", doc: "z: true\na: 1\n", want: []string{`"a"`, `"z"`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.doc))
			if err == nil {
				t.Fatal("Parse() error = nil")
			}
			message := err.Error()
			for _, want := range test.want {
				if !strings.Contains(message, want) {
					t.Fatalf("Parse() error = %q, want %q", message, want)
				}
			}
			if test.name == "sorted bad keys" && strings.Index(message, `"a"`) > strings.Index(message, `"z"`) {
				t.Fatalf("offending keys are not sorted: %q", message)
			}
		})
	}
}

func TestParseEmptyAndCommentsOnly(t *testing.T) {
	for _, document := range []string{"", "# a comment\n\n  # another\n"} {
		parsed, err := Parse([]byte(document))
		if err != nil || parsed == nil || len(parsed) != 0 {
			t.Fatalf("Parse(%q) = %#v, %v", document, parsed, err)
		}
	}
}

func TestParseCoercedKeyDocumented(t *testing.T) {
	parsed, err := Parse([]byte("no: x\n"))
	if err != nil || !reflect.DeepEqual(parsed, map[string]string{"false": "x"}) {
		t.Fatalf("Parse() = %#v, %v", parsed, err)
	}
}

func TestFromResource(t *testing.T) {
	secret := k8s.NewSecret(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "secret", Namespace: "default"},
		Data: map[string][]byte{
			"z":      []byte("text"),
			"a":      []byte("also text"),
			"binary": {0, 1, 2},
		},
	})
	values, binary, err := FromResource(secret)
	if err != nil || !reflect.DeepEqual(values, map[string]string{"a": "also text", "z": "text"}) || !reflect.DeepEqual(binary, []BinaryKey{{Name: "binary", Size: 3}}) {
		t.Fatalf("FromResource(secret) = %#v, %#v, %v", values, binary, err)
	}
	configMap := k8s.NewConfigMap(&corev1.ConfigMap{BinaryData: map[string][]byte{"text-in-binary-data": []byte("text")}})
	values, binary, err = FromResource(configMap)
	if err != nil || !reflect.DeepEqual(values, map[string]string{"text-in-binary-data": "text"}) || len(binary) != 0 {
		t.Fatalf("FromResource(configmap) = %#v, %#v, %v", values, binary, err)
	}
}
