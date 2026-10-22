//go:build e2e

// Package e2e は Docker で多段の sshd を立て、hashigo を本物の ssh で動かす。
package e2e

import (
	"os"
	"os/exec"
	"testing"
)

func run(t *testing.T, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
}

func TestE2E(t *testing.T) {
	run(t, []string{"CGO_ENABLED=0", "GOOS=linux"}, "go", "build", "-o", "bin/hashigo", "../../cmd/hashigo")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "down", "-v", "--remove-orphans")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_ = cmd.Run()
	})
	run(t, nil, "docker", "compose", "build")
	run(t, nil, "docker", "compose", "run", "--rm", "client")
}
