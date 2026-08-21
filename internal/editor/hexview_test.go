package editor

import (
	"reflect"
	"testing"
)

func TestHexDump(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want []string
	}{
		{name: "empty"},
		{
			name: "five bytes",
			data: []byte("Hello"),
			want: []string{"00000000  48 65 6c 6c 6f                                    |Hello|"},
		},
		{
			name: "sixteen bytes",
			data: []byte("0123456789abcdef"),
			want: []string{"00000000  30 31 32 33 34 35 36 37  38 39 61 62 63 64 65 66  |0123456789abcdef|"},
		},
		{
			name: "seventeen bytes",
			data: []byte("0123456789abcdefZ"),
			want: []string{
				"00000000  30 31 32 33 34 35 36 37  38 39 61 62 63 64 65 66  |0123456789abcdef|",
				"00000010  5a                                                |Z|",
			},
		},
		{
			name: "non-printable",
			data: []byte{0, 0x1f, 0x20, 0x7e, 0x7f},
			want: []string{"00000000  00 1f 20 7e 7f                                    |.. ~.|"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := HexDump(test.data); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("HexDump() = %#v, want %#v", got, test.want)
			}
		})
	}
}
