package config

import (
	"fmt"
	"slices"
	"strings"
)

// LookupHost は環境内のホストを探し、無ければルート環境を探す。
func (c *Config) LookupHost(env, name string) *Host {
	if e := c.Envs[env]; e != nil {
		if h := e.Hosts[name]; h != nil {
			return h
		}
	}
	if env != "" {
		return c.Envs[""].Hosts[name]
	}
	return nil
}

type cycleError struct{ path []string }

func (e *cycleError) Error() string {
	return "via が循環しています: " + strings.Join(e.path, " → ")
}

// hostChain は h から via をたどったホストの列 [h, h の via, ...] を返す。
func (c *Config) hostChain(h *Host) ([]*Host, error) {
	chain := []*Host{h}
	for cur := h; cur.Via != ""; {
		next := c.LookupHost(cur.Env, cur.Via)
		if next == nil {
			return nil, fmt.Errorf("via のホスト %q が見つかりません", cur.Via)
		}
		if slices.Contains(chain, next) {
			var ids []string
			for _, x := range chain {
				ids = append(ids, x.ID())
			}
			return nil, &cycleError{path: append(ids, next.ID())}
		}
		chain = append(chain, next)
		cur = next
	}
	return chain, nil
}

// Route は SSH の接続経路。Dest は最終接続先、Jumps は -J に渡す踏み台（手元に近い順）。
type Route struct {
	Dest  string
	Jumps []string
}

func sshAddr(h *Host) string {
	if h.User == "" {
		return h.Host
	}
	return h.User + "@" + h.Host
}

// Route はトンネルの接続経路を組み立てる。
func (c *Config) Route(t *Tunnel) (Route, error) {
	name := t.Via
	if name == "" {
		name = t.Host
	}
	h := c.LookupHost(t.Env, name)
	if h == nil {
		if t.Via != "" {
			return Route{}, fmt.Errorf("%s: via のホスト %q が見つかりません", t.ID(), t.Via)
		}
		return Route{Dest: t.Host}, nil
	}
	chain, err := c.hostChain(h)
	if err != nil {
		return Route{}, fmt.Errorf("%s: %w", t.ID(), err)
	}
	r := Route{Dest: sshAddr(chain[0])}
	for i := len(chain) - 1; i >= 1; i-- {
		r.Jumps = append(r.Jumps, sshAddr(chain[i]))
	}
	return r, nil
}

// lookupTarget は 環境.名前 → 環境名 → グループ名（allowGroup のとき）→ ルート環境のトンネル名 の順に照合する。
func (c *Config) lookupTarget(name string, allowGroup bool) ([]*Tunnel, bool) {
	if env, tn, ok := strings.Cut(name, "."); ok {
		if e := c.Envs[env]; e != nil && env != "" {
			if t := e.Tunnels[tn]; t != nil {
				return []*Tunnel{t}, true
			}
		}
		return nil, false
	}
	if e := c.Envs[name]; e != nil && name != "" {
		return e.sortedTunnels(), true
	}
	if allowGroup {
		if g := c.Groups[name]; g != nil {
			var ts []*Tunnel
			for _, m := range g.Members {
				got, ok := c.lookupTarget(m, false)
				if !ok {
					return nil, false
				}
				ts = append(ts, got...)
			}
			return ts, true
		}
	}
	if t := c.Envs[""].Tunnels[name]; t != nil {
		return []*Tunnel{t}, true
	}
	return nil, false
}

// Select はコマンドの引数で指定された対象をトンネルの一覧にする。重複は除き、指定順を保つ。
func (c *Config) Select(targets []string) ([]*Tunnel, error) {
	var out []*Tunnel
	seen := map[*Tunnel]bool{}
	for _, name := range targets {
		ts, ok := c.lookupTarget(name, true)
		if !ok {
			return nil, fmt.Errorf("%q に一致する環境・グループ・トンネルがありません", name)
		}
		for _, t := range ts {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out, nil
}
