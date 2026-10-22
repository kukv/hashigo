package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

type loader struct {
	cfg         *Config
	data        map[string][]byte // ファイルごとの内容（位置の特定に使う）
	problems    Problems
	hasDefaults bool
	hasWSL      bool
}

// Load は設定ファイルを読み込み、正規化して返す。問題があれば Problems を返す。
// goos は検証に使う OS 名（runtime.GOOS と同じ値）。
func Load(path, goos string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	l := &loader{cfg: newConfig(), data: map[string][]byte{}}
	l.loadFile(abs, Source{})
	if len(l.problems) > 0 {
		return nil, l.finish()
	}
	return l.cfg, nil
}

func (l *loader) problemf(src Source, format string, args ...any) {
	l.problems = append(l.problems, Problem{File: src.File, path: src.Path, Msg: fmt.Sprintf(format, args...)})
}

func (l *loader) finish() Problems {
	for i := range l.problems {
		p := &l.problems[i]
		if p.Line == 0 && p.path != nil {
			p.Line, p.Col = locate(l.data[p.File], p.path)
		}
	}
	sortProblems(l.problems)
	return l.problems
}

func (l *loader) loadFile(path string, from Source) {
	data, err := os.ReadFile(path)
	if err != nil {
		l.problemf(from, "設定ファイルを読めません: %v", err)
		return
	}
	l.cfg.Files = append(l.cfg.Files, path)
	l.data[path] = data

	var r rawFile
	if err := yaml.UnmarshalWithOptions(data, &r, yaml.DisallowUnknownField()); err != nil {
		l.problems = append(l.problems, syntaxProblem(path, err))
		return
	}
	l.merge(path, &r)
}

func (l *loader) merge(file string, r *rawFile) {
	if r.Defaults != nil {
		src := Source{File: file, Path: []any{"defaults"}}
		if l.hasDefaults {
			l.problemf(src, "defaults は %s でも定義されています", l.cfg.defaultsSrc.File)
		} else {
			l.hasDefaults = true
			l.cfg.defaultsSrc = src
			l.cfg.Defaults = l.settings(src, *r.Defaults)
			if r.Defaults.Profile != "" || r.Defaults.Region != "" {
				l.problemf(src, "defaults には profile / region を書けません。aws.profile / aws.region を使います")
			}
		}
	}
	if r.WSL != nil {
		src := Source{File: file, Path: []any{"wsl"}}
		if l.hasWSL {
			l.problemf(src, "wsl は %s でも定義されています", l.cfg.wslSrc.File)
		} else {
			l.hasWSL = true
			l.cfg.wslSrc = src
			l.cfg.WSLMode = r.WSL.Mode
		}
	}

	root := l.cfg.Envs[""]
	for name, h := range r.Hosts {
		l.addHost(root, name, h, Source{File: file, Path: []any{"hosts", name}})
	}
	for name, t := range r.Tunnels {
		l.addTunnel(root, name, t, Source{File: file, Path: []any{"tunnels", name}})
	}
	for name, e := range r.Environments {
		src := Source{File: file, Path: []any{"environments", name}}
		if old, ok := l.cfg.Envs[name]; ok {
			l.problemf(src, "環境 %q は %s でも定義されています", name, old.Src.File)
			continue
		}
		env := &Env{
			Name:      name,
			Protected: e.Protected,
			Settings:  l.settings(src, e.SettingsYAML),
			Hosts:     map[string]*Host{},
			Tunnels:   map[string]*Tunnel{},
			Src:       src,
		}
		l.cfg.Envs[name] = env
		for hn, h := range e.Hosts {
			l.addHost(env, hn, h, src.child("hosts", hn))
		}
		for tn, t := range e.Tunnels {
			l.addTunnel(env, tn, t, src.child("tunnels", tn))
		}
	}
	for name, members := range r.Groups {
		src := Source{File: file, Path: []any{"groups", name}}
		if old, ok := l.cfg.Groups[name]; ok {
			l.problemf(src, "グループ %q は %s でも定義されています", name, old.Src.File)
			continue
		}
		l.cfg.Groups[name] = &Group{Name: name, Members: members, Src: src}
	}
}

func (l *loader) settings(src Source, r SettingsYAML) Settings {
	s := Settings{
		SSH:       r.SSH,
		SSHConfig: r.SSHConfig,
		AWSConfig: r.AWSConfig,
		Keepalive: r.Keepalive,
		Reconnect: r.Reconnect,
		Profile:   r.Profile,
		Region:    r.Region,
	}
	if a := r.AWS; a != nil {
		if a.Err != nil {
			l.problemf(src.child("aws"), "%v", a.Err)
		}
		s.AWS = a.Path
		if a.Profile != "" {
			if s.Profile != "" {
				l.problemf(src.child("aws"), "profile と aws.profile は同時に書けません")
			} else {
				s.Profile = a.Profile
			}
		}
		if a.Region != "" {
			if s.Region != "" {
				l.problemf(src.child("aws"), "region と aws.region は同時に書けません")
			} else {
				s.Region = a.Region
			}
		}
	}
	return s
}

func (l *loader) addHost(env *Env, name string, r rawHost, src Source) {
	if old, ok := env.Hosts[name]; ok {
		l.problemf(src, "ホスト %q は %s でも定義されています", name, old.Src.File)
		return
	}
	env.Hosts[name] = &Host{Name: name, Env: env.Name, Host: r.Host, User: r.User, Via: r.Via, Src: src}
}

func (l *loader) addTunnel(env *Env, name string, r rawTunnel, src Source) {
	if old, ok := env.Tunnels[name]; ok {
		l.problemf(src, "トンネル %q は %s でも定義されています", name, old.Src.File)
		return
	}
	t := &Tunnel{
		Name: name, Env: env.Name, Type: r.Type,
		Host: r.Host, Via: r.Via,
		Cluster: r.Cluster, Service: r.Service, Container: r.Container,
		Hostname: r.Hostname,
		Settings: l.settings(src, r.SettingsYAML),
		Src:      src,
	}
	if t.Type == "" {
		t.Type = "ssh"
	}
	if len(r.Forwards) == 0 {
		l.problemf(src, "forwards がありません")
	}
	for i, f := range r.Forwards {
		if f.Err != nil {
			l.problemf(src.child("forwards", i), "%v", f.Err)
			continue
		}
		t.Forwards = append(t.Forwards, f.Forward)
	}
	env.Tunnels[name] = t
}
