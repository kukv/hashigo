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
	Tunnel    string
	Bin       string
	Route     config.Route
	Forwards  []config.Forward
	Keepalive time.Duration
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

func (d *Driver) args() []string {
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
	if len(d.o.Route.Jumps) > 0 {
		args = append(args, "-J", strings.Join(d.o.Route.Jumps, ","))
	}
	for _, f := range d.o.Forwards {
		args = append(args, "-L", fmt.Sprintf("%s:%d:%s:%d", bracket(f.Bind), f.LocalPort, bracket(f.RemoteHost), f.RemotePort))
	}
	return append(args, "--", d.o.Route.Dest)
}

// Preview は起動する ssh プロセスを返す。
func (d *Driver) Preview() []driver.ProcessSpec {
	ports := make([]driver.Listen, len(d.o.Forwards))
	for i, f := range d.o.Forwards {
		ports[i] = driver.Listen{Addr: f.Bind, Port: f.LocalPort}
	}
	return []driver.ProcessSpec{{Tunnel: d.o.Tunnel, Path: d.o.Bin, Args: d.args(), Ports: ports}}
}

// Prepare は Preview と同じものを返す。
func (d *Driver) Prepare(context.Context) ([]driver.ProcessSpec, error) {
	return d.Preview(), nil
}
