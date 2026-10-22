package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// loadString は YAML 文字列を一時ファイルに書いて Load する。
func loadString(t *testing.T, yaml, goos string) (*Config, Problems) {
	t.Helper()
	p := writeFile(t, t.TempDir(), "hashigo.yaml", yaml)
	cfg, err := Load(p, goos)
	if err == nil {
		return cfg, nil
	}
	var ps Problems
	if !errors.As(err, &ps) {
		t.Fatalf("Load のエラーが Problems ではない: %v", err)
	}
	return nil, ps
}

func mustLoad(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, ps := loadString(t, yaml, "linux")
	if ps != nil {
		t.Fatalf("想定外の問題:\n%s", ps.Error())
	}
	return cfg
}

func wantProblem(t *testing.T, ps Problems, substr string) {
	t.Helper()
	for _, p := range ps {
		if strings.Contains(p.Msg, substr) {
			return
		}
	}
	t.Errorf("%q を含む問題がない。実際:\n%s", substr, ps.Error())
}
