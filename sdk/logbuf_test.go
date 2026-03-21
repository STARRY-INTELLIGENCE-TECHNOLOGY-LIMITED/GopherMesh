package mesh

import (
	"reflect"
	"testing"
)

func TestLogBufferWriteKeepsLatestCompleteLines(t *testing.T) {
	t.Parallel()

	buf := NewLogBuffer(2)

	if _, err := buf.Write([]byte("line-1\nline")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := buf.Write([]byte("-2\nline-3\nline-4")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	got := buf.Lines()
	want := []string{"line-2", "line-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Lines() = %#v, want %#v", got, want)
	}
}
