package config

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukv/hashigo/internal/platform"
)

type mapRunner map[string]string

func (m mapRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	if out, ok := m[key]; ok {
		return []byte(out), nil
	}
	return nil, fmt.Errorf("想定外の呼び出し: %s", key)
}

func searchHost(env map[string]string, exists ...string) *platform.Host {
	set := map[string]bool{}
	for _, p := range exists {
		set[p] = true
	}
	return &platform.Host{
		GOOS:     "linux",
		Getenv:   func(k string) string { return env[k] },
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		Stat: func(p string) (fs.FileInfo, error) {
			if set[p] {
				return nil, nil
			}
			return nil, fs.ErrNotExist
		},
		HomeDir: func() (string, error) { return "/home/u", nil },
		Run: mapRunner{
			"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
			"wslpath -u C:\\Users\\taro":     "/mnt/c/Users/taro\n",
		},
	}
}

func TestFind(t *testing.T) {
	ctx := context.Background()
	home := filepath.Join("/home/u", ".config", "hashigo", "config.yaml")
	xdg := filepath.Join("/xdg", "hashigo", "config.yaml")
	win := "/mnt/c/Users/taro/.config/hashigo/config.yaml"
	wsl := map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}

	cases := []struct {
		name     string
		explicit string
		env      map[string]string
		exists   []string
		want     string
	}{
		{"--config", "/x.yaml", nil, []string{"/x.yaml", "hashigo.yaml"}, "/x.yaml"},
		{"カレント", "", nil, []string{"hashigo.yaml", home}, "hashigo.yaml"},
		{"ホーム", "", nil, []string{home}, home},
		{"XDG", "", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, []string{xdg, home}, xdg},
		{"WSL の Windows ホーム", "", wsl, []string{win}, win},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Find(ctx, c.explicit, searchHost(c.env, c.exists...))
			if err != nil || got != c.want {
				t.Fatalf("Find = %q, %v; want %q", got, err, c.want)
			}
		})
	}

	if _, err := Find(ctx, "/nope.yaml", searchHost(nil)); err == nil {
		t.Error("--config のファイルが無ければエラー")
	}
	_, err := Find(ctx, "", searchHost(wsl))
	if err == nil || !strings.Contains(err.Error(), win) {
		t.Errorf("見つからないときは探した場所を出す: %v", err)
	}
}
