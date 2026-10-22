// Package config は設定ファイルの読み込み・正規化・名前解決・検証を行う。
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const defaultBind = "127.0.0.1"

// Forward はローカルポートから宛先への転送1本。
type Forward struct {
	Bind       string
	LocalPort  int
	RemoteHost string
	RemotePort int
}

// LocalAddr は待ち受けアドレスを host:port の形で返す。
func (f Forward) LocalAddr() string {
	return net.JoinHostPort(f.Bind, strconv.Itoa(f.LocalPort))
}

// ParseForward は短縮記法 ローカルポート:宛先ホスト:宛先ポート を解釈する。
func ParseForward(s string) (Forward, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return Forward{}, fmt.Errorf("forwards は ローカルポート:宛先ホスト:宛先ポート の形式で書きます: %q", s)
	}
	lp, err := parsePort(parts[0])
	if err != nil {
		return Forward{}, err
	}
	if parts[1] == "" {
		return Forward{}, fmt.Errorf("forwards の宛先ホストが空です: %q", s)
	}
	rp, err := parsePort(parts[2])
	if err != nil {
		return Forward{}, err
	}
	return Forward{Bind: defaultBind, LocalPort: lp, RemoteHost: parts[1], RemotePort: rp}, nil
}

// parseDetailedForward は詳細記法 {local, remote} を解釈する。local はポート番号か アドレス:ポート。
func parseDetailedForward(local, remote string) (Forward, error) {
	f := Forward{Bind: defaultBind}
	if local == "" {
		return Forward{}, fmt.Errorf("forwards に local がありません")
	}
	if strings.Contains(local, ":") {
		h, p, err := net.SplitHostPort(local)
		if err != nil {
			return Forward{}, fmt.Errorf("local は ポート番号 か アドレス:ポート で書きます: %q", local)
		}
		if h != "" {
			f.Bind = h
		}
		local = p
	}
	lp, err := parsePort(local)
	if err != nil {
		return Forward{}, err
	}
	h, p, err := net.SplitHostPort(remote)
	if err != nil || h == "" {
		return Forward{}, fmt.Errorf("remote は 宛先ホスト:宛先ポート の形式で書きます: %q", remote)
	}
	rp, err := parsePort(p)
	if err != nil {
		return Forward{}, err
	}
	f.LocalPort, f.RemoteHost, f.RemotePort = lp, h, rp
	return f, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("ポート番号は 1〜65535 の整数で書きます: %q", s)
	}
	return n, nil
}
