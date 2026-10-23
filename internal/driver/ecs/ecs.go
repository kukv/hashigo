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

func (d *Driver) common() []string {
	a := []string{"--profile", d.o.Profile}
	if d.o.Region != "" {
		a = append(a, "--region", d.o.Region)
	}
	return a
}

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
			"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
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

// Resolvers は、接続のたびにタスク ID とランタイム ID を解決するコマンドを返す（cmd の表示用）。
func (d *Driver) Resolvers() [][]string {
	return [][]string{
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
	arn := clean(out)
	if arn == "" || arn == "None" {
		return nil, errNoTask
	}
	taskID := arn[strings.LastIndex(arn, "/")+1:]
	out, err = d.o.Run.Output(ctx, d.o.Bin, d.describeArgs(arn)...)
	if err != nil {
		return nil, fmt.Errorf("aws ecs describe-tasks に失敗しました: %s", platform.ErrText(err))
	}
	rid := clean(out)
	if rid == "" || rid == "None" {
		return nil, fmt.Errorf("タスク %s にコンテナ %q がありません", taskID, d.o.Container)
	}
	return d.specs(fmt.Sprintf("ecs:%s_%s_%s", d.o.Cluster, taskID, rid)), nil
}

// clean は aws.exe の出力の CRLF と前後の空白を取り除く。
func clean(b []byte) string {
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\r", ""))
}

var authExpired = []string{
	"ExpiredToken",
	"Token has expired",
	"The SSO session associated with this profile has expired",
	"Error when retrieving token from sso",
	"The security token included in the request is expired",
}

// IsAuthExpired は aws の出力が、SSO トークンの期限切れや認証情報の失効を示しているかを返す。
func IsAuthExpired(text string) bool {
	for _, s := range authExpired {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// LoginHint は SSO に再ログインする案内を返す。SSO のキャッシュは実行バイナリ側にあるので、同じバイナリで案内する。
func LoginHint(bin, profile string) string {
	return fmt.Sprintf("AWS の認証が切れています。%s sso login --profile %s を実行してから、もう一度 up してください", quote(bin), profile)
}

// ResolveProfile は使う AWS プロファイルを決める。トンネル / 環境の profile → AWS_PROFILE → default の順。
func ResolveProfile(p string, getenv func(string) string) string {
	if p != "" {
		return p
	}
	if p := getenv("AWS_PROFILE"); p != "" {
		return p
	}
	return "default"
}

func quote(s string) string {
	if strings.ContainsAny(s, " \t'\"") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// Classify は認証切れ（MFA の入力待ちで失敗した場合を含む）なら Fatal にする。RUNNING のタスクが無い、アイドルタイムアウトで切れたなどは再接続する。
func (d *Driver) Classify(err error, tail []byte) driver.Exit {
	text := string(tail)
	if err != nil {
		text += "\n" + err.Error()
	}
	if IsAuthExpired(text) {
		return driver.Exit{Fatal: true, Message: LoginHint(d.o.Bin, d.o.Profile)}
	}
	// 実行中に MFA 付きの一時認証情報が切れると、再接続時に非対話で MFA を聞かれて失敗し続けるので、再試行しない
	if strings.Contains(text, "MFA") {
		return driver.Exit{Fatal: true, Message: fmt.Sprintf("MFA の認証が切れました。hashigo up をもう一度実行して MFA コードを入力してください（profile: %s）", d.o.Profile)}
	}
	return driver.Exit{}
}
