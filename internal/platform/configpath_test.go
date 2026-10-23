package platform

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

const (
	exeSSH = "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"
	exeAWS = "/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe"
)

func wslConfigHost(extra fakeRunner) *Host {
	run := fakeRunner{
		"cmd.exe /c echo %USERPROFILE%":          "C:\\Users\\taro\r\n",
		"wslpath -u C:\\Users\\taro":             "/mnt/c/Users/taro\n",
		"wslpath -w /home/u/.ssh/config":         "\\\\wsl.localhost\\Ubuntu\\home\\u\\.ssh\\config\n",
		"wslpath -w /home/u/work/ssh_config":     "\\\\wsl.localhost\\Ubuntu\\home\\u\\work\\ssh_config\n",
		"wslpath -w /home/u/work/aws_config":     "\\\\wsl.localhost\\Ubuntu\\home\\u\\work\\aws_config\n",
		"wslpath -u C:\\Users\\taro\\.ssh\\id_a": "/mnt/c/Users/taro/.ssh/id_a\n",
	}
	for k, v := range extra {
		run[k] = v
	}
	return newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "WSLENV": "USERPROFILE/p"}, nil, nil, run)
}

func TestSSHConfigFor(t *testing.T) {
	ctx := context.Background()
	wsl := wslConfigHost(nil)
	cases := []struct {
		name, bin, setting, arg, display string
	}{
		{"exeのauto", exeSSH, "auto", "", "/mnt/c/Users/taro/.ssh/config"},
		{"exeのwindows", exeSSH, "windows", "", "/mnt/c/Users/taro/.ssh/config"},
		{"exeのwsl", exeSSH, "wsl", `\\wsl.localhost\Ubuntu\home\u\.ssh\config`, `\\wsl.localhost\Ubuntu\home\u\.ssh\config`},
		{"exeのパス", exeSSH, "/home/u/work/ssh_config", `\\wsl.localhost\Ubuntu\home\u\work\ssh_config`, `\\wsl.localhost\Ubuntu\home\u\work\ssh_config`},
		{"linuxのwindows", "/usr/bin/ssh", "windows", "/mnt/c/Users/taro/.ssh/config", "/mnt/c/Users/taro/.ssh/config"},
		{"linuxのwsl", "/usr/bin/ssh", "wsl", "", "/home/u/.ssh/config"},
		{"linuxのパス", "/usr/bin/ssh", "/home/u/work/ssh_config", "/home/u/work/ssh_config", "/home/u/work/ssh_config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			arg, display, err := wsl.SSHConfigFor(ctx, c.bin, c.setting)
			if err != nil || arg != c.arg || display != c.display {
				t.Fatalf("SSHConfigFor = %q, %q, %v; want %q, %q", arg, display, err, c.arg, c.display)
			}
		})
	}

	plain := newHost(nil, nil, nil, nil)
	for _, s := range []string{"windows", "wsl"} {
		if _, _, err := plain.SSHConfigFor(ctx, "/usr/bin/ssh", s); err == nil {
			t.Errorf("WSL でも Windows でもなければ %s はエラー", s)
		}
	}
	win := newHost(nil, nil, nil, nil)
	win.GOOS = "windows"
	if arg, display, err := win.SSHConfigFor(ctx, `C:\ssh.exe`, "windows"); err != nil || arg != "" || display != filepath.Join("/home/u", ".ssh", "config") {
		t.Errorf("Windows ネイティブの windows = %q, %q, %v", arg, display, err)
	}
}

func TestAWSConfigFor(t *testing.T) {
	ctx := context.Background()
	wsl := wslConfigHost(nil)
	// WSL 側は Linux なので、テストを Windows で動かしてもパスは / 区切り
	linuxCfg := "/home/u/.aws/config"
	linuxCred := "/home/u/.aws/credentials"
	cases := []struct {
		name, bin, setting string
		env                []string
		display            string
	}{
		{"exeのauto", exeAWS, "auto", nil, "/mnt/c/Users/taro/.aws/config"},
		{"exeのwindows", exeAWS, "windows", nil, "/mnt/c/Users/taro/.aws/config"},
		{"exeのwsl", exeAWS, "wsl", []string{
			"AWS_CONFIG_FILE=" + linuxCfg,
			"AWS_SHARED_CREDENTIALS_FILE=" + linuxCred,
			"WSLENV=USERPROFILE/p:AWS_CONFIG_FILE/p:AWS_SHARED_CREDENTIALS_FILE/p",
		}, linuxCfg},
		{"exeのパス", exeAWS, "/home/u/work/aws_config", []string{
			`AWS_CONFIG_FILE=\\wsl.localhost\Ubuntu\home\u\work\aws_config`,
			"WSLENV=USERPROFILE/p:AWS_CONFIG_FILE",
		}, `\\wsl.localhost\Ubuntu\home\u\work\aws_config`},
		{"linuxのwindows", "/usr/bin/aws", "windows", []string{
			"AWS_CONFIG_FILE=/mnt/c/Users/taro/.aws/config",
			"AWS_SHARED_CREDENTIALS_FILE=/mnt/c/Users/taro/.aws/credentials",
		}, "/mnt/c/Users/taro/.aws/config"},
		{"linuxのwsl", "/usr/bin/aws", "wsl", nil, linuxCfg},
		{"linuxのパス", "/usr/bin/aws", "/home/u/work/aws_config", []string{"AWS_CONFIG_FILE=/home/u/work/aws_config"}, "/home/u/work/aws_config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, display, err := wsl.AWSConfigFor(ctx, c.bin, c.setting)
			if err != nil || !reflect.DeepEqual(env, c.env) || display != c.display {
				t.Fatalf("AWSConfigFor = %q, %q, %v; want %q, %q", env, display, err, c.env, c.display)
			}
		})
	}

	withEnv := newHost(map[string]string{"AWS_CONFIG_FILE": "/etc/aws/config"}, nil, nil, nil)
	if _, display, _ := withEnv.AWSConfigFor(ctx, "/usr/bin/aws", "auto"); display != "/etc/aws/config" {
		t.Errorf("auto で AWS_CONFIG_FILE があればそれを表示する: %q", display)
	}
	if _, _, err := newHost(nil, nil, nil, nil).AWSConfigFor(ctx, "/usr/bin/aws", "wsl"); err == nil {
		t.Error("WSL でなければ wsl はエラー")
	}
}

func TestToWSLPath(t *testing.T) {
	got, err := wslConfigHost(nil).ToWSLPath(context.Background(), `C:\Users\taro\.ssh\id_a`)
	if err != nil || got != "/mnt/c/Users/taro/.ssh/id_a" {
		t.Fatalf("ToWSLPath = %q, %v", got, err)
	}
}
