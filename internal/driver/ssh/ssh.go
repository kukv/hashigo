// Package ssh は ssh / ssh.exe でポートフォワードを張るドライバ。
package ssh

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
)

// Options は SSH ドライバの入力。
type Options struct {
	Tunnel     string
	Bin        string
	Route      config.Route
	Forwards   []config.Forward
	Keepalive  time.Duration
	ConfigFile string // -F に渡す ssh config。空なら付けない
}

// Driver は1トンネルを1つの ssh プロセスで張る。
type Driver struct{ o Options }

// New は SSH ドライバを作る。
func New(o Options) *Driver { return &Driver{o: o} }

func bracket(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// args は ssh の引数を組み立てる。bind が空でなければ、全 forward の待ち受けアドレスをそれにする。
func (d *Driver) args(bind string) []string {
	ka := int(math.Ceil(d.o.Keepalive.Seconds()))
	if ka < 1 {
		ka = 1
	}
	args := []string{
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=" + strconv.Itoa(ka),
		"-o", "ServerAliveCountMax=3",
		"-o", "BatchMode=yes",
	}
	if d.o.ConfigFile != "" {
		args = append(args, "-F", d.o.ConfigFile)
	}
	if len(d.o.Route.Jumps) > 0 {
		args = append(args, "-J", strings.Join(d.o.Route.Jumps, ","))
	}
	for _, f := range d.o.Forwards {
		args = append(args, "-L", fmt.Sprintf("%s:%d:%s:%d", bracket(bindOf(f, bind)), f.LocalPort, bracket(f.RemoteHost), f.RemotePort))
	}
	return append(args, "--", d.o.Route.Dest)
}

func bindOf(f config.Forward, bind string) string {
	if bind != "" {
		return bind
	}
	return f.Bind
}

// PreviewAt は待ち受けアドレスを bind にした ssh プロセスを返す。bind が空なら設定のまま。
func (d *Driver) PreviewAt(bind string) []driver.ProcessSpec {
	ports := make([]driver.Listen, len(d.o.Forwards))
	for i, f := range d.o.Forwards {
		ports[i] = driver.Listen{Addr: bindOf(f, bind), Port: f.LocalPort}
	}
	return []driver.ProcessSpec{{Tunnel: d.o.Tunnel, Path: d.o.Bin, Args: d.args(bind), Ports: ports}}
}

// Preview は起動する ssh プロセスを返す。
func (d *Driver) Preview() []driver.ProcessSpec { return d.PreviewAt("") }

// PrepareAt は PreviewAt と同じものを返す。
func (d *Driver) PrepareAt(_ context.Context, bind string) ([]driver.ProcessSpec, error) {
	return d.PreviewAt(bind), nil
}

// Prepare は Preview と同じものを返す。
func (d *Driver) Prepare(ctx context.Context) ([]driver.ProcessSpec, error) {
	return d.PrepareAt(ctx, "")
}

// Classify は常に再接続とする。ssh の終了コードからは、直る問題かどうかを区別できない。
func (d *Driver) Classify(error, []byte) driver.Exit { return driver.Exit{} }
