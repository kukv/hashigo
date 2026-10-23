// Package driver は、トンネル1本を張るために起動するプロセスの組み立て方を定義する。
package driver

import "context"

// Listen はプロセスが開くローカルの待ち受け。
type Listen struct {
	Addr string
	Port int
}

// ProcessSpec は起動するプロセス1つ分。
type ProcessSpec struct {
	Tunnel string // "dev.db" など。表示と集計の単位
	Path   string // 実行するバイナリ
	Args   []string
	Env    []string // 追加する環境変数（KEY=VALUE）
	Ports  []Listen // このプロセスが開くローカルポート
}

// Driver はトンネルの種類ごとのコマンドの組み立て方。
type Driver interface {
	// Preview は外部呼び出しをせずに、起動するプロセスを返す（cmd 用）。
	Preview() []ProcessSpec
	// Prepare は接続のたびに呼ばれ、起動するプロセスを返す。並び順は毎回同じにする。
	Prepare(ctx context.Context) ([]ProcessSpec, error)
}
