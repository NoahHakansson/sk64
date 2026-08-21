package project

import "testing"

func TestIgnoreMatcher(t *testing.T) {
	matcher := newIgnoreMatcher()
	matcher.addFile("", []byte(`
# generated files
cache
*.log
temp?.yaml
build/
/top.yaml
docs/gen
**/vendor
logs/**
a/**/b
!keep.log
`))
	matcher.addFile("services/api", []byte("local.yaml\n"))
	tests := []struct {
		path    string
		dir     bool
		ignored bool
	}{
		{path: "cache", ignored: true},
		{path: "nested/cache", dir: true, ignored: true},
		{path: "error.log", ignored: true},
		{path: "nested/error.log", ignored: true},
		{path: "keep.log", ignored: false},
		{path: "temp1.yaml", ignored: true},
		{path: "build", dir: true, ignored: true},
		{path: "build", ignored: false},
		{path: "top.yaml", ignored: true},
		{path: "nested/top.yaml", ignored: false},
		{path: "docs/gen", dir: true, ignored: true},
		{path: "x/vendor", dir: true, ignored: true},
		{path: "logs/deep/file", ignored: true},
		{path: `logs\x.yaml`, ignored: false},
		{path: "a/b", ignored: true},
		{path: "a/x/y/b", ignored: true},
		{path: "services/api/local.yaml", ignored: true},
		{path: "services/web/local.yaml", ignored: false},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := matcher.ignored(test.path, test.dir); got != test.ignored {
				t.Fatalf("ignored(%q, %t) = %t, want %t", test.path, test.dir, got, test.ignored)
			}
		})
	}
}
