// Package resyaml serializes a resource's string-valued keys as an editable
// YAML document and parses the edited document back, byte-exactly.
package resyaml

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	diffpkg "github.com/NoahHakansson/sk64/internal/diff"
	"github.com/NoahHakansson/sk64/internal/k8s"
	"github.com/NoahHakansson/sk64/internal/natsort"
	"sigs.k8s.io/yaml"
)

// Header describes the resource identity rendered as comment lines.
type Header struct {
	Kind            string
	Name            string
	Namespace       string
	ResourceVersion string
}

// BinaryKey names a key omitted from the document because its value is binary.
type BinaryKey struct {
	Name string
	Size int
}

// go-yaml counts the 1024-character implicit-key window in runes. Since byte
// length is never smaller than rune count, this conservative check cannot emit
// a key the parser rejects.
const maxImplicitKeyLength = 1024

// FromResource splits a resource into editable string values and omitted binary keys.
func FromResource(res k8s.Resource) (map[string]string, []BinaryKey, error) {
	values := make(map[string]string)
	var binary []BinaryKey
	for _, key := range res.Keys() {
		value, err := res.Get(key)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s %q key %q: %w", strings.ToLower(res.Kind()), res.Name(), key, err)
		}
		if res.IsBinary(key) {
			binary = append(binary, BinaryKey{Name: key, Size: len(value)})
			continue
		}
		values[key] = string(value)
	}
	slices.SortFunc(binary, func(a, b BinaryKey) int { return natsort.Compare(a.Name, b.Name) })
	return values, binary, nil
}

// Serialize renders a complete editable resource document.
func Serialize(header Header, values map[string]string, binary []BinaryKey, separator string) ([]byte, error) {
	body, err := SerializeValues(values)
	if err != nil {
		return nil, fmt.Errorf("serialize resource values: %w", err)
	}
	var document strings.Builder
	fmt.Fprintf(&document, "# resource: %s/%s (namespace: %s)\n", strings.ToLower(header.Kind), header.Name, header.Namespace)
	if header.ResourceVersion != "" {
		fmt.Fprintf(&document, "# resourceVersion: %s\n", header.ResourceVersion)
	}
	if len(binary) != 0 {
		fmt.Fprintf(&document, "# Binary keys are omitted (use x/i export/import)%slisted below.\n", separator)
	}
	document.WriteString("# Deleting a mapping deletes the key on save; adding one adds a key.\n")
	document.Write(body)
	for _, key := range binary {
		fmt.Fprintf(&document, "# omitted (binary): %s [%s]\n", key.Name, diffpkg.HumanSize(key.Size))
	}
	return []byte(document.String()), nil
}

// SerializeValues renders only the sorted YAML mapping body.
func SerializeValues(values map[string]string) ([]byte, error) {
	var body strings.Builder
	for _, key := range slices.SortedFunc(maps.Keys(values), natsort.Compare) {
		renderedKey := renderKey(key)
		if len(renderedKey) > maxImplicitKeyLength {
			body.WriteString("? ")
			body.WriteString(renderedKey)
			body.WriteString("\n: ")
		} else {
			body.WriteString(renderedKey)
			body.WriteString(": ")
		}
		body.WriteString(renderValue(values[key]))
		body.WriteByte('\n')
	}
	result := []byte(body.String())
	parsed, err := Parse(result)
	if err != nil {
		return nil, fmt.Errorf("verify serialized values: %w", err)
	}
	if !maps.Equal(parsed, values) {
		return nil, fmt.Errorf("verify serialized values: round trip mismatch: got %v", parsed)
	}
	return result, nil
}

