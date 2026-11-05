package runs

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type byteReader struct{ data string }

func (r *byteReader) Read(p []byte) (int, error) {
	if r.data == "" {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestRedactionAcrossChunksAndLines(t *testing.T) {
	secrets := map[string]string{"TOKEN": "sensitive\nvalue", "OTHER": "sensitive", "EMPTY": ""}
	r := newRedactor(secrets)
	clear(secrets)
	var out bytes.Buffer
	if err := r.copy(&out, &byteReader{data: "before sensitive\nvalue and sensitive after"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "before [REDACTED] and [REDACTED] after" {
		t.Fatalf("redaction: %q", out.String())
	}
	out.Reset()
	if err := newRedactor(nil).copy(&out, strings.NewReader("plain output")); err != nil || out.String() != "plain output" {
		t.Fatalf("plain output: %q %v", out.String(), err)
	}
}

func TestRuntimeErrorRedaction(t *testing.T) {
	sanitizer := newRedactor(map[string]string{"TOKEN": "private-runtime-value"})
	message := sanitizer.redact("create busybox: denied for private-runtime-value")
	if message != "create busybox: denied for [REDACTED]" {
		t.Fatalf("runtime error: %q", message)
	}
}
