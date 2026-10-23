package doctor

import (
	"net"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kukv/hashigo/internal/driver/ecs"
	"github.com/kukv/hashigo/internal/platform"
)

// 4. 使用するプロファイルごとの認証状態。非対話で実行するので、MFA が要るときは失敗する
func (c *checker) profiles() Result {
	const title = "AWS の認証"
	ts := c.tunnels("ecs")
	if len(ts) == 0 {
		return na(title)
	}
	r := ok(title)
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	h := c.e.Host
	seen := map[string]bool{}
	for _, t := range ts {
		eff := c.e.Config.Effective(t)
		bin, err := h.AWSBinary(eff.AWS, c.mode)
		if err != nil {
			continue // 3 で報告する
		}
		env, _, err := h.AWSConfigFor(c.ctx, bin, eff.AWSConfig)
		if err != nil {
			continue // 7 で報告する
		}
		profile := ecs.ResolveProfile(eff.Profile, h.Getenv)
		key := strings.Join(append([]string{bin, profile, eff.Region}, env...), "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		args := []string{"sts", "get-caller-identity", "--profile", profile}
		if eff.Region != "" {
			args = append(args, "--region", eff.Region)
		}
		if _, err := h.Runner(platform.RunOptions{Env: env}).Output(c.ctx, bin, args...); err != nil {
			msg := platform.ErrText(err)
			switch {
			case ecs.IsAuthExpired(msg):
				r.line(Fail, "%s: %s", profile, ecs.LoginHint(bin, profile))
			case strings.Contains(msg, "MFA"):
				r.line(Warn, "%s: 要認証（MFA）。up のときに入力します", profile)
			default:
				r.line(Fail, "%s: %s", profile, msg)
			}
			continue
		}
		r.line(OK, "%s: 認証済み", profile)
	}
	return r
}

// 8. IdentityFile の存在。ssh -G は既定の鍵も列挙するので、1つも存在しないときだけ警告する
func (c *checker) identityFiles() Result {
	const title = "鍵ファイル（IdentityFile）"
	ts := c.tunnels("ssh")
	if len(ts) == 0 {
		return na(title)
	}
	r := ok(title)
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	h := c.e.Host
	for _, t := range ts {
		eff := c.e.Config.Effective(t)
		bin, err := h.SSHBinary(eff.SSH, c.mode)
		if err != nil {
			continue // 2 で報告する
		}
		route, err := c.e.Config.Route(t)
		if err != nil {
			continue
		}
		arg, _, err := h.SSHConfigFor(c.ctx, bin, eff.SSHConfig)
		if err != nil {
			continue // 7 で報告する
		}
		args := []string{"-G"}
		if arg != "" {
			args = append(args, "-F", arg)
		}
		if len(route.Jumps) > 0 {
			args = append(args, "-J", strings.Join(route.Jumps, ","))
		}
		args = append(args, "--", route.Dest)
		out, err := h.Run.Output(c.ctx, bin, args...)
		if err != nil {
			r.line(Warn, "%s: ssh -G に失敗しました: %s", t.ID(), platform.ErrText(err))
			continue
		}
		var found, missing []string
		for _, p := range parseIdentityFiles(out) {
			lp := c.localPath(bin, p)
			if _, err := h.Stat(lp); err == nil {
				found = append(found, lp)
			} else {
				missing = append(missing, lp)
			}
		}
		if len(found) == 0 {
			r.line(Warn, "%s: 鍵ファイルが1つも見つかりません（%s）", t.ID(), strings.Join(missing, ", "))
		} else {
			r.line(OK, "%s: %s", t.ID(), strings.Join(found, ", "))
		}
	}
	return r
}

// parseIdentityFiles は ssh -G の出力から identityfile の値を取り出す。
func parseIdentityFiles(out []byte) []string {
	var ps []string
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		if v, ok := strings.CutPrefix(line, "identityfile "); ok {
			ps = append(ps, strings.TrimSpace(v))
		}
	}
	return ps
}

// localPath は、bin が読む鍵のパスを、hashigo から確認できるパスにする。
func (c *checker) localPath(bin, p string) string {
	h := c.e.Host
	cross := h.CrossesToWindows(bin)
	if rest, ok := strings.CutPrefix(p, "~"); ok {
		if cross {
			if home, err := h.WindowsHome(c.ctx); err == nil {
				return path.Join(home, strings.ReplaceAll(rest, `\`, "/"))
			}
			return p
		}
		if home, err := h.HomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
		return p
	}
	if cross && isWindowsAbs(p) {
		if w, err := h.ToWSLPath(c.ctx, p); err == nil {
			return w
		}
	}
	return p
}

func isWindowsAbs(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

// 9. hosts の設定漏れ。WSL では Windows 側と WSL 側の両方を確認する
func (c *checker) hosts() Result {
	const title = "hosts の設定漏れ"
	var ts []string
	ids := map[string]string{}
	for _, t := range c.e.Tunnels {
		if t.Hostname != "" {
			ts = append(ts, t.Hostname)
			ids[t.Hostname] = t.ID()
		}
	}
	if len(ts) == 0 {
		return na(title)
	}
	r := ok(title)
	h := c.e.Host
	files := h.HostsFiles()
	for _, name := range ts {
		var missing []string
		for _, f := range files {
			data, err := h.ReadFile(f.Path)
			if err != nil {
				r.line(Warn, "%s を読めません: %v", f.Path, err)
				continue
			}
			if !platform.HostsHas(data, name) {
				missing = append(missing, hostsLabel(f))
			}
		}
		if len(missing) > 0 {
			r.line(Warn, "「127.0.0.1 %s」が %sにありません（%s）", name, strings.Join(missing, "と"), ids[name])
		} else {
			r.line(OK, "%s: 設定済み", name)
		}
	}
	return r
}

func hostsLabel(f platform.HostsFile) string {
	if f.Label == "" {
		return f.Path
	}
	return f.Label + " 側（" + f.Path + "）"
}

// 10. relay で動いている場合の LAN 公開（Windows 側の待ち受けアドレスの確認）
func (c *checker) lanExposure() Result {
	const title = "LAN への公開（relay）"
	if c.mode != "relay" {
		return na(title)
	}
	seen := map[int]bool{}
	var ports []int
	for _, run := range c.e.Runs {
		for _, s := range run.Slots {
			for _, p := range s.Ports {
				_, port, err := net.SplitHostPort(p)
				if err != nil {
					continue
				}
				n, err := strconv.Atoi(port)
				if err != nil || seen[n] {
					continue
				}
				seen[n] = true
				ports = append(ports, n)
			}
		}
	}
	if len(ports) == 0 {
		return na(title, "relay で起動中のトンネルがありません")
	}
	sort.Ints(ports)
	r := ok(title)
	ls, err := c.e.Host.WindowsListeners(c.ctx, ports)
	if err != nil {
		r.line(Warn, "%v", err)
		return r
	}
	for _, l := range ls {
		if l.Exposed() {
			r.line(Warn, "Windows 側でポート %d が %s で待ち受けています（LAN に公開されています）", l.Port, l.Addr)
		}
	}
	if len(r.Lines) == 0 {
		r.line(OK, "LAN には公開されていません")
	}
	return r
}
