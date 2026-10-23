package doctor

import (
	"errors"
	"path"
	"strings"

	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/state"
)

// 1. OS と WSL の判定、ネットワークモード、.wslconfig の内容
func (c *checker) platform() Result {
	h := c.e.Host
	r := ok("実行環境")
	r.line(OK, "OS: %s", h.GOOS)
	if !h.IsWSL() {
		r.line(OK, "WSL: 対象外")
		return r
	}
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	r.line(OK, "WSL: ネットワークモード %s、hashigo のモード %s", h.NetworkMode(c.ctx), c.mode)
	home, err := h.WindowsHome(c.ctx)
	if err != nil {
		r.line(Warn, "%v", err)
		return r
	}
	b, err := h.ReadFile(path.Join(home, ".wslconfig"))
	if err != nil {
		r.line(OK, ".wslconfig: なし")
		return r
	}
	r.line(OK, ".wslconfig:")
	for _, l := range strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r", "")), "\n") {
		r.line(OK, "  %s", l)
	}
	return r
}

// 2. ssh バイナリの場所と -V
func (c *checker) ssh() Result {
	const title = "ssh"
	ts := c.tunnels("ssh")
	if len(ts) == 0 {
		return na(title)
	}
	r := ok(title)
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	seen := map[string]bool{}
	for _, t := range ts {
		bin, err := c.e.Host.SSHBinary(c.e.Config.Effective(t).SSH, c.mode)
		if err != nil {
			r.line(Fail, "%s: %v", t.ID(), err)
			continue
		}
		if seen[bin] {
			continue
		}
		seen[bin] = true
		l, s := c.version(bin, "-V")
		r.line(l, "%s", s)
	}
	return r
}

const windowsPlugin = "/mnt/c/Program Files/Amazon/SessionManagerPlugin/bin/session-manager-plugin.exe"

// 3. aws バイナリの場所とバージョン、session-manager-plugin の有無
func (c *checker) aws() Result {
	const title = "aws と session-manager-plugin"
	ts := c.tunnels("ecs")
	if len(ts) == 0 {
		return na(title)
	}
	r := ok(title)
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	seen := map[string]bool{}
	for _, t := range ts {
		bin, err := c.e.Host.AWSBinary(c.e.Config.Effective(t).AWS, c.mode)
		if err != nil {
			r.line(Fail, "%s: %v", t.ID(), err)
			continue
		}
		if seen[bin] {
			continue
		}
		seen[bin] = true
		l, s := c.version(bin, "--version")
		r.line(l, "%s", s)
		if p, err := c.plugin(bin); err != nil {
			r.line(Fail, "session-manager-plugin が見つかりません（%s 用）。AWS のドキュメントに従って入れてください", bin)
		} else {
			r.line(OK, "session-manager-plugin: %s", p)
		}
	}
	return r
}

// plugin は bin と同じ側の session-manager-plugin を探す。
func (c *checker) plugin(bin string) (string, error) {
	h := c.e.Host
	if !h.CrossesToWindows(bin) {
		return h.LookPath("session-manager-plugin")
	}
	if p, err := h.LookPath("session-manager-plugin.exe"); err == nil {
		return p, nil
	}
	if _, err := h.Stat(windowsPlugin); err == nil {
		return windowsPlugin, nil
	}
	return "", errors.New("session-manager-plugin.exe が見つかりません")
}

// 5. socat の有無（relay モードの場合）
func (c *checker) socat() Result {
	const title = "socat（relay モード）"
	if c.mode != "relay" {
		return na(title)
	}
	r := ok(title)
	if p, err := c.e.Host.LookPath("socat"); err != nil {
		r.line(Fail, "socat が見つかりません。relay モードに必要です（例: sudo apt install socat）")
	} else {
		r.line(OK, "%s", p)
	}
	return r
}

// 6. ローカルポートの衝突（TCP 接続と状態ファイルで確認）
func (c *checker) ports() Result {
	r := ok("ローカルポートの衝突")
	for _, t := range c.e.Tunnels {
		for _, f := range t.Forwards {
			l := driver.Listen{Addr: f.Bind, Port: f.LocalPort}
			if !c.e.Dial(l.DialAddr()) {
				continue
			}
			if run, s, found := state.FindPort(c.e.Runs, l.String()); found {
				r.line(OK, "%s: %s（pid %d の up）が使用中", l, s.Tunnel, run.PID)
			} else {
				r.line(Warn, "%s（%s）: hashigo 外のプロセスが使用中", l, t.ID())
			}
		}
	}
	if len(r.Lines) == 0 {
		r.line(OK, "衝突なし")
	}
	return r
}

// 7. 実際に読む ssh / aws config のパス
func (c *checker) configPaths() Result {
	const title = "ssh / aws の config"
	if len(c.e.Tunnels) == 0 {
		return na(title)
	}
	r := ok(title)
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	h := c.e.Host
	seen := map[string]bool{}
	for _, t := range c.e.Tunnels {
		eff := c.e.Config.Effective(t)
		if t.Type == "ecs" {
			bin, err := h.AWSBinary(eff.AWS, c.mode)
			if err != nil || seen["aws\x00"+bin+"\x00"+eff.AWSConfig] {
				continue // バイナリが無いことは 3 で報告する
			}
			seen["aws\x00"+bin+"\x00"+eff.AWSConfig] = true
			if _, display, err := h.AWSConfigFor(c.ctx, bin, eff.AWSConfig); err != nil {
				r.line(Fail, "aws (%s, aws_config: %s): %v", bin, eff.AWSConfig, err)
			} else {
				r.line(OK, "aws (%s, aws_config: %s): %s", bin, eff.AWSConfig, display)
			}
			continue
		}
		bin, err := h.SSHBinary(eff.SSH, c.mode)
		if err != nil || seen["ssh\x00"+bin+"\x00"+eff.SSHConfig] {
			continue // バイナリが無いことは 2 で報告する
		}
		seen["ssh\x00"+bin+"\x00"+eff.SSHConfig] = true
		if _, display, err := h.SSHConfigFor(c.ctx, bin, eff.SSHConfig); err != nil {
			r.line(Fail, "ssh (%s, ssh_config: %s): %v", bin, eff.SSHConfig, err)
		} else {
			r.line(OK, "ssh (%s, ssh_config: %s): %s", bin, eff.SSHConfig, display)
		}
	}
	return r
}

// 11. ECS × relay の組み合わせ（R2: session-manager-plugin は 127.0.0.1 で待ち受けるため使えない）
func (c *checker) ecsRelay() Result {
	const title = "ECS × relay"
	if len(c.tunnels("ecs")) == 0 || c.mode != "relay" {
		return na(title)
	}
	r := ok(title)
	r.line(Fail, "ECS トンネルは relay モードでは使えません（session-manager-plugin は Windows の 127.0.0.1 でしか待ち受けないため）。.wslconfig で networkingMode=mirrored を有効にしてください")
	return r
}
