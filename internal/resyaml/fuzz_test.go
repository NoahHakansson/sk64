package resyaml

import (
	"maps"
	"regexp"
	"testing"
	"unicode/utf8"
)

var validKey = regexp.MustCompile(`^[-._a-zA-Z0-9]+$`)

func FuzzParse(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("# resource: secret/s (namespace: n)\nk: value\n"),
		[]byte("a: |-\n  x\nb: |\n  y\nc: |+\n  z\n\n"),
		[]byte(`k: "true"`),
		[]byte("k: |-\n  [{\"id\":\"a\"}]\n"),
		[]byte("k: a\nk: b\n"),
		[]byte("- list\n"),
		{0xff, 0x00},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		parsed, err := Parse(data)
		if err != nil {
			return
		}
		serialized, err := SerializeValues(parsed)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Parse(serialized)
		if err != nil || !maps.Equal(again, parsed) {
			t.Fatalf("second round trip = %q, %v; want %q", again, err, parsed)
		}
	})
}

func FuzzRoundTrip(f *testing.F) {
	for _, seed := range [][2]string{{"true", "no"}, {"crlf", "a\r\nb"}, {"trailing", "a\n\n"}, {"nul", "\x00"}, {"unicode", "世界🙂"}, {"line-separator", "a\"b\u2028c"}, {"paragraph-separator", "a\"b\u2029c"}, {"json", `[{"id":"a"}]`}, {"quote", `a"b`}, {"backslash", `a\b`}, {"quote-space", `a"b `}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, key, value string) {
		if !utf8.ValidString(key) || !utf8.ValidString(value) || !validKey.MatchString(key) {
			t.Skip()
		}
		input := map[string]string{key: value}
		serialized, err := SerializeValues(input)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(serialized)
		if err != nil || !maps.Equal(parsed, input) {
			t.Fatalf("round trip = %q, %v; want %q", parsed, err, input)
		}
	})
}
