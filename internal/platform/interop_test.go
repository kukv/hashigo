package platform

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// funcRunner は呼び出しを関数で受ける Runner。powershell のスクリプトのような長い引数の確認に使う。
type funcRunner func(name string, args []string) ([]byte, error)

func (f funcRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	return f(name, args)
}

func TestParseDefaultGateway(t *testing.T) {
	got, err := parseDefaultGateway([]byte("default via 172.29.16.1 dev eth0 proto kernel\n172.29.16.0/20 dev eth0 proto kernel scope link src 172.29.20.5\n"))
	if err != nil || got != "172.29.16.1" {
		t.Fatalf("parseDefaultGateway = %q, %v", got, err)
	}
	if _, err := parseDefaultGateway([]byte("172.29.16.0/20 dev eth0\n")); err == nil {
		t.Error("default が無ければエラー")
	}
}

func TestParseListeners(t *testing.T) {
	got := parseListeners([]byte("172.29.16.1 15432\r\n0.0.0.0 16379\r\n:: 8080\r\n\r\nbroken\r\n"))
	want := []Listener{{"172.29.16.1", 15432}, {"0.0.0.0", 16379}, {"::", 8080}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseListeners = %+v", got)
	}
	if got[0].Exposed() || !got[1].Exposed() || !got[2].Exposed() {
		t.Error("0.0.0.0 と :: だけが LAN に公開されている")
	}
}

func TestMatchForwardProcs(t *testing.T) {
	procs := parseWinProcs([]byte(strings.Join([]string{
		"100\tssh.exe\tC:\\Windows\\System32\\OpenSSH\\ssh.exe -N -o BatchMode=yes -L 127.0.0.1:15432:db:5432 -- admin@10.0.1.10",
		"101\tssh.exe\tssh.exe -N -L 127.0.0.1:25000:db:15432 -- other",
		"102\tssh.exe\tssh.exe -N -L [::1]:16379:redis:6379 -- x",
		"103\taws.exe\t\"C:\\Program Files\\Amazon\\AWSCLIV2\\aws.exe\" ssm start-session --parameters host=db,portNumber=5432,localPortNumber=35432",
		"104\tsession-manager-plugin.exe\tsession-manager-plugin.exe {\\\"localPortNumber\\\":[\\\"35432\\\"]} ap-northeast-1 StartSession",
		"105\taws.exe\taws.exe ssm start-session --parameters localPortNumber=354320",
		"106\tnotepad.exe\tnotepad.exe 15432",
		"bad line",
	}, "\r\n")))
	got := matchForwardProcs(procs, []int{15432, 16379, 35432})
	var pids []int
	for _, p := range got {
		pids = append(pids, p.PID)
	}
	if !reflect.DeepEqual(pids, []int{100, 102, 103, 104}) {
		t.Fatalf("一致した pid = %v", pids)
	}
}

func TestHostsHas(t *testing.T) {
	data := []byte("# comment 127.0.0.1 skip.local\r\n127.0.0.1 localhost app.local # app\r\n::1 v6.local\r\n")
	if !HostsHas(data, "app.local") || !HostsHas(data, "APP.local") {
		t.Error("127.0.0.1 の行にあれば true（大文字小文字は区別しない）")
	}
	if HostsHas(data, "skip.local") || HostsHas(data, "v6.local") || HostsHas(data, "none.local") {
		t.Error("コメントや 127.0.0.1 以外の行は数えない")
	}
}

func TestDefaultGateway(t *testing.T) {
	h := newHost(nil, nil, nil, fakeRunner{"ip route show default": "default via 172.29.16.1 dev eth0\n"})
	if got, err := h.DefaultGateway(context.Background()); err != nil || got != "172.29.16.1" {
		t.Fatalf("DefaultGateway = %q, %v", got, err)
	}
}

func TestWindowsListeners(t *testing.T) {
	var script string
	h := newHost(nil, nil, nil, nil)
	h.Run = funcRunner(func(_ string, args []string) ([]byte, error) {
		script = args[len(args)-1]
		return []byte("0.0.0.0 15432\r\n"), nil
	})
	got, err := h.WindowsListeners(context.Background(), []int{15432, 16379})
	if err != nil || len(got) != 1 || !got[0].Exposed() {
		t.Fatalf("WindowsListeners = %+v, %v", got, err)
	}
	if !strings.Contains(script, "Get-NetTCPConnection -State Listen -LocalPort 15432,16379") {
		t.Errorf("script = %s", script)
	}
	if got, _ := h.WindowsListeners(context.Background(), nil); got != nil {
		t.Error("ポートが無ければ呼ばない")
	}
}

func TestCleanupWindows(t *testing.T) {
	var killed []string
	h := newHost(nil, nil, nil, nil)
	h.Run = funcRunner(func(name string, args []string) ([]byte, error) {
		switch name {
		case "powershell.exe":
			if !strings.Contains(args[len(args)-1], "Win32_Process") {
				return nil, errors.New("想定外のスクリプト")
			}
			return []byte("100\tssh.exe\tssh.exe -L 127.0.0.1:15432:db:5432 -- x\r\n200\tssh.exe\tssh.exe -L 127.0.0.1:9999:db:5432 -- y\r\n300\taws.exe\taws.exe localPortNumber=35432\r\n"), nil
		case "taskkill.exe":
			killed = append(killed, strings.Join(args, " "))
			if args[2] == "300" {
				return nil, errors.New("access denied")
			}
			return nil, nil
		}
		return nil, errors.New("想定外: " + name)
	})
	got, err := h.CleanupWindows(context.Background(), []int{15432, 35432})
	if !reflect.DeepEqual(killed, []string{"/F /PID 100", "/F /PID 300"}) {
		t.Errorf("taskkill = %v", killed)
	}
	if len(got) != 1 || got[0].PID != 100 {
		t.Errorf("終了できたもの = %+v", got)
	}
	if err == nil || !strings.Contains(err.Error(), "aws.exe (pid 300)") {
		t.Errorf("失敗したものはエラーにまとめる: %v", err)
	}
}

func TestHostsFiles(t *testing.T) {
	if got := newHost(nil, nil, nil, nil).HostsFiles(); len(got) != 1 || got[0].Path != "/etc/hosts" {
		t.Errorf("Linux = %+v", got)
	}
	wsl := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, nil).HostsFiles()
	if len(wsl) != 2 || wsl[0].Label != "WSL" || wsl[1].Path != "/mnt/c/Windows/System32/drivers/etc/hosts" {
		t.Errorf("WSL = %+v", wsl)
	}
	win := newHost(map[string]string{"SystemRoot": `D:\Windows`}, nil, nil, nil)
	win.GOOS = "windows"
	if got := win.HostsFiles(); len(got) != 1 || got[0].Label != "Windows" || !strings.HasPrefix(got[0].Path, `D:\Windows`) {
		t.Errorf("Windows = %+v", got)
	}
}
