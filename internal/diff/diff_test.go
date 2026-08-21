package diff

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRenderText(t *testing.T) {
	before := []byte("first\nold\nlast\n")
	after := []byte("first\nnew\nlast\n")
	got := Render("before", "after", before, after, "…")
	for _, want := range []string{"before", "after", "-old", "+new"} {
		if !strings.Contains(got, want) {
			t.Fatalf("Render() missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, base64.StdEncoding.EncodeToString(before)) {
		t.Fatalf("Render() contains base64 input: %s", got)
	}
}

func TestRenderBinary(t *testing.T) {
	before := []byte{0, 1, 2, 3}
	after := []byte{0, 4, 5, 6, 7}
	for _, test := range []struct {
		name     string
		ellipsis string
	}{
		{name: "unicode", ellipsis: "…"},
		{name: "ASCII", ellipsis: "..."},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Render("before", "after", before, after, test.ellipsis)
			want := "binary value changed\nbefore: 4 B  sha256 054edec1d021" + test.ellipsis + "   after: 5 B  sha256 5ab0680027d2" + test.ellipsis
			if got != want {
				t.Fatalf("Render() = %q, want %q", got, want)
			}
			if strings.Contains(got, string(after)) {
				t.Fatalf("Render() contains raw bytes: %q", got)
			}
		})
	}
}

func TestRenderEqual(t *testing.T) {
	if got := Render("before", "after", []byte("same"), []byte("same"), "…"); got != "(no changes)" {
		t.Fatalf("Render(equal) = %q", got)
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		size int
		want string
	}{
		{size: 0, want: "0 B"},
		{size: 1023, want: "1023 B"},
		{size: 1024, want: "1.0 KiB"},
		{size: 1536, want: "1.5 KiB"},
		{size: 1024 * 1024, want: "1.0 MiB"},
	}
	for _, test := range tests {
		if got := HumanSize(test.size); got != test.want {
			t.Errorf("HumanSize(%d) = %q, want %q", test.size, got, test.want)
		}
	}
}
