package config

import (
	"strings"
	"testing"
)

const rootYAML = `
defaults:
  ssh: auto
  keepalive: 30s
  reconnect: true
wsl:
  mode: auto
hosts:
  bastion:
    host: bastion.example.com
    user: ec2-user
  inner:
    host: 10.0.1.10
    user: admin
    via: bastion
tunnels:
  prod-db:
    via: inner
    forwards:
      - 15432:db.internal:5432
      - 16379:redis.internal:6379
  web:
    host: app1
    forwards:
      - { local: "127.0.0.1:8080", remote: "localhost:80" }
groups:
  prod: [prod-db, web]
`

func TestLoadRoot(t *testing.T) {
	cfg := mustLoad(t, rootYAML)
	root := cfg.Envs[""]
	if root == nil {
		t.Fatal("ルート環境が無い")
	}
	db := root.Tunnels["prod-db"]
	if db == nil || db.Type != "ssh" || db.Via != "inner" || len(db.Forwards) != 2 {
		t.Fatalf("prod-db = %+v", db)
	}
	if db.ID() != "prod-db" {
		t.Errorf("ID = %q", db.ID())
	}
	if f := root.Tunnels["web"].Forwards[0]; f.LocalPort != 8080 || f.RemoteHost != "localhost" {
		t.Errorf("web の forward = %+v", f)
	}
	if h := root.Hosts["inner"]; h.User != "admin" || h.Via != "bastion" {
		t.Errorf("inner = %+v", h)
	}
	if cfg.Defaults.Keepalive != "30s" || cfg.WSLMode != "auto" {
		t.Errorf("defaults = %+v, wsl = %q", cfg.Defaults, cfg.WSLMode)
	}
	if g := cfg.Groups["prod"]; g == nil || strings.Join(g.Members, ",") != "prod-db,web" {
		t.Errorf("groups = %+v", cfg.Groups)
	}
	if len(cfg.Files) != 1 {
		t.Errorf("Files = %v", cfg.Files)
	}
}

func TestLoadEnvironments(t *testing.T) {
	cfg := mustLoad(t, `
environments:
  prod:
    protected: true
    ssh_config: wsl
    aws:
      profile: prod
    hosts:
      bastion: { host: prod-bastion.example.com, user: ec2-user }
    tunnels:
      db:
        via: bastion
        forwards:
          - 25432:prod-db.internal:5432
`)
	prod := cfg.Envs["prod"]
	if prod == nil || !prod.Protected || prod.Settings.SSHConfig != "wsl" || prod.Settings.Profile != "prod" {
		t.Fatalf("prod = %+v", prod)
	}
	if db := prod.Tunnels["db"]; db.ID() != "prod.db" || db.Env != "prod" {
		t.Errorf("db = %+v", db)
	}
	if prod.Hosts["bastion"].ID() != "prod.bastion" {
		t.Errorf("host ID = %q", prod.Hosts["bastion"].ID())
	}
}

func TestLoadAWSForms(t *testing.T) {
	cfg := mustLoad(t, `
defaults:
  aws: aws.exe
environments:
  stg:
    tunnels:
      db:
        type: ecs
        profile: stg
        region: ap-northeast-1
        cluster: c
        service: s
        container: app
        forwards: ["35432:db:5432"]
`)
	if cfg.Defaults.AWS != "aws.exe" {
		t.Errorf("defaults.aws = %q", cfg.Defaults.AWS)
	}
	db := cfg.Envs["stg"].Tunnels["db"]
	if db.Type != "ecs" || db.Settings.Profile != "stg" || db.Settings.Region != "ap-northeast-1" {
		t.Errorf("db = %+v", db)
	}

	_, ps := loadString(t, `
environments:
  stg:
    profile: a
    aws: { profile: b }
`, "linux")
	wantProblem(t, ps, "profile と aws.profile は同時に書けません")
}

func TestLoadProblems(t *testing.T) {
	_, ps := loadString(t, "tunnels:\n  t1:\n    hots: x\n", "linux")
	if len(ps) == 0 || !strings.Contains(ps[0].String(), "hashigo.yaml:3:") {
		t.Fatalf("未知のキーは3行目を指す: %v", ps)
	}

	_, ps = loadString(t, "tunnels:\n  t1:\n    host: x\n    forwards:\n      - 15432:db\n      - { local: 1, remote: db, extra: 1 }\n", "linux")
	wantProblem(t, ps, "ローカルポート:宛先ホスト:宛先ポート")
	wantProblem(t, ps, `未知のキー "extra"`)
	if !strings.Contains(ps[0].String(), "hashigo.yaml:5:") {
		t.Errorf("forwards の問題は該当行を指す: %s", ps[0].String())
	}

	_, ps = loadString(t, "tunnels:\n  t1:\n    host: x\n", "linux")
	wantProblem(t, ps, "forwards がありません")

	_, ps = loadString(t, "tunnels:\n  t1:\n    host: x\n    forwards: [~]\n", "linux")
	wantProblem(t, ps, "forwards の要素が空です")
	_, ps = loadString(t, "tunnels:\n  t1:\n    host: x\n    forwards: [\"1:a:1\", ~]\n", "linux")
	wantProblem(t, ps, "forwards の要素が空です")

	_, ps = loadString(t, "defaults:\n  profile: x\n", "linux")
	wantProblem(t, ps, "defaults には profile / region を書けません")
}
