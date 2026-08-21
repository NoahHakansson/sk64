package natsort

import (
	"slices"
	"testing"
)

func TestCompare(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{name: "equal", a: "URLS_1", b: "URLS_1", want: 0},
		{name: "plain byte order", a: "alpha", b: "beta", want: -1},
		{name: "numeric beats byte order", a: "URLS_2", b: "URLS_10", want: -1},
		{name: "multi digit runs", a: "key_10_2", b: "key_10_10", want: -1},
		{name: "digit versus letter keeps byte order", a: "a1", b: "aa", want: -1},
		{name: "shorter prefix first", a: "URLS_1", b: "URLS_1_extra", want: -1},
		{name: "pure numbers", a: "9", b: "10", want: -1},
		{name: "leading zeros equal value ties bytewise", a: "a01", b: "a1", want: -1},
		{name: "leading zeros differing value", a: "a010", b: "a9", want: 1},
		{name: "empty before anything", a: "", b: "0", want: -1},
		{name: "digits before empty suffix", a: "a", b: "a0", want: -1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Compare(test.a, test.b); got != test.want {
				t.Fatalf("Compare(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
			}
			if got, want := Compare(test.b, test.a), -test.want; got != want {
				t.Fatalf("Compare(%q, %q) = %d, want %d", test.b, test.a, got, want)
			}
		})
	}
}

func TestCompareSortsNumberedKeysNaturally(t *testing.T) {
	keys := []string{
		"SELFSERVICE_ALLOWED_RETURN_URLS_10",
		"SELFSERVICE_ALLOWED_RETURN_URLS_1",
		"SELFSERVICE_ALLOWED_RETURN_URLS_0",
		"SELFSERVICE_ALLOWED_RETURN_URLS_2",
		"SELFSERVICE_ALLOWED_RETURN_URLS_14",
		"SELFSERVICE_DEFAULT_BROWSER_RETURN_URL",
	}
	slices.SortFunc(keys, Compare)
	want := []string{
		"SELFSERVICE_ALLOWED_RETURN_URLS_0",
		"SELFSERVICE_ALLOWED_RETURN_URLS_1",
		"SELFSERVICE_ALLOWED_RETURN_URLS_2",
		"SELFSERVICE_ALLOWED_RETURN_URLS_10",
		"SELFSERVICE_ALLOWED_RETURN_URLS_14",
		"SELFSERVICE_DEFAULT_BROWSER_RETURN_URL",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("sorted keys = %v, want %v", keys, want)
	}
}
