package config

import (
	"errors"
	"fmt"
	"regexp"
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
			v.noDash(h.Src, "host", h.Host)
			v.noDash(h.Src, "user", h.User)
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

// noDash は ssh のオプションとして解釈される - 始まりの値を拒否する。
func (v *validator) noDash(src Source, key, val string) {
	if strings.HasPrefix(val, "-") {
		v.add(src.child(key), "%s は - で始められません: %q", key, val)
	}
}

// instanceID は SSM の対象にできる ID（EC2 インスタンス i-...、マネージドインスタンス mi-...）。
var instanceID = regexp.MustCompile(`^m?i-[0-9a-f]+$`)

func (v *validator) tunnels() {
	for _, t := range v.c.Tunnels() {
		s := t.Settings
		// only は、その type でだけ使えるキーが、ほかの type に書かれていないかを確かめる
		only := func(where string, xs ...kv) {
			for _, x := range xs {
				if x.v != "" {
					v.add(t.Src.child(x.k), "%s は %s でだけ使えます", x.k, where)
				}
			}
		}
		ecsKeys := []kv{{"cluster", t.Cluster}, {"service", t.Service}, {"container", t.Container}}
		ssmKeys := []kv{{"target", t.Target}}
		if t.TargetTag != nil {
			ssmKeys = append(ssmKeys, kv{"target_tag", "set"})
		}
		switch t.Type {
		case "ssh":
			v.noDash(t.Src, "host", t.Host)
			if (t.Host == "") == (t.Via == "") {
				v.add(t.Src, "host か via のどちらか一方を書きます")
			}
			if t.Via != "" && v.c.LookupHost(t.Env, t.Via) == nil {
				v.add(t.Src.child("via"), "via のホスト %q が見つかりません", t.Via)
			}
			only("type: ecs", ecsKeys...)
			only("type: ssm", ssmKeys...)
			only("type: ecs / ssm",
				kv{"aws", s.AWS}, kv{"aws_config", s.AWSConfig}, kv{"profile", s.Profile}, kv{"region", s.Region})
		case "ecs", "ssm":
			if t.Host != "" || t.Via != "" {
				v.add(t.Src, "type: %s では host / via を書けません", t.Type)
			}
			if t.Type == "ecs" {
				for _, x := range ecsKeys {
					if x.v == "" {
						v.add(t.Src, "%s がありません", x.k)
					}
				}
				only("type: ssm", ssmKeys...)
			} else {
				v.ssmTarget(t)
				only("type: ecs", ecsKeys...)
			}
			only("type: ssh", kv{"ssh", s.SSH}, kv{"ssh_config", s.SSHConfig}, kv{"keepalive", s.Keepalive})
			for i, f := range t.Forwards {
				if f.Bind != defaultBind {
					v.add(t.Src.child("forwards", i), "type: %s の forwards には待ち受けアドレスを書けません（SSM は 127.0.0.1 で待ち受けます）: %q", t.Type, f.Bind)
				}
			}
		default:
			v.add(t.Src.child("type"), "type は ssh / ecs / ssm のいずれかです: %q", t.Type)
		}
	}
}

// ssmTarget は、target と target_tag のどちらか一方が正しく書かれているかを確かめる。
func (v *validator) ssmTarget(t *Tunnel) {
	if (t.Target == "") == (t.TargetTag == nil) {
		v.add(t.Src, "target か target_tag のどちらか一方を書きます")
	}
	if t.Target != "" && !instanceID.MatchString(t.Target) {
		v.add(t.Src.child("target"), "target はインスタンス ID（i-0123456789abcdef0 など）で書きます: %q", t.Target)
	}
	if t.TargetTag != nil && len(t.TargetTag) == 0 {
		v.add(t.Src.child("target_tag"), "target_tag にタグがありません")
	}
	for k, val := range t.TargetTag {
		if k == "" || val == "" {
			v.add(t.Src.child("target_tag"), "target_tag のタグ名と値は空にできません: %q: %q", k, val)
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
