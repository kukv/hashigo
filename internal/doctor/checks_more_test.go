package doctor

import (
	"runtime"
	"testing"

	"github.com/kukv/hashigo/internal/state"
)

const ecsYAML = "tunnels:\n  task: { type: ecs, cluster: c, service: s, container: app, profile: stg, forwards: [\"35432:db:5432\"] }\n"

func TestAllElevenItems(t *testing.T) {
	h := newHost(nil, nil, map[string]string{"ssh": "/usr/bin/ssh"}, fakeRunner{
		"/usr/bin/ssh -V":       "OpenSSH_9\n",
		"/usr/bin/ssh -G -- ap": "user u\nidentityfile ~/.ssh/id_ed25519\n",
	})
	rs := runDoctor(h, loadConfig(t, sshYAML), nil, closed)
	want := []string{
		"実行環境", "ssh", "aws と session-manager-plugin", "AWS の認証", "socat（relay モード）",
		"ローカルポートの衝突", "ssh / aws の config", "鍵ファイル（IdentityFile）", "hosts の設定漏れ", "LAN への公開（relay）", "ECS × relay",
	}
	if len(rs) != len(want) {
		t.Fatalf("項目の数 = %d", len(rs))
	}
	for i, w := range want {
		if rs[i].Title != w {
			t.Errorf("%d 番目 = %q, want %q", i+1, rs[i].Title, w)
		}
	}
}

func TestProfiles(t *testing.T) {
	cases := []struct {
		name  string
		out   string
		level Level
		line  string
	}{
		{"認証済み", `{"Account":"1"}`, OK, "stg: 認証済み"},
		{"MFA", "ERR:Enter MFA code for arn:aws:iam::1:mfa/u: EOF", Warn, "要認証（MFA）"},
		{"期限切れ", "ERR:The SSO session associated with this profile has expired", Fail, "/usr/bin/aws sso login --profile stg"},
		{"その他", "ERR:could not connect", Fail, "stg: could not connect"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHost(nil, nil, map[string]string{"aws": "/usr/bin/aws"}, fakeRunner{
				"/usr/bin/aws sts get-caller-identity --profile stg": c.out,
				"/usr/bin/aws --version":                             "aws-cli/2\n",
			})
			r := find(t, runDoctor(h, loadConfig(t, ecsYAML), nil, closed), "AWS の認証")
			if r.Level != c.level || !hasLine(r, c.line) {
				t.Errorf("AWS の認証 = %+v", r)
			}
		})
	}
}

func TestIdentityFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("パス区切りが異なるため Unix 系でのみ確認する")
	}
	cfg := loadConfig(t, "hosts:\n  b: { host: bastion, user: ec2-user }\ntunnels:\n  db: { via: b, forwards: [\"15432:db:5432\"] }\n  web: { host: app1, forwards: [\"18080:l:80\"] }\n")
	h := newHost(nil, map[string]string{"/home/u/.ssh/id_ed25519": ""}, map[string]string{"ssh": "/usr/bin/ssh"}, fakeRunner{
		"/usr/bin/ssh -V":                     "OpenSSH_9\n",
		"/usr/bin/ssh -G -- ec2-user@bastion": "identityfile ~/.ssh/id_rsa\nidentityfile ~/.ssh/id_ed25519\n",
		"/usr/bin/ssh -G -- app1":             "identityfile ~/.ssh/id_rsa\n",
	})
	r := find(t, runDoctor(h, cfg, nil, closed), "鍵ファイル（IdentityFile）")
	if r.Level != Warn || !hasLine(r, "db: /home/u/.ssh/id_ed25519") || !hasLine(r, "web: 鍵ファイルが1つも見つかりません") {
		t.Errorf("IdentityFile = %+v", r)
	}
}

