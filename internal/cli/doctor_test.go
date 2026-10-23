package cli

import (
	"os/exec"
	"strings"
	"testing"
)

var errNotFound = exec.ErrNotFound

func TestDoctorCmd(t *testing.T) {
	h := testHost(t)
	h.Run = mapRunner{"/usr/bin/ssh -V": "OpenSSH_9.6p1\n"}
	p := writeConfig(t, "tunnels:\n  db: { host: ap, forwards: [\"1:db:5432\"] }\n")
	code, out, errOut := runWith(t, h, "doctor", "--config", p)
	if code != 0 || !strings.Contains(out, "✓ ssh") || !strings.Contains(out, "- socat（relay モード）: 対象外") {
		t.Fatalf("code=%d\n%s\n%s", code, out, errOut)
	}

	h.LookPath = func(string) (string, error) { return "", errNotFound }
	code, out, _ = runWith(t, h, "doctor", "--config", p, "db")
	if code != 1 || !strings.Contains(out, "✗ ssh") {
		t.Fatalf("✗ があれば終了コード 1: code=%d\n%s", code, out)
	}
}
