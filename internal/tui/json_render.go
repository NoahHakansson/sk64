package tui

import (
	"bytes"
	"encoding/json"
	"strings"
)

const (
	maxPrettyJSONInputBytes = 128 << 10
	maxPrettyJSONBytes      = 1 << 20
	maxPrettyJSONDepth      = 64
)

func prettyJSON(value []byte) (string, bool) {
	trimmed := bytes.TrimSpace(value)
	// Bound the work before doing it because the viewer runs synchronously on the event loop.
	if len(trimmed) == 0 || len(trimmed) > maxPrettyJSONInputBytes ||
		trimmed[0] != '{' && trimmed[0] != '[' || exceedsJSONDepth(trimmed) || !json.Valid(trimmed) {
		return "", false
	}
	var out bytes.Buffer
	if err := json.Indent(&out, trimmed, "", "  "); err != nil {
		return "", false
	}
	if out.Len() > maxPrettyJSONBytes {
		return "", false
	}
	return out.String(), true
}

func exceedsJSONDepth(value []byte) bool {
	depth := 0
	inString := false
	escaped := false
	for _, current := range value {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch current {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maxPrettyJSONDepth {
				return true
			}
		case '}', ']':
			depth--
		}
	}
	return false
}

// colorizeJSON styles tokens in pretty, which must be well-formed JSON as
// produced by prettyJSON. It never adds or removes non-styling bytes.
func colorizeJSON(pretty string, st *styles) string {
	var out strings.Builder
	for index := 0; index < len(pretty); {
		start := index
		switch {
		case pretty[index] == '"':
			index++
			for index < len(pretty) {
				if pretty[index] == '\\' {
					index += min(2, len(pretty)-index)
					continue
				}
				index++
				if pretty[index-1] == '"' {
					break
				}
			}
			next := index
			for next < len(pretty) && (pretty[next] == ' ' || pretty[next] == '\t' || pretty[next] == '\n' || pretty[next] == '\r') {
				next++
			}
			style := st.jsonString
			if next < len(pretty) && pretty[next] == ':' {
				style = st.jsonKey
			}
			out.WriteString(style.Render(pretty[start:index]))
		case pretty[index] == '-' || pretty[index] >= '0' && pretty[index] <= '9':
			for index < len(pretty) && (pretty[index] == '-' || pretty[index] == '+' || pretty[index] == '.' || pretty[index] == 'e' || pretty[index] == 'E' || pretty[index] >= '0' && pretty[index] <= '9') {
				index++
			}
			out.WriteString(st.jsonNumber.Render(pretty[start:index]))
		case strings.HasPrefix(pretty[index:], "true"):
			index += len("true")
			out.WriteString(st.jsonLiteral.Render(pretty[start:index]))
		case strings.HasPrefix(pretty[index:], "false"):
			index += len("false")
			out.WriteString(st.jsonLiteral.Render(pretty[start:index]))
		case strings.HasPrefix(pretty[index:], "null"):
			index += len("null")
			out.WriteString(st.jsonLiteral.Render(pretty[start:index]))
		case isJSONSpace(pretty[index]):
			// Indentation is written raw. Styling it would wrap every line's
			// leading run in its own escape sequence, which costs more than the
			// punctuation styling saves and shows nothing: st.dim is foreground
			// only, so a styled space is indistinguishable from a plain one.
			for index < len(pretty) && isJSONSpace(pretty[index]) {
				index++
			}
			out.WriteString(pretty[start:index])
		default:
			index++
			for index < len(pretty) && !isJSONSpace(pretty[index]) && !startsStyledJSONToken(pretty, index) {
				index++
			}
			out.WriteString(st.dim.Render(pretty[start:index]))
		}
	}
	return out.String()
}

func isJSONSpace(current byte) bool {
	return current == ' ' || current == '\t' || current == '\n' || current == '\r'
}

func startsStyledJSONToken(value string, index int) bool {
	current := value[index]
	return current == '"' || current == '-' || current >= '0' && current <= '9' ||
		strings.HasPrefix(value[index:], "true") || strings.HasPrefix(value[index:], "false") ||
		strings.HasPrefix(value[index:], "null")
}
