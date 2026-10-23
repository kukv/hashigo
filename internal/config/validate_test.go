package config

import "testing"

func TestValidate(t *testing.T) {
	cases := []struct {
		name, yaml, goos, want string
	}{
		{"via の循環", `
hosts:
  a: { host: a.example.com, via: b }
  b: { host: b.example.com, via: a }
`, "linux", "via が循環しています: a → b → a"},
		{"存在しない via", `
tunnels:
  t1: { via: nope, forwards: ["15432:db:5432"] }
`, "linux", `via のホスト "nope" が見つかりません`},
		{"ポートの重複", `
tunnels:
  t1: { host: x, forwards: ["15432:db:5432"] }
environments:
  dev:
    tunnels:
      t2: { host: y, forwards: ["15432:db:5432"] }
`, "linux", "ローカルポート 15432 は"},
		{"環境名とトンネル名の衝突", `
tunnels:
  dev: { host: x, forwards: ["15432:db:5432"] }
environments:
  dev: {}
`, "linux", `トンネル "dev" は環境名と衝突しています`},
		{"グループ名とトンネル名の衝突", `
tunnels:
  web: { host: x, forwards: ["15432:db:5432"] }
groups:
  web: [web]
`, "linux", `グループ "web" はトンネル名と衝突しています`},
		{"host と via の両方", `
hosts:
  b: { host: b }
tunnels:
  t1: { host: x, via: b, forwards: ["15432:db:5432"] }
`, "linux", "host か via のどちらか一方を書きます"},
		{"host のないホスト", `
hosts:
  b: { user: u }
`, "linux", "host がありません"},
		{"ecs の必須項目", `
tunnels:
  t1: { type: ecs, cluster: c, service: s, forwards: ["15432:db:5432"] }
`, "linux", "container がありません"},
		{"ecs に host", `
tunnels:
  t1: { type: ecs, host: x, cluster: c, service: s, container: a, forwards: ["15432:db:5432"] }
`, "linux", "type: ecs では host / via を書けません"},
		{"ssh に profile", `
tunnels:
  t1: { host: x, profile: p, forwards: ["15432:db:5432"] }
`, "linux", "profile は type: ecs でだけ使えます"},
		{"ecs に keepalive", `
tunnels:
  t1: { type: ecs, cluster: c, service: s, container: a, keepalive: 10s, forwards: ["15432:db:5432"] }
`, "linux", "keepalive は type: ssh でだけ使えます"},
		{"不正な type", `
tunnels:
  t1: { type: k8s, host: x, forwards: ["15432:db:5432"] }
`, "linux", `type は ssh か ecs です: "k8s"`},
		{"不正な keepalive", `
defaults:
  keepalive: 500ms
`, "linux", "keepalive は 30s のような 1 秒以上の時間で書きます"},
		{"Windows ネイティブの ssh_config: wsl", `
defaults:
  ssh_config: wsl
`, "windows", "Windows ネイティブでは ssh_config: wsl は使えません"},
		{"存在しないグループのメンバー", `
groups:
  g: [x]
`, "linux", `グループのメンバー "x" が見つかりません`},
		{"不正な wsl.mode", `
wsl:
  mode: bridge
`, "linux", "wsl.mode は auto / mirrored / relay / native のいずれかです"},
		{"hosts の host が - 始まり", `
hosts:
  h: { host: "-oProxyCommand=id" }
`, "linux", `host は - で始められません: "-oProxyCommand=id"`},
		{"hosts の user が - 始まり", `
hosts:
  h: { host: h, user: "-oProxyCommand=id" }
`, "linux", `user は - で始められません: "-oProxyCommand=id"`},
		{"トンネルの host が - 始まり", `
tunnels:
  t1: { host: "-oProxyCommand=id", forwards: ["15432:db:5432"] }
`, "linux", `host は - で始められません: "-oProxyCommand=id"`},
		{"名前にドット", `
environments:
  a.b: {}
`, "linux", "名前に . は使えません"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ps := loadString(t, c.yaml, c.goos)
			wantProblem(t, ps, c.want)
		})
	}
}

func TestValidateOK(t *testing.T) {
	mustLoad(t, envYAML)
	mustLoad(t, rootYAML)
}