func TestIdentityFilesWindowsSide(t *testing.T) {
	cfg := loadConfig(t, "wsl:\n  mode: mirrored\ntunnels:\n  db: { host: ap, forwards: [\"15432:db:5432\"] }\n")
	exe := "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"},
		map[string]string{"/mnt/c/Users/taro/.ssh/id_ed25519": "", "/mnt/c/keys/work": ""},
		map[string]string{"ssh.exe": exe},
		fakeRunner{
			"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
			"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
			"wslpath -u C:\\keys\\work":     "/mnt/c/keys/work\n",
			exe + " -V":                     "OpenSSH_for_Windows_9.5p1\r\n",
			exe + " -G -- ap":               "identityfile ~/.ssh/id_ed25519\r\nidentityfile C:\\keys\\work\r\n",
		})
	r := find(t, runDoctor(h, cfg, nil, closed), "鍵ファイル（IdentityFile）")
	if r.Level != OK || !hasLine(r, "/mnt/c/Users/taro/.ssh/id_ed25519") || !hasLine(r, "/mnt/c/keys/work") {
		t.Errorf("ssh.exe は Windows 側のパスで確認する: %+v", r)
	}
}

func TestHostsCheck(t *testing.T) {
	cfg := loadConfig(t, "tunnels:\n  web: { host: app1, hostname: app.local, forwards: [\"18080:l:80\"] }\n")
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"},
		map[string]string{
			"/etc/hosts": "127.0.0.1 localhost app.local\n",
			"/mnt/c/Windows/System32/drivers/etc/hosts": "127.0.0.1 localhost\r\n",
		},
		map[string]string{"ssh.exe": "/mnt/c/ssh.exe"},
		fakeRunner{
			"wslinfo --networking-mode":     "mirrored\n",
			"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
			"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
			"/mnt/c/ssh.exe -V":             "OpenSSH\n",
			"/mnt/c/ssh.exe -G -- app1":     "",
		})
	r := find(t, runDoctor(h, cfg, nil, closed), "hosts の設定漏れ")
	if r.Level != Warn || !hasLine(r, "「127.0.0.1 app.local」が Windows 側（/mnt/c/Windows/System32/drivers/etc/hosts）にありません") {
		t.Errorf("hosts = %+v", r)
	}
	if r := find(t, runDoctor(h, loadConfig(t, sshYAML), nil, closed), "hosts の設定漏れ"); r.Level != NA {
		t.Errorf("hostname が無ければ対象外: %+v", r)
	}
}

func TestLANExposure(t *testing.T) {
	cfg := loadConfig(t, sshYAML)
	run := fakeRunner{
		"wslinfo --networking-mode":     "nat\n",
		"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
		"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
		"/mnt/c/ssh.exe -V":             "OpenSSH\n",
		"/mnt/c/ssh.exe -G -- ap":       "",
		"powershell.exe -NoProfile -NonInteractive -Command Get-NetTCPConnection -State Listen -LocalPort 15432 -ErrorAction SilentlyContinue | ForEach-Object { \"$($_.LocalAddress) $($_.LocalPort)\" }": "0.0.0.0 15432\r\n",
	}
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, map[string]string{"ssh.exe": "/mnt/c/ssh.exe", "socat": "/usr/bin/socat"}, run)
	runs := []state.Run{{PID: 1, Slots: []state.Slot{
		{Tunnel: "db", Ports: []string{"172.29.16.1:15432"}},
		{Tunnel: "db", Ports: []string{"127.0.0.1:15432"}},
	}}}
	r := find(t, runDoctor(h, cfg, runs, closed), "LAN への公開（relay）")
	if r.Level != Warn || !hasLine(r, "ポート 15432 が 0.0.0.0 で待ち受けています") {
		t.Errorf("LAN = %+v", r)
	}
	if r := find(t, runDoctor(h, cfg, nil, closed), "LAN への公開（relay）"); r.Level != NA || !hasLine(r, "relay で起動中のトンネルがありません") {
		t.Errorf("起動中が無ければ対象外: %+v", r)
	}
}
