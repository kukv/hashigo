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

// Exit はプロセスの終了（または Prepare の失敗）をどう扱うか。
type Exit struct {
	Fatal   bool   // true なら再接続せずに failed にする
	Message string // Fatal のときに表示する案内
}

// Driver はトンネルの種類ごとのコマンドの組み立て方。
type Driver interface {
	// Preview は外部呼び出しをせずに、起動するプロセスを返す（cmd 用）。
	Preview() []ProcessSpec
	// Prepare は接続のたびに呼ばれ、起動するプロセスを返す。並び順は毎回同じにする。
	Prepare(ctx context.Context) ([]ProcessSpec, error)
	// Classify は終了の理由（stderr の末尾を含む）から、再接続するかを決める。
	Classify(err error, stderrTail []byte) Exit
}

// Rebinder は待ち受けアドレスを差し替えてプロセスを組み立てられるドライバ。relay モードで使う。
type Rebinder interface {
	Driver
	PreviewAt(bind string) []ProcessSpec
	PrepareAt(ctx context.Context, bind string) ([]ProcessSpec, error)
}
