package platform

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultGateway は WSL から見たデフォルトゲートウェイ（Windows 側の WSL 用仮想 NIC）の IP を返す。
// この IP は再起動で変わるので、接続のたびに呼ぶ。
func (h *Host) DefaultGateway(ctx context.Context) (string, error) {
	out, err := h.Run.Output(ctx, "ip", "route", "show", "default")
	if err != nil {
		return "", fmt.Errorf("ip route を実行できません: %w", err)
	}
	return parseDefaultGateway(out)
}

func (h *Host) powershell(ctx context.Context, script string) ([]byte, error) {
	return h.Run.Output(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
}

// WindowsListeners は、Windows 側で ports を待ち受けているアドレスを返す。
func (h *Host) WindowsListeners(ctx context.Context, ports []int) ([]Listener, error) {
	if len(ports) == 0 {
		return nil, nil
	}
	ps := make([]string, len(ports))
	for i, p := range ports {
		ps[i] = strconv.Itoa(p)
	}
	script := "Get-NetTCPConnection -State Listen -LocalPort " + strings.Join(ps, ",") +
		` -ErrorAction SilentlyContinue | ForEach-Object { "$($_.LocalAddress) $($_.LocalPort)" }`
	out, err := h.powershell(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("Get-NetTCPConnection を実行できません: %w", err)
	}
	return parseListeners(out), nil
}

// procScript は対象のプロセスを「pid<TAB>名前<TAB>コマンドライン」で1行ずつ出す。
const procScript = "Get-CimInstance Win32_Process -Filter \"Name='ssh.exe' OR Name='aws.exe' OR Name='session-manager-plugin.exe'\"" +
	" | ForEach-Object { \"$($_.ProcessId)\t$($_.Name)\t$($_.CommandLine)\" }"

// CleanupWindows は、ports を使っている Windows 側の ssh.exe / aws.exe / session-manager-plugin.exe を
// taskkill で終了させる。WSL 側のプロセスを止めても Windows 側に残ることがあるため、up の停止後に呼ぶ（仕様 6.5）。
// 終了させたプロセスを返す。終了させられなかったものはエラーにまとめる。
func (h *Host) CleanupWindows(ctx context.Context, ports []int) ([]WinProc, error) {
	out, err := h.powershell(ctx, procScript)
	if err != nil {
		return nil, fmt.Errorf("プロセスの一覧を取得できません（Windows 側）: %w", err)
	}
	var killed []WinProc
	var errs []error
	for _, p := range matchForwardProcs(parseWinProcs(out), ports) {
		if _, err := h.Run.Output(ctx, "taskkill.exe", "/F", "/PID", strconv.Itoa(p.PID)); err != nil {
			errs = append(errs, fmt.Errorf("%s (pid %d) を終了できません: %w", p.Name, p.PID, err))
			continue
		}
		killed = append(killed, p)
	}
	return killed, errors.Join(errs...)
}

// HostsFile は確認する hosts ファイル1つ。Label は表示用（WSL / Windows）。
type HostsFile struct {
	Label string
	Path  string
}

// HostsFiles は、ブラウザなどが名前解決に使う hosts ファイルの一覧を返す。WSL では両側を返す。
func (h *Host) HostsFiles() []HostsFile {
	switch {
	case h.GOOS == "windows":
		root := h.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return []HostsFile{{Label: "Windows", Path: filepath.Join(root, "System32", "drivers", "etc", "hosts")}}
	case h.IsWSL():
		return []HostsFile{
			{Label: "WSL", Path: "/etc/hosts"},
			{Label: "Windows", Path: "/mnt/c/Windows/System32/drivers/etc/hosts"},
		}
	}
	return []HostsFile{{Path: "/etc/hosts"}}
}
