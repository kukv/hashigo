package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	code := Run(context.Background(), Deps{Stdout: &out, Stderr: io.Discard}, []string{"version"})
	if code != 0 {
		t.Fatalf("終了コード = %d, want 0", code)
	}
	got := strings.TrimSpace(out.String())
	if got == "" || strings.Contains(got, "\n") {
		t.Fatalf("version の出力 = %q, want 1行", got)
	}
}
