package runs

import (
	"bufio"
	"crypto/sha256"
	"io"
	"sort"
	"strings"
)

type redactor struct {
	hashes  map[int]map[[32]byte]bool
	lengths []int
	max     int
}

func newRedactor(secrets map[string]string) redactor {
	r := redactor{hashes: map[int]map[[32]byte]bool{}}
	for _, value := range secrets {
		size := len(value)
		if size == 0 {
			continue
		}
		if r.hashes[size] == nil {
			r.hashes[size] = map[[32]byte]bool{}
			r.lengths = append(r.lengths, size)
		}
		r.hashes[size][sha256.Sum256([]byte(value))] = true
		if size > r.max {
			r.max = size
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(r.lengths)))
	return r
}

func (r redactor) redact(message string) string {
	var output strings.Builder
	_ = r.copy(&output, strings.NewReader(message))
	return output.String()
}

func (r redactor) copy(dst io.Writer, src io.Reader) error {
	if r.max == 0 {
		_, err := io.Copy(dst, src)
		return err
	}
	writer := bufio.NewWriter(dst)
	buffer := make([]byte, 0, 32768+r.max)
	chunk := make([]byte, 32768)
	for {
		size, err := src.Read(chunk)
		buffer = append(buffer, chunk[:size]...)
		for len(buffer) > 0 && (len(buffer) >= r.max || err != nil) {
			matched := 0
			for _, size := range r.lengths {
				if size <= len(buffer) && r.hashes[size][sha256.Sum256(buffer[:size])] {
					matched = size
					break
				}
			}
			if matched > 0 {
				if _, err := writer.WriteString("[REDACTED]"); err != nil {
					return err
				}
				buffer = buffer[matched:]
			} else {
				if err := writer.WriteByte(buffer[0]); err != nil {
					return err
				}
				buffer = buffer[1:]
			}
		}
		if err := writer.Flush(); err != nil {
			return err
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
