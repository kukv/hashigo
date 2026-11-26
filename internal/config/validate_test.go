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
`, "linux", "profile は type: ecs / ssm でだけ使えます"},
		{"ecs に keepalive", `
tunnels:
  t1: { type: ecs, cluster: c, service: s, container: a, keepalive: 10s, forwards: ["15432:db:5432"] }
`, "linux", "keepalive は type: ssh でだけ使えます"},
		{"不正な type", `
tunnels:
  t1: { type: k8s, host: x, forwards: ["15432:db:5432"] }
`, "linux", `type は ssh / ecs / ssm のいずれかです: "k8s"`},
		{"ssm に target が無い", `
tunnels:
  t1: { type: ssm, forwards: ["15432:db:5432"] }
`, "linux", "target か target_tag のどちらか一方を書きます"},
		{"ssm に target と target_tag の両方", `
tunnels:
  t1: { type: ssm, target: i-0123abcd, target_tag: { Name: b }, forwards: ["15432:db:5432"] }
`, "linux", "target か target_tag のどちらか一方を書きます"},
		{"ssm の target がインスタンス ID でない", `
tunnels:
  t1: { type: ssm, target: bastion, forwards: ["15432:db:5432"] }
`, "linux", "target はインスタンス ID"},
		{"ssm の target_tag が空", `
tunnels:
  t1: { type: ssm, target_tag: {}, forwards: ["15432:db:5432"] }
`, "linux", "target_tag にタグがありません"},
		{"ssm の target_tag の値が空", `
tunnels:
  t1: { type: ssm, target_tag: { Name: "" }, forwards: ["15432:db:5432"] }
`, "linux", "target_tag のタグ名と値は空にできません"},
		{"ssm の target_tag が文字列", `
tunnels:
  t1: { type: ssm, target_tag: bastion, forwards: ["15432:db:5432"] }
`, "linux", "target_tag は {タグ名: 値} のマッピングで書きます"},
		{"ssm に cluster", `
tunnels:
  t1: { type: ssm, target: i-0123abcd, cluster: c, forwards: ["15432:db:5432"] }
`, "linux", "cluster は type: ecs でだけ使えます"},
		{"ssm に via", `
hosts:
  b: { host: b }
tunnels:
  t1: { type: ssm, target: i-0123abcd, via: b, forwards: ["15432:db:5432"] }
`, "linux", "type: ssm では host / via を書けません"},
		{"ssm に ssh_config", `
tunnels:
  t1: { type: ssm, target: i-0123abcd, ssh_config: wsl, forwards: ["15432:db:5432"] }
`, "linux", "ssh_config は type: ssh でだけ使えます"},
		{"ssm の bind", `
tunnels:
  t1: { type: ssm, target: i-0123abcd, forwards: [{ local: "0.0.0.0:15432", remote: "db:5432" }] }
`, "linux", "type: ssm の forwards には待ち受けアドレスを書けません"},
		{"ssh に target", `
tunnels:
  t1: { host: x, target: i-0123abcd, forwards: ["15432:db:5432"] }
`, "linux", "target は type: ssm でだけ使えます"},
		{"ecs に target_tag", `
tunnels:
  t1: { type: ecs, cluster: c, service: s, container: a, target_tag: { Name: b }, forwards: ["15432:db:5432"] }
`, "linux", "target_tag は type: ssm でだけ使えます"},
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

func TestValidateECSBind(t *testing.T) {
	_, ps := loadString(t, `
tunnels:
  task:
    type: ecs
    cluster: c
    service: s
    container: app
    forwards:
      - { local: "0.0.0.0:35432", remote: "db:5432" }
`, "linux")
	wantProblem(t, ps, "type: ecs の forwards には待ち受けアドレスを書けません")
}