// Parse converts an edited YAML document back to a string mapping.
func Parse(data []byte) (map[string]string, error) {
	if commentsOnly(data) {
		return map[string]string{}, nil
	}
	var decoded any
	if err := yaml.UnmarshalStrict(data, &decoded); err != nil {
		return nil, fmt.Errorf("parse YAML document: %w", err)
	}
	mapping, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("parse YAML document: top level must be a mapping, got %s", friendlyType(decoded))
	}
	values := make(map[string]string, len(mapping))
	invalid := make([]string, 0)
	for key, value := range mapping {
		text, ok := value.(string)
		if ok {
			values[key] = text
			continue
		}
		if value == nil {
			invalid = append(invalid, fmt.Sprintf("%q is null; use \"\" for an empty value", key))
			continue
		}
		invalid = append(invalid, fmt.Sprintf("%q has non-string value of type %s", key, friendlyType(value)))
	}
	if len(invalid) != 0 {
		slices.SortFunc(invalid, natsort.Compare)
		return nil, errors.New("parse YAML document: " + strings.Join(invalid, "; "))
	}
	return values, nil
}

func commentsOnly(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			return false
		}
	}
	return true
}

func friendlyType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case map[string]any:
		return "mapping"
	case []any:
		return "list"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func renderKey(key string) string {
	if plainRoundTrips(key) {
		return key
	}
	return quoteString(key)
}

func renderValue(value string) string {
	if plainCandidate(value) && plainRoundTrips(value) {
		return value
	}
	if blockCandidate(value) {
		block := renderBlock(value)
		parsed, err := Parse([]byte("k: " + block + "\n"))
		if err == nil && parsed["k"] == value {
			return block
		}
	}
	return quoteString(value)
}

func plainCandidate(value string) bool {
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func plainRoundTrips(value string) bool {
	if !plainCandidate(value) {
		return false
	}
	var decoded any
	return yaml.Unmarshal([]byte(value), &decoded) == nil && decoded == value
}

func blockCandidate(value string) bool {
	if strings.Contains(value, "\r") {
		return false
	}
	if !strings.Contains(value, "\n") && !strings.ContainsAny(value, "\"\\") {
		return false
	}
	lines := strings.Split(value, "\n")
	if lines[0] == "" || strings.HasPrefix(lines[0], " ") || strings.HasPrefix(lines[0], "\t") {
		return false
	}
	for _, line := range lines {
		if strings.HasSuffix(line, " ") || strings.HasSuffix(line, "\t") {
			return false
		}
		for _, r := range line {
			if r == '\u2028' || r == '\u2029' || r < 0x20 || r == 0x7f {
				return false
			}
		}
	}
	return true
}

func renderBlock(value string) string {
	trailing := len(value) - len(strings.TrimRight(value, "\n"))
	indicator := "|-"
	if trailing == 1 {
		indicator = "|"
	} else if trailing >= 2 {
		indicator = "|+"
	}
	content := strings.TrimSuffix(value, strings.Repeat("\n", trailing))
	lines := strings.Split(content, "\n")
	var block strings.Builder
	block.WriteString(indicator)
	block.WriteByte('\n')
	for _, line := range lines {
		block.WriteString("  ")
		block.WriteString(line)
		block.WriteByte('\n')
	}
	for range max(0, trailing-1) {
		block.WriteByte('\n')
	}
	return strings.TrimSuffix(block.String(), "\n")
}

func quoteString(value string) string {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return escapeYAMLNonPrintable(strings.TrimSuffix(buf.String(), "\n"))
}

// escapeYAMLNonPrintable escapes the runes encoding/json leaves literal but YAML
// rejects inside a double-quoted scalar. json only escapes below U+0020, so DEL,
// the C1 block and the two non-characters would otherwise reach the document as
// raw bytes and fail to parse. This is the floor of the style ladder, so it has
// to be total: every other style is accepted only after a verifying reparse.
func escapeYAMLNonPrintable(quoted string) string {
	if !strings.ContainsFunc(quoted, yamlRejectsLiteral) {
		return quoted
	}
	var escaped strings.Builder
	escaped.Grow(len(quoted))
	for _, r := range quoted {
		if yamlRejectsLiteral(r) {
			fmt.Fprintf(&escaped, `\u%04x`, r)
			continue
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}

func yamlRejectsLiteral(r rune) bool {
	return r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0xfffe || r == 0xffff
}
