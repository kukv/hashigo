// Package ssm は SSM Session Manager のポートフォワードで、EC2 インスタンス（踏み台）経由の転送を張るドライバ。
// 踏み台に SSH 鍵を配らずに「ローカル → EC2 インスタンス → 宛先」の1段で完結させる。
package ssm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/driver/awscli"
	"github.com/kukv/hashigo/internal/platform"
)

// Options は SSM ドライバの入力。
type Options struct {
	Tunnel    string
	Bin       string          // aws / aws.exe のパス
	Run       platform.Runner // describe-instances の呼び出しに使う。aws_config の環境変数を設定済みのもの
	Env       []string        // 子プロセス（ssm start-session）に渡す環境変数
	Profile   string          // すべての呼び出しに --profile で付ける
	Region    string          // 空なら --region を付けず、プロファイルの値に任せる
	Target    string          // インスタンス ID。空なら TargetTag で引く
	TargetTag map[string]string
	Forwards  []config.Forward
}

// Driver は forward ごとに1つの aws ssm start-session を張る（SSM は1セッション1ポート）。
type Driver struct{ o Options }

// New は SSM ドライバを作る。
func New(o Options) *Driver { return &Driver{o: o} }

// placeholder は、タグで引くインスタンス ID の cmd 表示用の置き換え。
const placeholder = "<instance-id>"

func (d *Driver) common() []string { return awscli.Common(d.o.Profile, d.o.Region) }

// Parameters は start-session の --parameters に渡す JSON を返す。シェルを通さずに1つの引数として渡す。
func Parameters(f config.Forward) string {
	q := func(s string) string {
		b, _ := json.Marshal([]string{s})
		return string(b)
	}
	return fmt.Sprintf(`{"host":%s,"portNumber":%s,"localPortNumber":%s}`,
		q(f.RemoteHost), q(strconv.Itoa(f.RemotePort)), q(strconv.Itoa(f.LocalPort)))
}

// filters は describe-instances の --filters に渡す JSON を返す。タグ名の順に並べ、running のものに絞る。
func (d *Driver) filters() string {
	type filter struct {
		Name   string   `json:"Name"`
		Values []string `json:"Values"`
	}
	keys := make([]string, 0, len(d.o.TargetTag))
	for k := range d.o.TargetTag {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fs := make([]filter, 0, len(keys)+1)
	for _, k := range keys {
		fs = append(fs, filter{Name: "tag:" + k, Values: []string{d.o.TargetTag[k]}})
	}
	fs = append(fs, filter{Name: "instance-state-name", Values: []string{"running"}})
	b, _ := json.Marshal(fs)
	return string(b)
}

func (d *Driver) describeArgs() []string {
	return append([]string{
		"ec2", "describe-instances", "--filters", d.filters(),
		"--query", "Reservations[].Instances[].InstanceId", "--output", "text",
	}, d.common()...)
}

func (d *Driver) specs(target string) []driver.ProcessSpec {
	specs := make([]driver.ProcessSpec, len(d.o.Forwards))
	for i, f := range d.o.Forwards {
		args := append([]string{"ssm", "start-session"}, d.common()...)
		args = append(args,
			"--target", target,
			"--document-name", awscli.PortForwardDocument,
			"--parameters", Parameters(f),
		)
		specs[i] = driver.ProcessSpec{
			Tunnel: d.o.Tunnel, Path: d.o.Bin, Args: args, Env: d.o.Env,
			Ports: []driver.Listen{{Addr: f.Bind, Port: f.LocalPort}},
		}
	}
	return specs
}

// Preview は起動するコマンドを返す。タグで引く場合はインスタンス ID をプレースホルダーにする。外部呼び出しはしない。
func (d *Driver) Preview() []driver.ProcessSpec {
	if d.o.Target != "" {
		return d.specs(d.o.Target)
	}
	return d.specs(placeholder)
}

// Resolvers は、接続のたびにインスタンス ID を解決するコマンドと、その説明を返す（cmd の表示用）。
// インスタンス ID を直接書いた場合は何も返さない。
func (d *Driver) Resolvers() (string, [][]string) {
	if d.o.Target != "" {
		return "", nil
	}
	return placeholder + " は、接続のたびに次のコマンドで解決します（複数あれば ID の順で先頭を使います）",
		[][]string{append([]string{d.o.Bin}, d.describeArgs()...)}
}

var errNoInstance = errors.New("target_tag に一致する running のインスタンスがありません")

// Prepare はインスタンス ID を決めて、forward ごとのコマンドを返す。タグで引く場合は接続のたびに ID を解決する。
func (d *Driver) Prepare(ctx context.Context) ([]driver.ProcessSpec, error) {
	if d.o.Target != "" {
		return d.specs(d.o.Target), nil
	}
	out, err := d.o.Run.Output(ctx, d.o.Bin, d.describeArgs()...)
	if err != nil {
		return nil, fmt.Errorf("aws ec2 describe-instances に失敗しました: %s", platform.ErrText(err))
	}
	ids := slices.DeleteFunc(strings.Fields(awscli.Clean(out)), func(s string) bool { return s == "None" })
	if len(ids) == 0 {
		return nil, errNoInstance
	}
	sort.Strings(ids)
	return d.specs(ids[0]), nil
}

// Classify は認証切れ（MFA の入力待ちで失敗した場合を含む）なら Fatal にする。インスタンスが見つからない、
// アイドルタイムアウトで切れたなどは再接続する。
func (d *Driver) Classify(err error, tail []byte) driver.Exit {
	return awscli.Classify(d.o.Bin, d.o.Profile, err, tail)
}
