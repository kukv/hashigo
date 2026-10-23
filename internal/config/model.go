package config

import "sort"

// Settings は defaults / 環境 / トンネルで上書きできる設定。空文字と nil は未指定を表す。
type Settings struct {
	SSH       string
	SSHConfig string
	AWS       string // aws バイナリ
	AWSConfig string
	Keepalive string
	Reconnect *bool
	Profile   string
	Region    string
}

// Source は定義元のファイルと YAML 上のパス。Path の要素は string（キー）か int（添字）。
type Source struct {
	File string
	Path []any
}

func (s Source) child(keys ...any) Source {
	p := append(append([]any{}, s.Path...), keys...)
	return Source{File: s.File, Path: p}
}

// Host は hosts の1件。
type Host struct {
	Name, Env       string
	Host, User, Via string
	Src             Source
}

// ID は外から参照するときの名前（環境.名前、ルート環境なら名前）。
func (h *Host) ID() string { return qualify(h.Env, h.Name) }

// Tunnel は tunnels の1件。Type は "ssh" か "ecs"。
type Tunnel struct {
	Name, Env                   string
	Type                        string
	Host, Via                   string
	Cluster, Service, Container string
	Hostname                    string
	Forwards                    []Forward
	Settings                    Settings
	Src                         Source
}

// ID は外から参照するときの名前（環境.名前、ルート環境なら名前）。
func (t *Tunnel) ID() string { return qualify(t.Env, t.Name) }

func qualify(env, name string) string {
	if env == "" {
		return name
	}
	return env + "." + name
}

// Env は環境。Name が "" のものはルート環境。
type Env struct {
	Name      string
	Protected bool
	Settings  Settings
	Hosts     map[string]*Host
	Tunnels   map[string]*Tunnel
	Src       Source
}

func (e *Env) sortedTunnels() []*Tunnel {
	ts := make([]*Tunnel, 0, len(e.Tunnels))
	for _, t := range e.Tunnels {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	return ts
}

// Group は groups の1件。
type Group struct {
	Name    string
	Members []string
	Src     Source
}

// Config は正規化した設定全体。
type Config struct {
	Files    []string // 読み込んだファイル（先頭がメイン）
	Defaults Settings
	WSLMode  string
	Envs     map[string]*Env // キー "" はルート環境
	Groups   map[string]*Group

	defaultsSrc Source
	wslSrc      Source
}

func newConfig() *Config {
	return &Config{
		Envs:   map[string]*Env{"": {Hosts: map[string]*Host{}, Tunnels: map[string]*Tunnel{}}},
		Groups: map[string]*Group{},
	}
}

// Tunnels は全環境のトンネルを ID 順に返す。
func (c *Config) Tunnels() []*Tunnel {
	var ts []*Tunnel
	for _, e := range c.Envs {
		ts = append(ts, e.sortedTunnels()...)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID() < ts[j].ID() })
	return ts
}
