package ssh

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
)

func TestArgs(t *testing.T) {
	d := New(Options{
		Tunnel: "dev.db",
		Bin:    "/usr/bin/ssh",
		Route:  config.Route{Dest: "admin@10.0.1.10", Jumps: []string{"ec2-user@bastion", "10.0.0.2"}},
		Forwards: []config.Forward{
			{Bind: "127.0.0.1", LocalPort: 15432, RemoteHost: "db.internal", RemotePort: 5432},
			{Bind: "::1", LocalPort: 16379, RemoteHost: "fd00::1", RemotePort: 6379},
		},
		Keepalive: 30 * time.Second,
	})
	want := "-N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o BatchMode=yes " +
		"-J ec2-user@bastion,10.0.0.2 -L 127.0.0.1:15432:db.internal:5432 -L [::1]:16379:[fd00::1]:6379 admin@10.0.1.10"
	specs := d.Preview()
	if len(specs) != 1 {
		t.Fatalf("spec は1つ: %d", len(specs))
	}
	if got := strings.Join(specs[0].Args, " "); got != want {
		t.Errorf("args =\n%s\nwant\n%s", got, want)
	}
	if specs[0].Path != "/usr/bin/ssh" || specs[0].Tunnel != "dev.db" {
		t.Errorf("spec = %+v", specs[0])
	}
	wantPorts := []driver.Listen{{Addr: "127.0.0.1", Port: 15432}, {Addr: "::1", Port: 16379}}
	if !reflect.DeepEqual(specs[0].Ports, wantPorts) {
		t.Errorf("ports = %+v", specs[0].Ports)
	}
	prepared, err := d.Prepare(context.Background())
	if err != nil || !reflect.DeepEqual(prepared, specs) {
		t.Errorf("Prepare は Preview と同じ: %+v, %v", prepared, err)
	}
}

func TestArgsNoJumpAndKeepalive(t *testing.T) {
	d := New(Options{
		Tunnel: "web", Bin: "ssh",
		Route:     config.Route{Dest: "app1"},
		Forwards:  []config.Forward{{Bind: "127.0.0.1", LocalPort: 8080, RemoteHost: "localhost", RemotePort: 80}},
		Keepalive: 1500 * time.Millisecond,
	})
	got := strings.Join(d.Preview()[0].Args, " ")
	if strings.Contains(got, "-J") || !strings.Contains(got, "ServerAliveInterval=2") || !strings.HasSuffix(got, " app1") {
		t.Errorf("args = %s", got)
	}
}
