package ecs

import (
	"context"
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

const (
	listKey     = "aws ecs list-tasks --cluster stg-cluster --service-name api --desired-status RUNNING --query taskArns[0] --output text --profile stg --region ap-northeast-1"
	describeKey = "aws ecs describe-tasks --cluster stg-cluster --tasks arn:aws:ecs:ap-northeast-1:123:task/stg-cluster/abc123 --query tasks[0].containers[?name=='app'].runtimeId | [0] --output text --profile stg --region ap-northeast-1"
)

func newDriver(run fakeRunner) *Driver {
	return New(Options{
		Tunnel: "stg.db", Bin: "aws", Run: run, Env: []string{"AWS_CONFIG_FILE=/x"},
		Profile: "stg", Region: "ap-northeast-1",
		Cluster: "stg-cluster", Service: "api", Container: "app",
		Forwards: []config.Forward{
			{Bind: "127.0.0.1", LocalPort: 35432, RemoteHost: "stg-db.internal", RemotePort: 5432},
			{Bind: "127.0.0.1", LocalPort: 38080, RemoteHost: "localhost", RemotePort: 8080},
		},
	})
}

func TestPrepare(t *testing.T) {
	d := newDriver(fakeRunner{
		listKey:     "arn:aws:ecs:ap-northeast-1:123:task/stg-cluster/abc123\r\n",
		describeKey: "abc123-4567\r\n",
	})
	specs, err := d.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("forward ごとに1プロセス: %d", len(specs))
	}
	want := "ssm start-session --profile stg --region ap-northeast-1 --target ecs:stg-cluster_abc123_abc123-4567 " +
		"--document-name AWS-StartPortForwardingSessionToRemoteHost --parameters host=stg-db.internal,portNumber=5432,localPortNumber=35432"
	if got := strings.Join(specs[0].Args, " "); got != want {
		t.Errorf("args =\n%s\nwant\n%s", got, want)
	}
	if specs[1].Ports[0] != (driver.Listen{Addr: "127.0.0.1", Port: 38080}) || specs[1].Path != "aws" || specs[1].Tunnel != "stg.db" {
		t.Errorf("2つ目 = %+v", specs[1])
	}
	if !reflect.DeepEqual(specs[0].Env, []string{"AWS_CONFIG_FILE=/x"}) {
		t.Errorf("env = %v", specs[0].Env)
	}
}

func TestPrepareNoTask(t *testing.T) {
	d := newDriver(fakeRunner{listKey: "None\n"})
	_, err := d.Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "RUNNING のタスクがありません") {
		t.Fatalf("err = %v", err)
	}
	if d.Classify(err, nil).Fatal {
		t.Error("タスクが無いのはデプロイ中とみなし、再接続する")
	}
}

func TestPrepareNoContainer(t *testing.T) {
	d := newDriver(fakeRunner{listKey: "arn:aws:ecs:ap-northeast-1:123:task/stg-cluster/abc123\n", describeKey: "None\n"})
	if _, err := d.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), `コンテナ "app"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareAuthExpired(t *testing.T) {
	d := newDriver(fakeRunner{listKey: "ERR:Error when retrieving token from sso: Token has expired and refresh failed"})
	_, err := d.Prepare(context.Background())
	ex := d.Classify(err, nil)
	if !ex.Fatal || !strings.Contains(ex.Message, "aws sso login --profile stg") {
		t.Fatalf("Prepare の認証切れは Fatal: %+v (%v)", ex, err)
	}
}

func TestClassify(t *testing.T) {
	d := newDriver(nil)
	for _, msg := range []string{
		"An error occurred (ExpiredTokenException) when calling the StartSession operation",
		"The SSO session associated with this profile has expired or is otherwise invalid.",
		"Token has expired and refresh failed",
		"An error occurred (UnrecognizedClientException): The security token included in the request is expired",
	} {
		if !d.Classify(errors.New("exit status 255"), []byte(msg)).Fatal {
			t.Errorf("認証切れは Fatal: %s", msg)
		}
	}
	mfa := d.Classify(errors.New("exit status 255"), []byte("Enter MFA code for arn:aws:iam::1:mfa/me: \nAborted!"))
	if !mfa.Fatal || !strings.Contains(mfa.Message, "MFA の認証が切れました") {
		t.Errorf("MFA の入力待ちで失敗したら Fatal: %+v", mfa)
	}
	if d.Classify(errors.New("exit status 0"), []byte("Exiting session with sessionId: x")).Fatal {
		t.Error("アイドルタイムアウトなどの終了は再接続する")
	}
}

func TestPreviewAndResolvers(t *testing.T) {
	d := New(Options{
		Tunnel: "x", Bin: "aws.exe", Profile: "default", Cluster: "c", Service: "s", Container: "app",
		Forwards: []config.Forward{{Bind: "127.0.0.1", LocalPort: 1, RemoteHost: "h", RemotePort: 2}},
	})
	got := strings.Join(d.Preview()[0].Args, " ")
	if !strings.Contains(got, "--target ecs:c_<task-id>_<runtime-id>") || strings.Contains(got, "--region") {
		t.Errorf("Preview = %s", got)
	}
	rs := d.Resolvers()
	if len(rs) != 2 || rs[0][0] != "aws.exe" || rs[0][2] != "list-tasks" || !strings.Contains(strings.Join(rs[1], " "), "--tasks <task-arn>") {
		t.Errorf("Resolvers = %v", rs)
	}
}

func TestResolveProfile(t *testing.T) {
	env := map[string]string{"AWS_PROFILE": "from-env"}
	getenv := func(k string) string { return env[k] }
	if got := ResolveProfile("stg", getenv); got != "stg" {
		t.Errorf("設定の profile が優先: %s", got)
	}
	if got := ResolveProfile("", getenv); got != "from-env" {
		t.Errorf("次に AWS_PROFILE: %s", got)
	}
	if got := ResolveProfile("", func(string) string { return "" }); got != "default" {
		t.Errorf("どちらも無ければ default: %s", got)
	}
}

func TestLoginHintQuotesPath(t *testing.T) {
	got := LoginHint("/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe", "stg")
	if !strings.Contains(got, "'/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe' sso login --profile stg") {
		t.Errorf("空白を含むパスは quote する: %s", got)
	}
}
