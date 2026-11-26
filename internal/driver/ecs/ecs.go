// Package ecs は SSM Session Manager（ECS Exec）のポートフォワードで、ECS タスク経由の転送を張るドライバ。
// ECS のコンテナには sshd が無いため、ssh とは組み合わせず「ローカル → ECS タスク → 宛先」の1段で完結させる。
package ecs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/driver/awscli"
	"github.com/kukv/hashigo/internal/platform"
)

// Options は ECS ドライバの入力。
type Options struct {
	Tunnel    string
	Bin       string          // aws / aws.exe のパス
	Run       platform.Runner // list-tasks などの呼び出しに使う。aws_config の環境変数を設定済みのもの
	Env       []string        // 子プロセス（ssm start-session）に渡す環境変数
	Profile   string          // すべての呼び出しに --profile で付ける
	Region    string          // 空なら --region を付けず、プロファイルの値に任せる
	Cluster   string
	Service   string
	Container string
	Forwards  []config.Forward
}

// Driver は forward ごとに1つの aws ssm start-session を張る（SSM は1セッション1ポート）。
type Driver struct{ o Options }

// New は ECS ドライバを作る。
func New(o Options) *Driver { return &Driver{o: o} }

var errNoTask = errors.New("RUNNING のタスクがありません（デプロイ中の可能性があります）")

func (d *Driver) common() []string { return awscli.Common(d.o.Profile, d.o.Region) }

func (d *Driver) listTasksArgs() []string {
	return append([]string{
		"ecs", "list-tasks", "--cluster", d.o.Cluster, "--service-name", d.o.Service,
		"--desired-status", "RUNNING", "--query", "taskArns[0]", "--output", "text",
	}, d.common()...)
}

func (d *Driver) describeArgs(task string) []string {
	return append([]string{
		"ecs", "describe-tasks", "--cluster", d.o.Cluster, "--tasks", task,
		"--query", fmt.Sprintf("tasks[0].containers[?name=='%s'].runtimeId | [0]", d.o.Container),
		"--output", "text",
	}, d.common()...)
}

func (d *Driver) specs(target string) []driver.ProcessSpec {
	specs := make([]driver.ProcessSpec, len(d.o.Forwards))
	for i, f := range d.o.Forwards {
		args := append([]string{"ssm", "start-session"}, d.common()...)
		args = append(args,
			"--target", target,
			"--document-name", awscli.PortForwardDocument,
			"--parameters", fmt.Sprintf("host=%s,portNumber=%d,localPortNumber=%d", f.RemoteHost, f.RemotePort, f.LocalPort),
		)
		specs[i] = driver.ProcessSpec{
			Tunnel: d.o.Tunnel, Path: d.o.Bin, Args: args, Env: d.o.Env,
			Ports: []driver.Listen{{Addr: f.Bind, Port: f.LocalPort}},
		}
	}
	return specs
}

// Preview は、タスク ID とランタイム ID をプレースホルダーにしたコマンドを返す。外部呼び出しはしない。
func (d *Driver) Preview() []driver.ProcessSpec {
	return d.specs(fmt.Sprintf("ecs:%s_<task-id>_<runtime-id>", d.o.Cluster))
}

// Resolvers は、接続のたびにタスク ID とランタイム ID を解決するコマンドと、その説明を返す（cmd の表示用）。
func (d *Driver) Resolvers() (string, [][]string) {
	return "<task-id> と <runtime-id> は、接続のたびに次のコマンドで解決します", [][]string{
		append([]string{d.o.Bin}, d.listTasksArgs()...),
		append([]string{d.o.Bin}, d.describeArgs("<task-arn>")...),
	}
}

// Prepare は RUNNING のタスクを1つ選び、コンテナのランタイム ID を引いて、forward ごとのコマンドを返す。
func (d *Driver) Prepare(ctx context.Context) ([]driver.ProcessSpec, error) {
	out, err := d.o.Run.Output(ctx, d.o.Bin, d.listTasksArgs()...)
	if err != nil {
		return nil, fmt.Errorf("aws ecs list-tasks に失敗しました: %s", platform.ErrText(err))
	}
	arn := awscli.Clean(out)
	if arn == "" || arn == "None" {
		return nil, errNoTask
	}
	taskID := arn[strings.LastIndex(arn, "/")+1:]
	out, err = d.o.Run.Output(ctx, d.o.Bin, d.describeArgs(arn)...)
	if err != nil {
		return nil, fmt.Errorf("aws ecs describe-tasks に失敗しました: %s", platform.ErrText(err))
	}
	rid := awscli.Clean(out)
	if rid == "" || rid == "None" {
		return nil, fmt.Errorf("タスク %s にコンテナ %q がありません", taskID, d.o.Container)
	}
	return d.specs(fmt.Sprintf("ecs:%s_%s_%s", d.o.Cluster, taskID, rid)), nil
}

// Classify は認証切れ（MFA の入力待ちで失敗した場合を含む）なら Fatal にする。RUNNING のタスクが無い、アイドルタイムアウトで切れたなどは再接続する。
func (d *Driver) Classify(err error, tail []byte) driver.Exit {
	return awscli.Classify(d.o.Bin, d.o.Profile, err, tail)
}
