package platform

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Listener は TCP の待ち受け1件。
type Listener struct {
	Addr string
	Port int
}

// Exposed は LAN に公開されている（0.0.0.0 か :: で待ち受けている）かを返す。
func (l Listener) Exposed() bool { return l.Addr == "0.0.0.0" || l.Addr == "::" }

// WinProc は Windows 側のプロセス1件。
type WinProc struct {
	PID         int
	Name        string
	CommandLine string
}

func lines(out []byte) []string {
	return strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n")
}

var defaultRouteRe = regexp.MustCompile(`(?m)^default via (\S+)`)

// parseDefaultGateway は `ip route show default` の出力からゲートウェイの IP を取り出す。
func parseDefaultGateway(out []byte) (string, error) {
	m := defaultRouteRe.FindSubmatch(out)
	if m == nil {
		return "", errors.New("ip route の出力にデフォルトゲートウェイがありません")
	}
	return string(m[1]), nil
}

// parseListeners は「アドレス ポート」の行を読む。形式が違う行は無視する。
func parseListeners(out []byte) []Listener {
	var ls []Listener
	for _, line := range lines(out) {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		p, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		ls = append(ls, Listener{Addr: f[0], Port: p})
	}
	return ls
}

// parseWinProcs は「pid<TAB>名前<TAB>コマンドライン」の行を読む。形式が違う行は無視する。
func parseWinProcs(out []byte) []WinProc {
	var ps []WinProc
	for _, line := range lines(out) {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}
		ps = append(ps, WinProc{PID: pid, Name: parts[1], CommandLine: parts[2]})
	}
	return ps
}

// matchForwardProcs は ports のどれかを使っているプロセスを返す。
// ssh.exe は `-L <bind>:<port>:`、aws.exe と session-manager-plugin.exe は localPortNumber で照合する。
// ローカルポートは設定全体で一意なので、別のトンネルのプロセスと取り違えることはない。
func matchForwardProcs(procs []WinProc, ports []int) []WinProc {
	var out []WinProc
	for _, p := range procs {
		for _, port := range ports {
			if procUsesPort(p, port) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func procUsesPort(p WinProc, port int) bool {
	n := strconv.Itoa(port)
	switch strings.ToLower(p.Name) {
	case "ssh.exe":
		re := regexp.MustCompile(`(?:^|\s)-L\s*"?(?:\[[^\]]+\]:|[^\s":\[]+:)?` + n + `:`)
		return re.MatchString(p.CommandLine)
	case "aws.exe", "session-manager-plugin.exe":
		re := regexp.MustCompile(`localPortNumber[=":\[\s\\]*` + n + `\b`)
		return re.MatchString(p.CommandLine)
	}
	return false
}

// HostsHas は hosts ファイルに「127.0.0.1 <name>」の行があるかを返す。
func HostsHas(data []byte, name string) bool {
	for _, line := range lines(data) {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "127.0.0.1" {
			continue
		}
		for _, h := range f[1:] {
			if strings.EqualFold(h, name) {
				return true
			}
		}
	}
	return false
}
