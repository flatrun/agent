package docker

import (
	"testing"
)

func TestStripDockerStreamHeaders(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "empty input",
			input: []byte{},
			want:  "",
		},
		{
			name:  "plain text without headers",
			input: []byte("hello world"),
			want:  "hello world",
		},
		{
			name: "single stdout frame",
			input: func() []byte {
				payload := []byte("hello")
				header := []byte{1, 0, 0, 0, 0, 0, 0, byte(len(payload))}
				return append(header, payload...)
			}(),
			want: "hello",
		},
		{
			name: "single stderr frame",
			input: func() []byte {
				payload := []byte("error msg")
				header := []byte{2, 0, 0, 0, 0, 0, 0, byte(len(payload))}
				return append(header, payload...)
			}(),
			want: "error msg",
		},
		{
			name: "multiple frames",
			input: func() []byte {
				p1 := []byte("hello ")
				h1 := []byte{1, 0, 0, 0, 0, 0, 0, byte(len(p1))}
				p2 := []byte("world")
				h2 := []byte{1, 0, 0, 0, 0, 0, 0, byte(len(p2))}
				result := append(h1, p1...)
				return append(result, append(h2, p2...)...)
			}(),
			want: "hello world",
		},
		{
			name:  "data shorter than header size",
			input: []byte{1, 2, 3},
			want:  "\x01\x02\x03",
		},
		{
			name: "frame with size larger than remaining data falls back to raw",
			input: func() []byte {
				header := []byte{1, 0, 0, 0, 0, 0, 0, 50}
				return append(header, []byte("short")...)
			}(),
			want: string(append([]byte{1, 0, 0, 0, 0, 0, 0, 50}, []byte("short")...)),
		},
		{
			name: "large payload with multi-byte size",
			input: func() []byte {
				payload := make([]byte, 300)
				for i := range payload {
					payload[i] = 'A'
				}
				// size = 300 = 0x12C -> bytes: 0, 0, 1, 44
				header := []byte{1, 0, 0, 0, 0, 0, 1, 44}
				return append(header, payload...)
			}(),
			want: string(func() []byte {
				b := make([]byte, 300)
				for i := range b {
					b[i] = 'A'
				}
				return b
			}()),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripDockerStreamHeaders(tt.input)
			if got != tt.want {
				t.Errorf("stripDockerStreamHeaders() = %q, want %q", got, tt.want)
			}
		})
	}
}
