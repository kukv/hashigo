package config

import (
	"errors"
	"testing"
)

func loadFiles(t *testing.T, files map[string]string, main string) (*Config, Problems) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, dir, name, content)
	}
	cfg, err := Load(dir+"/"+main, "linux")
	var ps Problems
	if err != nil && !errors.As(err, &ps) {
		t.Fatal(err)
	}
	return cfg, ps
}

func TestInclude(t *testing.T) {
	cfg, ps := loadFiles(t, map[string]string{
		"main.yaml": "include: [dev.yaml]\ngroups:\n  all: [dev]\n",
		"dev.yaml":  "environments:\n  dev:\n    tunnels:\n      db: { host: x, forwards: [\"15432:db:5432\"] }\n",
	}, "main.yaml")
	if ps != nil {
		t.Fatal(ps.Error())
	}
	if cfg.Envs["dev"].Tunnels["db"] == nil || len(cfg.Files) != 2 {
		t.Fatalf("include が読まれていない: %+v", cfg.Files)
	}
}

func TestIncludeDuplicate(t *testing.T) {
	_, ps := loadFiles(t, map[string]string{
		"main.yaml": "include: [a.yaml]\nenvironments:\n  dev: {}\n",
		"a.yaml":    "environments:\n  dev: {}\n",
	}, "main.yaml")
	wantProblem(t, ps, `環境 "dev" は`)
}

func TestIncludeLoop(t *testing.T) {
	_, ps := loadFiles(t, map[string]string{
		"main.yaml": "include: [a.yaml]\n",
		"a.yaml":    "include: [main.yaml]\n",
	}, "main.yaml")
	wantProblem(t, ps, "include がループしています")
}

func TestIncludeMissing(t *testing.T) {
	_, ps := loadFiles(t, map[string]string{"main.yaml": "include: [nope.yaml]\n"}, "main.yaml")
	wantProblem(t, ps, "設定ファイルを読めません")
}
