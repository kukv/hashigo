package config

import (
	"strings"
	"testing"
	"time"
)

const envYAML = `
defaults:
  keepalive: 20s
hosts:
  shared: { host: shared.example.com, user: ops }
environments:
  dev:
    keepalive: 40s
    hosts:
      bastion: { host: dev-bastion, user: ec2-user }
      mid:     { host: 10.0.0.2, via: bastion }
      ap:      { host: 10.0.1.10, user: admin, via: mid }
    tunnels:
      db:
        via: ap
        forwards: ["15432:dev-db:5432"]
      cache:
        via: shared
        forwards: ["16379:redis:6379"]
  prod:
    protected: true
    tunnels:
      db:
        host: prod-alias
        keepalive: 50s
        forwards: ["25432:prod-db:5432"]
tunnels:
  web:
    host: app1
    forwards: ["8080:localhost:80"]
groups:
  all-db: [dev.db, prod.db]
`

func TestRoute(t *testing.T) {
	cfg := mustLoad(t, envYAML)
	r, err := cfg.Route(cfg.Envs["dev"].Tunnels["db"])
	if err != nil {
		t.Fatal(err)
	}
	if r.Dest != "admin@10.0.1.10" || strings.Join(r.Jumps, ",") != "ec2-user@dev-bastion,10.0.0.2" {
		t.Errorf("dev.db の経路 = %+v", r)
	}
	r, _ = cfg.Route(cfg.Envs["dev"].Tunnels["cache"])
	if r.Dest != "ops@shared.example.com" || len(r.Jumps) != 0 {
		t.Errorf("ルート環境のホストへのフォールバック = %+v", r)
	}
	r, _ = cfg.Route(cfg.Envs["prod"].Tunnels["db"])
	if r.Dest != "prod-alias" {
		t.Errorf("hosts に無い host はそのまま = %+v", r)
	}
}

func TestSelect(t *testing.T) {
	cfg := mustLoad(t, envYAML)
	ids := func(targets ...string) string {
		ts, err := cfg.Select(targets)
		if err != nil {
			return "ERR: " + err.Error()
		}
		var s []string
		for _, t := range ts {
			s = append(s, t.ID())
		}
		return strings.Join(s, ",")
	}
	cases := map[string]string{
		"dev":           "dev.cache,dev.db",
		"dev.db":        "dev.db",
		"all-db":        "dev.db,prod.db",
		"web":           "web",
		"dev prod":      "dev.cache,dev.db,prod.db",
		"dev.db all-db": "dev.db,prod.db",
	}
	for in, want := range cases {
		if got := ids(strings.Fields(in)...); got != want {
			t.Errorf("Select(%s) = %s, want %s", in, got, want)
		}
	}
	if got := ids("nope"); !strings.Contains(got, `"nope" に一致する環境・グループ・トンネルがありません`) {
		t.Errorf("存在しない対象 = %s", got)
	}
}

func TestEffective(t *testing.T) {
	cfg := mustLoad(t, envYAML)
	if e := cfg.Effective(cfg.Envs[""].Tunnels["web"]); e.Keepalive != 20*time.Second || e.SSH != "auto" || !e.Reconnect {
		t.Errorf("web = %+v", e)
	}
	if e := cfg.Effective(cfg.Envs["dev"].Tunnels["db"]); e.Keepalive != 40*time.Second {
		t.Errorf("環境の値で上書き = %+v", e)
	}
	if e := cfg.Effective(cfg.Envs["prod"].Tunnels["db"]); e.Keepalive != 50*time.Second || !e.Protected {
		t.Errorf("トンネルの値で上書き = %+v", e)
	}
}
