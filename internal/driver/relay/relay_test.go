package relay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/driver/ssh"
)

func inner() *ssh.Driver {
	return ssh.New(ssh.Options{
		Tunnel: "dev.db", Bin: "ssh.exe", Route: config.Route{Dest: "ap"}, Keepalive: 30 * time.Second,
		Forwards: []config.Forward{
			{Bind: "127.0.0.1", LocalPort: 15432, RemoteHost: "db", RemotePort: 5432},
			{Bind: "127.0.0.1", LocalPort: 16379, RemoteHost: "redis", RemotePort: 6379},
		},
	})
}

func TestPrepare(t *testing.T) {
	calls := 0
	d := Wrap(inner(), Options{Socat: "/usr/bin/socat", Gateway: func(context.Context) (string, error) {
		calls++
		return "172.29.16.1", nil
	}})
	specs, err := d.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 {
		t.Fatalf("ssh 1つ + forward ごとの socat 2つ: %d", len(specs))
	}
	if got := strings.Join(specs[0].Args, " "); !strings.Contains(got, "-L 172.29.16.1:15432:db:5432 -L 172.29.16.1:16379:redis:6379") {
		t.Errorf("ssh.exe はゲートウェイ IP で待ち受ける: %s", got)
	}
	if specs[0].Ports[0] != (driver.Listen{Addr: "172.29.16.1", Port: 15432}) {
		t.Errorf("ssh のヘルスチェック先 = %+v", specs[0].Ports)
	}
	if got := strings.Join(specs[1].Args, " "); got != "TCP-LISTEN:15432,bind=127.0.0.1,reuseaddr,fork TCP:172.29.16.1:15432" {
		t.Errorf("socat = %s", got)
	}
	if specs[2].Path != "/usr/bin/socat" || specs[2].Tunnel != "dev.db" || specs[2].Ports[0] != (driver.Listen{Addr: "127.0.0.1", Port: 16379}) {
		t.Errorf("2つ目の socat = %+v", specs[2])
	}
	if _, err := d.Prepare(context.Background()); err != nil || calls != 2 {
		t.Errorf("ゲートウェイ IP は Prepare のたびに取り直す: calls=%d", calls)
	}
}

func TestPreview(t *testing.T) {
	d := Wrap(inner(), Options{Socat: "socat", Gateway: func(context.Context) (string, error) {
		t.Fatal("Preview は外部呼び出しをしない")
		return "", nil
	}})
	specs := d.Preview()
	if len(specs) != 3 || !strings.Contains(strings.Join(specs[1].Args, " "), "TCP:<ゲートウェイIP>:15432") {
		t.Errorf("Preview = %+v", specs)
	}
}

func TestIPv6Bind(t *testing.T) {
	in := ssh.New(ssh.Options{
		Tunnel: "x", Bin: "ssh.exe", Route: config.Route{Dest: "h"},
		Forwards: []config.Forward{{Bind: "::1", LocalPort: 8080, RemoteHost: "web", RemotePort: 80}},
	})
	specs, _ := Wrap(in, Options{Socat: "socat", Gateway: func(context.Context) (string, error) { return "172.29.16.1", nil }}).Prepare(context.Background())
	if got := specs[1].Args[0]; got != "TCP6-LISTEN:8080,bind=[::1],reuseaddr,fork" {
		t.Errorf("IPv6 の待ち受け = %s", got)
	}
}

func TestGatewayError(t *testing.T) {
	d := Wrap(inner(), Options{Socat: "socat", Gateway: func(context.Context) (string, error) { return "", errors.New("no route") }})
	if _, err := d.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "no route") {
		t.Fatalf("err = %v", err)
	}
	if d.Classify(errors.New("x"), nil).Fatal {
		t.Error("Classify は中身のドライバに任せる（ssh は常に再接続）")
	}
}
