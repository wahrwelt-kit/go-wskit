package wskit

import (
	"bytes"
	"strings"
	"testing"
)

func FuzzWriteSSEData(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("hello"),
		[]byte("one\ntwo"),
		[]byte("one\r\ntwo\rthree"),
		{},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, msg []byte) {
		var buf bytes.Buffer
		if err := writeSSEData(&buf, msg); err != nil {
			t.Fatalf("writeSSEData: %v", err)
		}
		out := buf.String()
		if !strings.HasSuffix(out, "\n\n") {
			t.Fatalf("SSE output does not end event: %q", out)
		}
		if strings.Contains(out, "\r") {
			t.Fatalf("SSE output contains carriage return: %q", out)
		}
		for line := range strings.SplitSeq(strings.TrimSuffix(out, "\n\n"), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				t.Fatalf("SSE line = %q, want data prefix", line)
			}
		}
	})
}
