package ssm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
)

type fakeRunner map[string]string

func (f fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	out, ok := f[key]
	if !ok {
		return nil, fmt.Errorf("想定外の呼び出し: %s", key)
	}
	if strings.HasPrefix(out, "ERR:") {
		return nil, errors.New(strings.TrimPrefix(out, "ERR:"))
	}
	return []byte(out), nil
}

const describeKey = `aws ec2 describe-instances --filters [{"Name":"tag:Env","Values":["stg"]},{"Name":"tag:Name","Values":["stg-bastion"]},{"Name":"instance-state-name","Values":["running"]}] ` +
	`--query Reservations[].Instances[].InstanceId --output text --profile stg-admin --region ap-northeast-1`

var forwards = []config.Forward{
	{Bind: "127.0.0.1", LocalPort: 15433, RemoteHost: "db.example.internal", RemotePort: 5432},
	{Bind: "127.0.0.1", LocalPort: 16379, RemoteHost: "cache.example.internal", RemotePort: 6379},
}

func newDriver(run fakeRunner, target string, tags map[string]string) *Driver {
	return New(Options{
		Tunnel: "stg.db", Bin: "aws", Run: run, Env: []string{"AWS_CONFIG_FILE=/x"},
		Profile: "stg-admin", Region: "ap-northeast-1",
		Target: target, TargetTag: tags, Forwards: forwards,
	})
}

func TestPrepareTarget(t *testing.T) {
	d := newDriver(nil, "i-0123456789abcdef0", nil)
	specs, err := d.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("forward ごとに1プロセス: %d", len(specs))
	}
	want := []string{
		"ssm", "start-session", "--profile", "stg-admin", "--region", "ap-northeast-1",
		"--target", "i-0123456789abcdef0",
		"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
		"--parameters", `{"host":["db.example.internal"],"portNumber":["5432"],"localPortNumber":["15433"]}`,
	}
	if !reflect.DeepEqual(specs[0].Args, want) {
		t.Errorf("args =\n%q\nwant\n%q", specs[0].Args, want)
	}
	if specs[1].Ports[0] != (driver.Listen{Addr: "127.0.0.1", Port: 16379}) || specs[1].Path != "aws" || specs[1].Tunnel != "stg.db" {
		t.Errorf("2つ目 = %+v", specs[1])
	}
	if !reflect.DeepEqual(specs[0].Env, []string{"AWS_CONFIG_FILE=/x"}) {
		t.Errorf("env = %v", specs[0].Env)
	}
	if !reflect.DeepEqual(d.Preview(), specs) {
		t.Error("ID を直接書いた場合は Preview と Prepare が同じ")
	}
	if note, rs := d.Resolvers(); note != "" || rs != nil {
		t.Errorf("ID を直接書いた場合は解決のコマンドが無い: %v", rs)
	}
}

func TestParametersIsValidJSON(t *testing.T) {
	p := Parameters(config.Forward{LocalPort: 1, RemoteHost: `we"ird,host`, RemotePort: 2})
	var m map[string][]string
	if err := json.Unmarshal([]byte(p), &m); err != nil {
		t.Fatalf("JSON として読めない: %s: %v", p, err)
	}
	if m["host"][0] != `we"ird,host` || m["portNumber"][0] != "2" || m["localPortNumber"][0] != "1" {
		t.Errorf("parameters = %v", m)
	}
}

func TestPrepareTargetTag(t *testing.T) {
	tags := map[string]string{"Name": "stg-bastion", "Env": "stg"}
	d := newDriver(fakeRunner{describeKey: "i-0bbb\ti-0aaa\r\n"}, "", tags)
	specs, err := d.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(specs[0].Args, " "); !strings.Contains(got, "--target i-0aaa ") {
		t.Errorf("複数あれば ID の順で先頭を使う: %s", got)
	}
	if got := strings.Join(d.Preview()[1].Args, " "); !strings.Contains(got, "--target <instance-id> ") {
		t.Errorf("Preview はプレースホルダー: %s", got)
	}
	note, rs := d.Resolvers()
	if !strings.Contains(note, "<instance-id>") || len(rs) != 1 || strings.Join(rs[0], " ") != describeKey {
		t.Errorf("Resolvers = %q %v", note, rs)
	}
}

func TestPrepareNoInstance(t *testing.T) {
	d := newDriver(fakeRunner{describeKey: "\n"}, "", map[string]string{"Name": "stg-bastion", "Env": "stg"})
	_, err := d.Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "running のインスタンスがありません") {
		t.Fatalf("err = %v", err)
	}
	if d.Classify(err, nil).Fatal {
		t.Error("インスタンスが無いのは入れ替え中とみなし、再接続する")
	}
}

func TestPrepareAuthExpired(t *testing.T) {
	d := newDriver(fakeRunner{describeKey: "ERR:Error when retrieving token from sso: Token has expired and refresh failed"},
		"", map[string]string{"Name": "stg-bastion", "Env": "stg"})
	_, err := d.Prepare(context.Background())
	ex := d.Classify(err, nil)
	if !ex.Fatal || !strings.Contains(ex.Message, "aws sso login --profile stg-admin") {
		t.Fatalf("Prepare の認証切れは Fatal: %+v (%v)", ex, err)
	}
}

func TestClassifyStartSession(t *testing.T) {
	d := newDriver(nil, "i-0123456789abcdef0", nil)
	if !d.Classify(errors.New("exit status 255"), []byte("An error occurred (ExpiredTokenException) when calling the StartSession operation")).Fatal {
		t.Error("start-session の認証切れは Fatal")
	}
	if d.Classify(errors.New("exit status 0"), []byte("Exiting session with sessionId: x")).Fatal {
		t.Error("アイドルタイムアウトなどの終了は再接続する")
	}
}
