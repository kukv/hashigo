package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type validator struct {
	c        *Config
	goos     string
	problems []Problem
}

type kv struct{ k, v string }

func validate(c *Config, goos string) []Problem {
	v := &validator{c: c, goos: goos}
	v.names()
	v.settings()
	v.hosts()
	v.tunnels()
	v.groups()
	v.ports()
	return v.problems
}

func (v *validator) add(src Source, format string, args ...any) {
	v.problems = append(v.problems, Problem{File: src.File, path: src.Path, Msg: fmt.Sprintf(format, args...)})
}

func (v *validator) names() {
	root := v.c.Envs[""]
	for name, e := range v.c.Envs {
		if name == "" {
			continue
		}
		if strings.Contains(name, ".") {
			v.add(e.Src, "名前に . は使えません: %q", name)
		}
		if t := root.Tunnels[name]; t != nil {
			v.add(t.Src, "トンネル %q は環境名と衝突しています", name)
		}
		if g := v.c.Groups[name]; g != nil {
			v.add(g.Src, "グループ %q は環境名と衝突しています", name)
		}
	}
	for _, t := range v.c.Tunnels() {
		if strings.Contains(t.Name, ".") {
			v.add(t.Src, "名前に . は使えません: %q", t.Name)
		}
	}
	for name, g := range v.c.Groups {
		if strings.Contains(name, ".") {
			v.add(g.Src, "名前に . は使えません: %q", name)
		}
		if root.Tunnels[name] != nil {
			v.add(g.Src, "グループ %q はトンネル名と衝突しています", name)
		}
	}
}

func (v *validator) settings() {
	if m := v.c.WSLMode; m != "" && !slices.Contains([]string{"auto", "mirrored", "relay", "native"}, m) {
		v.add(v.c.wslSrc.child("mode"), "wsl.mode は auto / mirrored / relay / native のいずれかです: %q", m)
	}
	v.checkSettings(v.c.defaultsSrc, v.c.Defaults)
	for _, e := range v.c.Envs {
		if e.Name != "" {
			v.checkSettings(e.Src, e.Settings)
		}
	}
	for _, t := range v.c.Tunnels() {
		v.checkSettings(t.Src, t.Settings)
	}
}

func (v *validator) checkSettings(src Source, s Settings) {
	if s.Keepalive != "" {
		if d, err := time.ParseDuration(s.Keepalive); err != nil || d < time.Second {
			v.add(src.child("keepalive"), "keepalive は 30s のような 1 秒以上の時間で書きます: %q", s.Keepalive)
		}
	}
	if v.goos == "windows" {
		if s.SSHConfig == "wsl" {
			v.add(src.child("ssh_config"), "Windows ネイティブでは ssh_config: wsl は使えません。パスで指定します")
		}
		if s.AWSConfig == "wsl" {
			v.add(src.child("aws_config"), "Windows ネイティブでは aws_config: wsl は使えません。パスで指定します")
		}
	}
}

func (v *validator) hosts() {
	for _, e := range v.c.Envs {
		for _, h := range e.Hosts {
			if h.Host == "" {
				v.add(h.Src, "host がありません")
			}
			if h.Via == "" {
				continue
			}
			if v.c.LookupHost(h.Env, h.Via) == nil {
				v.add(h.Src.child("via"), "via のホスト %q が見つかりません", h.Via)
				continue
			}
			var ce *cycleError
			if _, err := v.c.hostChain(h); errors.As(err, &ce) {
				v.add(h.Src.child("via"), "%v", err)
			}
		}
	}
}

func (v *validator) tunnels() {
	for _, t := range v.c.Tunnels() {
		s := t.Settings
		switch t.Type {
		case "ssh":
			if (t.Host == "") == (t.Via == "") {
				v.add(t.Src, "host か via のどちらか一方を書きます")
			}
			if t.Via != "" && v.c.LookupHost(t.Env, t.Via) == nil {
				v.add(t.Src.child("via"), "via のホスト %q が見つかりません", t.Via)
			}
			for _, x := range []kv{{"cluster", t.Cluster}, {"service", t.Service}, {"container", t.Container},
				{"aws", s.AWS}, {"aws_config", s.AWSConfig}, {"profile", s.Profile}, {"region", s.Region}} {
				if x.v != "" {
					v.add(t.Src.child(x.k), "%s は type: ecs でだけ使えます", x.k)
				}
			}
		case "ecs":
			if t.Host != "" || t.Via != "" {
				v.add(t.Src, "type: ecs では host / via を書けません")
			}
			for _, x := range []kv{{"cluster", t.Cluster}, {"service", t.Service}, {"container", t.Container}} {
				if x.v == "" {
					v.add(t.Src, "%s がありません", x.k)
				}
			}
			for _, x := range []kv{{"ssh", s.SSH}, {"ssh_config", s.SSHConfig}, {"keepalive", s.Keepalive}} {
				if x.v != "" {
					v.add(t.Src.child(x.k), "%s は type: ssh でだけ使えます", x.k)
				}
			}
		default:
			v.add(t.Src.child("type"), "type は ssh か ecs です: %q", t.Type)
		}
	}
}

func (v *validator) groups() {
	for _, g := range v.c.Groups {
		for i, m := range g.Members {
			if _, ok := v.c.lookupTarget(m, false); !ok {
				v.add(g.Src.child(i), "グループのメンバー %q が見つかりません（グループの入れ子はできません）", m)
			}
		}
	}
}

func (v *validator) ports() {
	owner := map[int]string{}
	for _, t := range v.c.Tunnels() {
		for _, f := range t.Forwards {
			if prev, ok := owner[f.LocalPort]; ok {
				v.add(t.Src.child("forwards"), "ローカルポート %d は %s と重複しています", f.LocalPort, prev)
				continue
			}
			owner[f.LocalPort] = t.ID()
		}
	}
}
