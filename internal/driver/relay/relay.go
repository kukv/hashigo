// Package relay は、NAT モードの WSL で ssh.exe の待ち受けを WSL 側へ中継するドライバ。
// ssh.exe を Windows 側の WSL 用仮想 NIC の IP で待ち受けさせ（0.0.0.0 にはしないので LAN に公開されない）、
// WSL 側の待ち受けアドレスから socat で中継する。
package relay

import (
	"context"
	"fmt"
	"strings"

	"github.com/kukv/hashigo/internal/driver"
)

// Options は relay ドライバの入力。
type Options struct {
	Socat   string                                    // socat のパス
	Gateway func(ctx context.Context) (string, error) // WSL から見た Windows 側の IP。接続のたびに呼ぶ
}

// Driver は中身のドライバの Spec に、forward ごとの socat の補助スロットを足す。
type Driver struct {
	inner driver.Rebinder
	o     Options
}

// Wrap は inner を relay モードにする。
func Wrap(inner driver.Rebinder, o Options) *Driver { return &Driver{inner: inner, o: o} }

const previewGateway = "<ゲートウェイIP>"

// Preview はゲートウェイ IP をプレースホルダーにして返す。外部呼び出しはしない。
func (d *Driver) Preview() []driver.ProcessSpec {
	return d.wrap(d.inner.PreviewAt(previewGateway), previewGateway)
}

// Prepare はゲートウェイ IP を取り直し、ssh の待ち受けをその IP にして、socat を足す。
// この IP は Windows の再起動で変わるため、接続のたびに取り直す。
func (d *Driver) Prepare(ctx context.Context) ([]driver.ProcessSpec, error) {
	gw, err := d.o.Gateway(ctx)
	if err != nil {
		return nil, fmt.Errorf("relay の中継先（WSL から見た Windows の IP）を取得できません: %w", err)
	}
	specs, err := d.inner.PrepareAt(ctx, gw)
	if err != nil {
		return nil, err
	}
	return d.wrap(specs, gw), nil
}

// Classify は中身のドライバに任せる。
func (d *Driver) Classify(err error, tail []byte) driver.Exit { return d.inner.Classify(err, tail) }

// wrap は specs の後ろに、ローカルポートごとの socat を足す。並びは毎回同じになる。
func (d *Driver) wrap(specs []driver.ProcessSpec, gw string) []driver.ProcessSpec {
	orig := d.inner.Preview() // 待ち受けを差し替える前の、WSL 側の待ち受けアドレス
	out := append([]driver.ProcessSpec(nil), specs...)
	for i, sp := range specs {
		for j, p := range sp.Ports {
			local := orig[i].Ports[j]
			out = append(out, driver.ProcessSpec{
				Tunnel: sp.Tunnel,
				Path:   d.o.Socat,
				Args:   []string{listenArg(local), fmt.Sprintf("TCP:%s:%d", gw, p.Port)},
				Ports:  []driver.Listen{local},
			})
		}
	}
	return out
}

func listenArg(l driver.Listen) string {
	if strings.Contains(l.Addr, ":") {
		return fmt.Sprintf("TCP6-LISTEN:%d,bind=[%s],reuseaddr,fork", l.Port, l.Addr)
	}
	return fmt.Sprintf("TCP-LISTEN:%d,bind=%s,reuseaddr,fork", l.Port, l.Addr)
}
