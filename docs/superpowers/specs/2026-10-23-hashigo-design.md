# hashigo 設計書

- 作成日: 2026-10-23
- 元にした計画書: [hashigo 計画書（多段対応ポートフォワード CLI）](https://claude.ai/artifact/BxWtxfewjzJ1vgAamGkzvU)（rev 35）
- 対象範囲: MVP と安定化の全項目。拡張フェーズの項目は作らないが、後から足せる設計にする
- 実装プラン: 2本に分ける。MVP のプランを書いて実装・検証し、そのあとで安定化のプランを書く

## 1. 目的と前提

YAML に定義した複数のポートフォワードを、1コマンドで起動・停止・監視できる CLI `hashigo` を作る。踏み台を経由する多段 SSH、ECS への SSM ポートフォワード、WSL から Windows の `ssh.exe` / `aws.exe` を使う構成に対応する。

- SSH を自前実装せず、`ssh` / `ssh.exe`、`aws` / `aws.exe` を子プロセスとして起動・監視するラッパーにする
- 実装言語は Go（シングルバイナリ、クロスコンパイル、`go install` 1行で再導入できる）
- SSH の鍵と SSO のキャッシュは実行バイナリ側（WSL では Windows 側）に置く。WSL は作り直す前提で、WSL 側に鍵を置かない
- 対象 OS: Linux、macOS、WSL、Windows ネイティブ（Windows ネイティブは安定化フェーズ）
- 転送は汎用の TCP。DB 以外の Web アプリ、Redis、内部 API、WebSocket、gRPC も通る。UDP は対象外

## 2. 全体構成

4層に分ける。

```
config ──▶ driver ──▶ supervisor ──▶ 子プロセス (ssh / aws / socat)
  │          ▲            │
  │          └─ platform ─┘  (バイナリ選択・パス変換・relay・後始末・OS 差分)
  └──▶ cli (up / cmd / validate / status / doctor / version)
```

- `config`: 読み込み、include、正規化、名前解決、検証
- `driver`: 1回の接続試行で立てるプロセスの一覧（ProcessSpec）を組み立てる
- `supervisor`: 子プロセスの起動・監視・再接続・ヘルスチェック・状態ファイル
- `platform`: OS と WSL の差分をすべて引き受ける

ライブラリ: CLI は `github.com/spf13/cobra`、YAML は `github.com/goccy/go-yaml`（エラーの行と列を出せる）、OS 固有の処理は `golang.org/x/sys`。

### パッケージ構成

```
cmd/hashigo/            main
internal/cli/           cobra のコマンド定義（薄く保つ）
internal/config/        読み込み、include、正規化、名前解決、validate
internal/driver/        Driver インターフェース、ssh/、ecs/、relay/（relay モードの書き換え）
internal/supervisor/    スロット、状態遷移、バックオフ、ヘルスチェック
internal/state/         状態ファイルの読み書き、残骸の判定
internal/platform/      OS / WSL 判定、パス変換、relay、後始末、proc_{unix,windows}.go
internal/doctor/        確認項目
internal/testutil/      偽 ssh / aws、Docker E2E のヘルパー
```

外部コマンドの実行（aws の list-tasks、`wslpath`、`powershell.exe` など）は、すべて `Runner` インターフェースを通す。時刻は `Clock` インターフェースから取る。どちらもテストで差し替える。

## 3. 設定ファイル

### 3.1 探索順

最初に見つかったファイルを使う。

1. `--config <path>`
2. カレントディレクトリの `hashigo.yaml`
3. `$XDG_CONFIG_HOME/hashigo/config.yaml`（既定は `~/.config/hashigo/config.yaml`）。Windows ネイティブでは `%USERPROFILE%\.config\hashigo\config.yaml`
4. WSL 上のみ: `<Windowsホーム>/.config/hashigo/config.yaml`。Windows ホームは `cmd.exe /c echo %USERPROFILE%` を `wslpath -u` で変換して求める

4 と Windows ネイティブの 3 は同じファイルなので、WSL と Windows で1つの設定を共有できる。

### 3.2 書式

環境が1つならトップレベルに直接書く。

```yaml
defaults:
  ssh: auto          # auto | ssh | ssh.exe | パス
  ssh_config: auto   # auto | windows | wsl | パス
  aws: auto          # auto | aws | aws.exe | パス
  aws_config: auto   # auto | windows | wsl | パス
  keepalive: 30s
  reconnect: true

wsl:
  mode: auto         # auto | mirrored | relay | native

hosts:
  bastion:
    host: bastion.example.com
    user: ec2-user
  inner:
    host: 10.0.1.10
    user: admin
    via: bastion     # 多段は via を数珠つなぎに

tunnels:
  prod-db:
    via: inner
    forwards:
      - 15432:db.internal:5432
      - 16379:redis.internal:6379
  web:
    host: app1       # ~/.ssh/config のエイリアスも可
    forwards:
      - 8080:localhost:80

groups:
  prod: [prod-db, web]
```

複数の環境を扱う場合は `environments` の下に書く。

```yaml
include: [stg.yaml]

environments:
  dev:
    hosts:
      bastion: { host: dev-bastion.example.com, user: ec2-user }
      ap:      { host: 10.0.1.10, user: admin, via: bastion }
    tunnels:
      db:
        via: ap
        forwards:
          - 15432:dev-db.internal:5432
  prod:
    protected: true      # 起動時に確認を出す
    hosts:
      bastion: { host: prod-bastion.example.com, user: ec2-user }
      ap:      { host: 10.1.1.10, user: admin, via: bastion }
    tunnels:
      db:
        via: ap
        forwards:
          - 25432:prod-db.internal:5432

groups:
  all-db: [dev.db, prod.db]
```

ECS のトンネル。

```yaml
environments:
  stg:
    ssh_config: wsl      # 環境単位で上書きできる
    aws:
      profile: stg       # 環境内の ECS トンネルに継承
    tunnels:
      db:
        type: ecs        # 省略時は ssh
        region: ap-northeast-1
        cluster: stg-cluster
        service: api     # タスクIDは接続のたびに解決
        container: app
        forwards:
          - 35432:stg-db.internal:5432
      app:
        type: ecs
        cluster: stg-cluster
        service: api
        container: app
        forwards:
          - 38080:localhost:8080   # ECS コンテナ自身のアプリ
```

### 3.3 キーの定義

| キー | 置ける場所 | 内容 |
|---|---|---|
| `include` | トップレベル | 読み込むファイルの一覧。読み込んだファイルからの相対パス |
| `defaults` | トップレベル | `ssh`, `ssh_config`, `aws`, `aws_config`, `keepalive`, `reconnect` の既定値 |
| `wsl.mode` | トップレベル | `auto` / `mirrored` / `relay` / `native` |
| `hosts.<名前>` | トップレベル、環境 | `host`（必須）, `user`, `via` |
| `tunnels.<名前>` | トップレベル、環境 | 下記 |
| `groups.<名前>` | トップレベルのみ | 対象の一覧（3.5 の規則で解決。グループの入れ子は不可） |
| `environments.<名前>` | トップレベル | `protected`, `hosts`, `tunnels`, 継承できるキー |
| `protected` | 環境 | `true` なら起動時に確認する |

トンネルのキー:

| キー | type: ssh | type: ecs |
|---|---|---|
| `type` | 省略可（既定 `ssh`） | `ecs` |
| `host` / `via` | どちらか一方が必須 | 置けない |
| `cluster`, `service`, `container` | 置けない | 必須 |
| `forwards` | 必須（1つ以上） | 必須（1つ以上） |
| `hostname` | 任意（doctor の hosts 確認に使う） | 任意 |
| 継承できるキー | `ssh`, `ssh_config`, `keepalive`, `reconnect` | `aws`, `aws_config`, `profile`, `region`, `reconnect` |

継承できるキーは `defaults` → 環境 → トンネルの順に上書きする。

**`aws` の2つの書き方**: 計画書の例では、`defaults.aws: auto`（バイナリの選択）と `environments.stg.aws.profile`（プロファイル）の両方に `aws` が出てくる。そこで次のように扱う。

- スカラー `aws: aws.exe` は `aws: { path: aws.exe }` と同じ意味
- マッピング `aws: { path, profile, region }`
- トンネルや環境に直接書いた `profile` / `region` は、`aws.profile` / `aws.region` と同じ意味。同じ階層で両方を書いたら validate エラー

### 3.4 forwards

- 短縮記法: `ローカルポート:宛先ホスト:宛先ポート`（例: `15432:db.internal:5432`）
- 詳細記法: `{ local: 15432 | "127.0.0.1:15432", remote: "db.internal:5432" }`
- 待ち受けアドレスの既定は `127.0.0.1`
- `type: ecs` の forwards には待ち受けアドレスを書けない（SSM の `localPortNumber` は 127.0.0.1 で待ち受けるため。validate エラー）

### 3.5 正規化と名前解決

- トップレベルの `hosts` / `tunnels` は「ルート環境」（名前なし）に入れる。`environments.<名前>` は名前付きの環境になる
- `include` したファイルは、トップレベルのキーごとにマージする。同じ環境・ホスト・トンネル・グループが2つのファイルで定義されていたらエラー。`include` のループもエラー
- 環境内の `via` / `host`: 同じ環境 → ルート環境の順に探す
- トンネルの `host` は、hosts にあればそれを使う。無ければ ssh config のエイリアスか生のホスト名として、そのまま ssh に渡す。`via` は hosts に存在しなければエラー
- `up` / `cmd` / `status` / `doctor` の引数とグループのメンバーは、`env.name` → 環境名（その中の全トンネル）→ グループ名（引数のときのみ）→ ルート環境のトンネル名の順に照合する
- 外から参照するときの名前: 名前付き環境のトンネルは `env.name`、ルート環境のトンネルは `name`

### 3.6 validate の検証項目

- YAML の構文エラー（`ファイル:行:列`）
- 未知のキー（strict decode）
- 存在しない参照（`via`、`host` に書いた hosts 名、グループのメンバー、引数）
- `via` の循環（`a → b → a` のように経路を表示）
- 名前の衝突（環境名・グループ名・ルートのトンネル名）
- ローカルポートの重複（環境をまたいで検出。重複しているトンネルを両方表示）
- 必須項目の欠け、type ごとに置けないキー、ポート番号の範囲、`keepalive` の書式
- `include` のループと重複定義
- Windows ネイティブで `ssh_config: wsl` / `aws_config: wsl` を指定している

エラーはまとめて列挙し、1つでもあれば終了コード 1 にする。`up` / `cmd` / `status` / `doctor` も実行前に同じ検証をする。

## 4. ドライバ

```go
type Listen struct{ Addr string; Port int }

type ProcessSpec struct {
    Tunnel string   // "dev.db" など。status の集計単位
    Path   string   // 実行するバイナリ
    Args   []string
    Env    []string // AWS_CONFIG_FILE, WSLENV など
    Ports  []Listen // このプロセスが開くローカルポート。ヘルスチェック対象
}

type Exit struct {
    Fatal   bool
    Message string // Fatal のときに表示する案内
}

type Driver interface {
    Preview() []ProcessSpec                              // cmd 用。外部呼び出しをしない
    Prepare(ctx context.Context) ([]ProcessSpec, error)  // 接続・再接続のたびに呼ぶ
    Classify(err error, stderrTail []byte) Exit
}
```

`Prepare` が返すスライスの並び順は、呼ぶたびに同じにする（スロットの位置に対応するため）。

### 4.1 SSH ドライバ

1トンネルにつき1つの ProcessSpec を返す。同じトンネルの forwards は `-L` を複数付けて1プロセスにまとめる。

```
<ssh> -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=<keepalive> -o ServerAliveCountMax=3
      -o BatchMode=yes [-F <config>] [-J u1@h1,u2@h2] \
      -L 127.0.0.1:15432:db.internal:5432 ... <user@最終ホスト>
```

- `-J` は `via` を再帰的にたどり、手元に近い踏み台から順に並べる（`-J ec2-user@bastion,admin@10.0.1.10`）
- `user` が無いホストは `host` だけを書く
- `BatchMode=yes` は計画書に無い追加。子は別プロセスグループで動き stdin も `/dev/null` なので、ホスト鍵の確認やパスワード入力が出ると止まってしまう。代わりにすぐ失敗させ、「初回は `ssh <host>` を手で実行してホスト鍵を登録してください」と案内する
- `Prepare` は `Preview` と同じものを返す
- 終了はすべて Retry として扱う
- Windows 版 OpenSSH は ControlMaster に対応していないため、多重化は使わない

### 4.2 ECS ドライバ

ECS のコンテナには sshd が無いため、SSM Session Manager（ECS Exec）のポートフォワードを使う。構成は「ローカル → ECS タスク → 宛先」の1段で完結し、SSH との多段の組み合わせは扱わない。

- `Prepare`:
  1. `ecs list-tasks --cluster <c> --service-name <s> --desired-status RUNNING` で最初のタスクを選ぶ
  2. `ecs describe-tasks` で、`container` の名前に一致するコンテナの `runtimeId` を取る
  3. forward ごとに次の ProcessSpec を返す（SSM は1セッション1ポート）

     ```
     <aws> ssm start-session --profile <p> [--region <r>] \
       --target ecs:<cluster>_<task-id>_<runtime-id> \
       --document-name AWS-StartPortForwardingSessionToRemoteHost \
       --parameters host=<宛先>,portNumber=<宛先ポート>,localPortNumber=<ローカルポート>
     ```
- すべての aws 呼び出しに `--profile` を付ける。トンネルの `profile` → 環境の `profile` → `AWS_PROFILE` → `default` の順に決める。`region` は指定があるときだけ `--region` を付け、無ければプロファイルの値に任せる
- aws の出力に含まれる CRLF は取り除く
- RUNNING のタスクが無い場合はデプロイ中とみなし、Retry にする
- アイドルタイムアウト（既定 20 分）で切れた場合も、終了として Retry にする
- **Fatal にする条件**: stderr（`Prepare` の失敗も含む）に SSO トークンの期限切れや認証情報の失効が出たとき（`ExpiredToken`、`Token has expired`、`The SSO session associated with this profile has expired`、`Error when retrieving token from sso` など）。案内は `<同じ aws バイナリ> sso login --profile <名前>` とする（SSO のキャッシュは実行バイナリ側のホームにあるため）
- **AssumeRole＋MFA**: `up` の起動時、子プロセスを立てる前に、ECS トンネルが使うプロファイルごとに一度ずつ `sts get-caller-identity --profile <p>` を実行する。端末の stdin をつなぐので、MFA コードを対話で入力できる。失敗したら起動しない
- **`cmd` の表示**: `<task-id>` / `<runtime-id>` はプレースホルダーで出し、それを解決するための `list-tasks` / `describe-tasks` のコマンドも並べて出す

### 4.3 拡張フェーズへの備え

リモートフォワード（`-R`）とダイナミックフォワード（`-D`）は、SSH ドライバの引数の組み立てを増やすだけで足せる。forwards の詳細記法に `type: remote | dynamic` を足す想定で、今回は予約もしない（未知のキーとしてエラーになる）。

## 5. スーパーバイザ（`up`）

### 5.1 スロット

`Prepare` が返した ProcessSpec の1つ1つを「スロット」として監視する。SSH は1トンネル1スロット、ECS は forward ごとに1スロット。relay モードでは socat の補助スロットが加わる（6.4）。

あるスロットが終了したときは、そのスロットだけを張り直す。張り直すときは `Prepare` を呼び直し、同じ位置の Spec を使う。ECS の片方のポートがアイドルタイムアウトで切れても、もう片方は止まらない。

スロットの数と並びは `Preview()` で決める。起動直後も含め、各スロットは接続のたびに自分で `Prepare` を呼ぶ（ECS で forward が N 本なら、起動時の list-tasks も N 回になる）。

### 5.2 状態遷移

```
starting → up ⇄ unhealthy
   ↑         ↓ (プロセス終了)
backoff(n) ←─┘   → failed(理由)   ← Fatal、または reconnect: false
                 → stopped        ← Ctrl-C / SIGTERM
```

- **再接続**: 1s → 2s → 4s → … と倍にしていき、上限は 60s。60 秒以上安定して動いたら 1s に戻す。`reconnect: false` なら終了時に failed にする
- **ヘルスチェック**: 10 秒ごとに各ローカルポートへ TCP 接続する（タイムアウト 2 秒）。失敗したら unhealthy として表示するだけで、プロセスは kill しない
- **起動できないとき**: 子プロセスを起動できない（バイナリが無いなど）ときは、再接続せずに failed にする
- **MVP での動き**: 再接続とヘルスチェックは安定化フェーズで入れる。MVP では、プロセスが終了したらそのスロットを failed にし、他のスロットは動かし続ける。停止の指示が無いまま全スロットが終了したら、`up` は終了コード 1 で終わる

### 5.3 起動前のチェック

1. 検証（3.6）
2. 対象に protected の環境が含まれていれば、`prod は protected です。起動しますか? [y/N]` と確認する。`--yes` で省略できる。TTY が無く `--yes` も無ければエラーにする
3. 全トンネルのローカルポートへ TCP 接続を試す。つながったら使用中と判断する。1つでも使用中なら何も起動せずに終了する。衝突相手は状態ファイル（7章）から `dev.db（pid 1234 の up）` のように特定し、特定できなければ「hashigo 外のプロセス」と表示する
4. ECS トンネルがあれば事前認証（4.2）

### 5.4 シグナルと停止

- `signal.NotifyContext` で SIGINT と SIGTERM（Windows では Ctrl-C）を受ける
- 子プロセスは別のプロセスグループで起動する（8章）。端末の Ctrl-C が子に直接届かないので、子の終了を再接続の契機と取り違えない
- 停止するときは、OS ごとの `stop` を呼ぶ（Unix: SIGTERM → 5 秒待つ → SIGKILL）。そのあと platform 層の後始末（6.5）を行う
- キャンセル後のプロセス終了は、再接続の対象にしない

### 5.5 出力

- 各プロセスの stderr に `[dev.db] ...` の接頭辞を付けて表示する
- 状態が変わったときも1行ずつ出す（起動、終了、`5s 後に再接続`、unhealthy、failed と案内）
- stderr の末尾 4KB を保持し、`Classify` に渡す

## 6. platform 層

### 6.1 判定

- **WSL**: 環境変数 `WSL_DISTRO_NAME` があるか、`/proc/version` に `microsoft` が含まれる
- **ネットワークモード**（`wsl.mode: auto`）: `wslinfo --networking-mode` の結果で判定する。`wslinfo` が無ければ `<Windowsホーム>/.wslconfig` の `networkingMode=mirrored` を読む。どちらでも分からなければ NAT とみなし relay を選ぶ

### 6.2 バイナリの選択

- `ssh: auto` → WSL なら `ssh.exe`、それ以外は `ssh`。`wsl.mode: native` なら WSL でも `ssh`
- `aws: auto` → 同じ考え方で `aws.exe` / `aws`
- 探すのは `exec.LookPath` で、シェルの alias は見ない。`ssh.exe` が PATH に無ければ `/mnt/c/Windows/System32/OpenSSH/ssh.exe` を使う

### 6.3 config の参照先

`auto` は実行バイナリと同じ側の config を使う。

| バイナリ | config の場所 | 渡し方 |
|---|---|---|
| ssh.exe | Windows | 何もしない（標準） |
| ssh.exe | WSL | `wslpath -w` で UNC パス（`\\wsl.localhost\<distro>\...`）に変換し、`-F` で指定 |
| ssh | WSL | 何もしない（標準） |
| ssh | Windows | `-F /mnt/c/Users/<名前>/.ssh/config` |
| aws.exe | Windows | 何もしない（標準） |
| aws.exe | WSL | `AWS_CONFIG_FILE` / `AWS_SHARED_CREDENTIALS_FILE` を設定し、`WSLENV` に `/p` 付きで追加 |
| aws | WSL | 何もしない（標準） |
| aws | Windows | `AWS_CONFIG_FILE` などに `/mnt/c/...` のパスを設定 |

パスを直接指定した場合はそのパスを使い、実行バイナリが `.exe` なら `wslpath -w` で変換する。`cmd` と `doctor` は、実際に読む config のパスを表示する。

### 6.4 mirrored と relay

- **mirrored**: ssh.exe が Windows 側の `127.0.0.1` で待ち受け、WSL からもそのまま届く。手を加えない
- **relay**: `internal/driver/relay` がドライバの出力を書き換える（`Wrap(specs) → specs + 補助スロット`）。ゲートウェイ IP の取得など OS に依存する部分は platform 層が受け持つ（depguard で platform から driver を参照できないため）
  - ssh.exe の待ち受けアドレスを、WSL から見たデフォルトゲートウェイ（Windows 側の WSL 用仮想 NIC）の IP にする。`0.0.0.0` にはしないので、LAN には公開されない。この IP は再起動で変わるため、`Prepare` のたびに `ip route` から取り直す
  - ローカルポートごとに socat の補助スロットを追加する: `socat TCP-LISTEN:<port>,bind=127.0.0.1,reuseaddr,fork TCP:<gatewayIP>:<port>`。補助スロットもスーパーバイザが監視・再起動する
  - 起動後に `powershell.exe Get-NetTCPConnection` で Windows 側の待ち受けアドレスを確認し、`0.0.0.0` や `::` なら警告する
- **native**: Linux 版 ssh / aws を使う。非 WSL の Linux と同じ経路

### 6.5 後始末（WSL）

WSL 側のプロセスを止めても、Windows 側の `ssh.exe` が残る前提で作る。

- 停止後、`powershell.exe` で `Win32_Process` から `ssh.exe` のコマンドラインを取得し、自分の `-L <bind>:<port>:` を含むものを `taskkill.exe /F /PID` で終了させる。ローカルポートは全体で一意なので、取り違えは起きない
- `aws.exe` と、そこから起動される `session-manager-plugin.exe` も同じ方法で片付ける（`--parameters` の `localPortNumber=<port>` で照合する）

## 7. 状態ファイルと `status`

### 7.1 状態ファイル

- 場所: `$XDG_STATE_HOME/hashigo/run/<hashigoのpid>.json`（既定は `~/.local/state/hashigo/run/`。macOS も同じ）。Windows ネイティブは `%LOCALAPPDATA%\hashigo\run\`
- `up` を1回起動するごとに1ファイル。別々の端末で `up` しても上書きしない

```json
{ "pid": 1234, "started_at": "2026-10-23T10:00:00+09:00", "config": "/path/hashigo.yaml",
  "slots": [ { "tunnel": "dev.db", "env": "dev", "protected": false,
               "ports": ["127.0.0.1:15432"], "state": "backoff", "child_pid": 5678,
               "reconnects": 3, "next_retry_at": "...", "last_error": "...",
               "healthy": true, "checked_at": "..." } ] }
```

- 状態が変わったときと、ヘルスチェックのたびに書く。一時ファイルに書いて rename する
- 正常終了したら削除する
- **残骸の判定**: 記録された pid が生きていない、または pid の起動時刻が `started_at` と一致しない（pid が再利用された）場合は残骸とみなし、無視して削除する
- `started_at` には、OS から取ったプロセスの起動時刻（8章）を書き、残骸の判定では同じ方法で取った値と 1 秒以内かで比べる
- 拡張フェーズの `up -d` / `down` / `logs` は、このファイルをそのまま使う

### 7.2 `hashigo status [対象...]`

設定にある全トンネル（引数があればそれに絞る）を表示する。状態ファイルがあるトンネルはスロットごとに1行、無いトンネルはトンネルごとに1行（ポートをまとめて表示）にする。

| 環境 | トンネル | ポート | 状態 | 再接続 | 最終エラー |
|---|---|---|---|---|---|

| 状態ファイル | TCP 接続 | 表示 |
|---|---|---|
| あり | 成功 | `up` |
| あり | 失敗 | `unhealthy`、または状態ファイルの値（`backoff (次回 12s 後)`、`failed: <案内>`） |
| なし | 成功 | `hashigo 外のプロセスが使用中` |
| なし | 失敗 | `停止` |

- 環境名は名前のハッシュで色を決める。protected の環境は赤の太字。TTY でない場合と `NO_COLOR` が設定されている場合は色を付けない
- 終了コード: 対象が全部 `up` なら 0、それ以外は 1

## 8. OS ごとの差分

プロセス管理は `proc_unix.go` / `proc_windows.go` に分け、`start`、`stop`、`isAlive(pid, startedAt)` の3つの操作だけを OS ごとに実装する。

| 項目 | Linux / WSL / macOS | Windows ネイティブ（安定化） |
|---|---|---|
| 子プロセスの分離 | `Setpgid` | `CREATE_NEW_PROCESS_GROUP` と Job Object（`KILL_ON_JOB_CLOSE`）。hashigo が異常終了しても子が残らない |
| 停止 | SIGTERM → 5 秒待つ → SIGKILL | Job Object で終了させる |
| 起動時刻（残骸判定） | Linux は `/proc/<pid>/stat`、macOS は `sysctl kern.proc.pid` | `OpenProcess` と `GetProcessTimes` |
| 状態ファイル | `~/.local/state/hashigo/run/` | `%LOCALAPPDATA%\hashigo\run\` |
| 設定の探索（3番目） | `~/.config/hashigo/config.yaml` | `%USERPROFILE%\.config\hashigo\config.yaml` |
| バイナリ | `ssh` / `aws`（WSL では `.exe`） | `ssh.exe` / `aws.exe`。パス変換、relay、taskkill による後始末は不要 |
| `ssh_config: wsl` | WSL では使える | validate エラー（パスで指定する） |
| 色 | ANSI エスケープ | `SetConsoleMode` で VT 処理を有効にしてから ANSI エスケープを出す |
| doctor | WSL の項目は WSL 上でだけ確認する | WSL の項目は「対象外」 |

## 9. CLI

cobra で作る。全コマンド共通のフラグは `--config <path>` だけ。

| コマンド | 内容 | フェーズ |
|---|---|---|
| `hashigo up <対象...> [--yes]` | 対象は1つ以上必須。5章の流れで起動し、フォアグラウンドで動かす。Ctrl-C で全停止 | MVP（安定化で再接続・ヘルスチェック・状態ファイル・ECS・relay・事前認証を追加） |
| `hashigo cmd <対象...>` | 生成したコマンド、実行バイナリ、実際に読む ssh / aws config のパス、WSL のモードを表示する。外部コマンドは実行しない | MVP（ECS の表示は安定化） |
| `hashigo validate` | 3.6 の検証。エラーを `ファイル:行:列 メッセージ` の形で列挙する | MVP |
| `hashigo status [対象...]` | 7.2 | 安定化 |
| `hashigo doctor [対象...]` | 下記 | 安定化 |
| `hashigo version` | バージョンを表示 | MVP |

### doctor の確認項目

結果は `✓` / `⚠` / `✗` で表示する。1つでも `✗` があれば終了コードは 1。該当しない項目は「対象外」と表示する。

1. OS と WSL の判定、ネットワークモード、`.wslconfig` の内容
2. ssh バイナリの場所と `-V`
3. aws バイナリの場所とバージョン、`session-manager-plugin` の有無（ECS トンネルがある場合）
4. 使用するプロファイルごとの認証状態（`sts get-caller-identity`）。非対話で実行し、MFA が必要なら「要認証」と表示する
5. socat の有無（relay モードの場合）
6. ローカルポートの衝突（TCP 接続と状態ファイルで確認）
7. 実際に読む ssh / aws config のパス
8. IdentityFile の存在: 接続先ごとに `ssh -G` の結果を取り、`identityfile` に挙がったファイルを実行バイナリ側のパスで確認する。`-G` は既定の鍵も列挙するので、1つも存在しないときだけ警告する
9. hosts の設定漏れ: `hostname` を書いたトンネルについて、hosts ファイルに `127.0.0.1 <hostname>` があるか確認する。WSL では Windows 側（`C:\Windows\System32\drivers\etc\hosts`）と WSL 側（`/etc/hosts`）の両方を確認し、どちらに無いかを表示する
10. relay で動いている場合の LAN 公開（待ち受けアドレスの確認）
11. ECS × relay の組み合わせ（12章の未確認事項4の結果に従う）

## 10. テスト

1. **単体テスト**
   - config: `testdata/*.yaml` を正規化した結果とエラーメッセージを golden ファイルと比較する（循環、衝突、include などを含む）
   - SSH と ECS のコマンド生成: golden ファイルと比較する
   - ECS の `Prepare` と `Classify`: 偽の Runner でテストする
   - バックオフ（偽の Clock）、状態ファイル、WSL 判定、パス変換
2. **偽バイナリを使った結合テスト**: テストバイナリ自身を、環境変数で「偽 ssh」や「偽 aws」として振る舞わせる（`TestMain` で再実行する方式）
   - 振る舞い: `-L` / `localPortNumber` のポートで待ち受ける、N 秒後に終了する、認証切れのメッセージを stderr に出す、SIGTERM を無視する
   - スーパーバイザの再接続、Fatal での停止、停止時のエスカレーション、ポート衝突、状態ファイルを確認する
3. **Docker E2E**（`//go:build e2e`、`make e2e`）
   - compose で `bastion` → `ap` → `db`（TCP エコー）を並べ、テストの中で使い捨ての鍵を生成する
   - hashigo はクライアント用コンテナ内で Linux 版 ssh を使って動かす。手元の ssh.exe には依存しない
   - シナリオ: 2段・3段のフォワード、`ap` の再起動後に再接続されること、Ctrl-C で子プロセスが残らないこと
4. **手動の受け入れ手順**（`docs/acceptance.md`）
   - WSL の ssh.exe での多段接続、mirrored、relay、実 ECS、Windows ネイティブ、macOS
   - 12章の未確認事項もここで確認する

**CI**: GitHub Actions で ubuntu / macos / windows のマトリクス。`go vet`、単体テスト、結合テストは全 OS で、E2E は ubuntu だけで回す。

**導入**: `go install github.com/kukv/hashigo/cmd/hashigo@latest`。インストールスクリプトは拡張フェーズ。

## 11. フェーズ

### MVP: 普段使いの置き換えができる最小構成

- 設定の読み込み（探索順、include、environments、ルート環境、継承するキーの解釈）と validate
- SSH ドライバ（`-J` の組み立て、常に付けるオプション、keepalive）と `cmd`
- `up`: 複数指定、protected の確認、起動前のポートチェック、フォアグラウンド起動、Ctrl-C での一括停止
- WSL の判定、ssh.exe の選択、パス変換（mirrored 前提）、`wsl.mode: native`
- 環境をまたいだポート重複の検出
- `version`、CI（3 OS）、単体テスト、偽バイナリの結合テスト、Docker E2E

### 安定化: 放置しても使い続けられる状態にする

- 再接続（指数バックオフ）、ヘルスチェック、状態ファイル
- `status` と `doctor`（hosts の設定漏れ警告を含む）
- relay モード（socat の自動管理、LAN 公開の警告）と ssh.exe / aws.exe の後始末
- ECS ドライバ（タスクIDの解決、1セッション1ポート、認証切れの検出、AssumeRole＋MFA の事前認証、`cmd` の表示）
- `ssh_config` / `aws_config` の参照先切り替えと、IdentityFile の存在チェック
- Windows ネイティブ対応
- `docs/acceptance.md`

### 拡張（今回は作らない）

- バックグラウンド実行（`up -d` / `down` / `logs`。7.1 の状態ファイルを使う）
- リモートフォワード（`-R`）とダイナミックフォワード（`-D`）
- 環境変数の展開、シェル補完、インストールスクリプト
- バックグラウンド実行時の事前認証の必須化

## 12. 未確認事項

実装中に確かめ、結果に応じてこの設計を直す。

| # | 確認すること | タイミング | 結果による変更 |
|---|---|---|---|
| 1 | WSL 側で kill したとき、`ssh.exe` が残るか | 安定化: 後始末 | 残らなければ 6.5 の taskkill は保険として残す |
| 2 | ssh.exe で WSL 側の config を `-F`（UNC パス）で読めるか、権限エラーにならないか | 安定化: ssh_config 切り替え | 読めなければ `ssh.exe` × WSL の config を validate エラーにする |
| 3 | `WSLENV` 経由で `AWS_CONFIG_FILE` を aws.exe に渡し、WSL 側の config を使えるか | 安定化: aws_config 切り替え | 使えなければ同様にエラーにする |
| 4 | `session-manager-plugin` の待ち受けが Windows の `127.0.0.1` 固定か。固定なら NAT モードの WSL から届かず、socat 中継も成り立たない | 安定化: ECS ドライバ | 届かなければ、ECS トンネルを relay で起動しようとしたときにエラーにし、mirrored を案内する。doctor でも警告する。計画書の「mirrored / relay の扱いも SSH と共通にできる」と食い違う可能性がある |
| 5 | ECS 経由の転送を、タスク入れ替え後の再接続も含めて安定して張れるか | 安定化: ECS ドライバ | |
| 6 | 3段以上の `-J` と、DB へのログイン（計画書の確認事項1で未確認の部分） | MVP: 受け入れ確認 | 失敗したら `ProxyCommand` 方式を検討する |
| 7 | WSL 上の Docker コンテナでは `/proc/version` に `microsoft` が入り、WSL と誤判定される（E2E では `wsl.mode: native` で回避） | 安定化: WSL 判定の見直し | `/proc/version` だけで判定せず、`WSL_DISTRO_NAME` / `WSL_INTEROP` または `/proc/sys/fs/binfmt_misc/WSLInterop` の存在も条件にする |

安定化プラン（`docs/superpowers/plans/2026-10-23-hashigo-stabilization.md`）での扱い:

| # | 扱い |
|---|---|
| 1 | 結果によらず、WSL で .exe を使ったときは停止後に 6.5 の後始末を必ず行う。実機の確認は `docs/acceptance.md` 1-3 |
| 2 | 設計どおり UNC パスの `-F` で実装した。実機の確認は `docs/acceptance.md` 4-1 |
| 3 | 設計どおり `WSLENV` の `/p` で実装した。実機の確認は `docs/acceptance.md` 4-2 |
| 4 | 届かない側（保守的な側）を選び、ECS トンネルを relay で起動しようとしたらエラーにして mirrored を案内する。doctor でも ✗ にする。実機の確認は `docs/acceptance.md` 6-7 |
| 5 | `docs/acceptance.md` 6-3、6-4 で確認する |
| 6 | `docs/acceptance.md` 1-2 で確認する |
| 7 | `WSL_DISTRO_NAME` / `WSL_INTEROP` があるか、`/proc/version` に microsoft があり `/proc/sys/fs/binfmt_misc/WSLInterop` もある場合だけ WSL とみなすように直した |

MVP の実機受け入れ（プラン Task 14 Step 5）は未実施（2026-10-23 時点）。実機の踏み台を用意できる環境で、WSL mirrored モードを前提に実施する。そのとき、Ctrl-C のあとに Windows 側の `ssh.exe` が残らないこと（#1）を合否の項目にする。残る場合は、6.5 の taskkill による後始末を MVP に前倒しする。手順は `docs/acceptance.md` の 1 章にまとめた。

ユーザー側で用意・判断するもの:

- 自分の YAML の置き場所（Windows 側に一本化するか）と、環境ごとのローカルポート帯（MVP で最初の設定ファイルを書くとき）
- relay の手動確認用に socat、ECS の手動確認用に `aws.exe` と `session-manager-plugin`
- 対象の ECS サービスで ECS Exec が有効か（計画書の確認事項3）

## 13. 計画書との対応表

| 計画書の項目 | 本書 | フェーズ |
|---|---|---|
| YAML の複数フォワードを1コマンドで起動・停止・監視 | 5, 7 | MVP / 安定化 |
| 多段 SSH、WSL から ssh.exe | 4.1, 6 | MVP |
| 鍵は Windows 側、WSL 側に置かない | 1, 6.2, 6.3 | MVP |
| ssh を子プロセスとして起動・監視、Go | 1, 2 | MVP |
| ~/.ssh/config、ProxyJump、ssh-agent をそのまま使う | 4.1 | MVP |
| 生成した ssh コマンドを表示 | 9 `cmd` | MVP |
| ホスト・トンネル・グループの3層、via の数珠つなぎ | 3.2, 3.5 | MVP |
| 短縮記法と詳細記法 | 3.4 | MVP |
| 設定ファイルの探索順（WSL では Windows ホームも） | 3.1 | MVP |
| environments、環境1つならトップレベル | 3.2, 3.5 | MVP |
| `up` / `cmd` / `validate` | 9 | MVP |
| `status`（TCP で生死を判定） | 7.2 | 安定化 |
| `doctor`（WSL、ネットワークモード、ssh、socat、ポート衝突） | 9 | 安定化 |
| `up -d` / `down` / `logs` | 11 拡張、7.1 | 拡張 |
| 1トンネル1プロセス、`-L` をまとめる | 4.1 | MVP |
| `-J` の組み立て、via の循環を validate | 4.1, 3.6 | MVP |
| 常に付けるオプション（`-N`、ExitOnForwardFailure、ServerAlive*） | 4.1 | MVP |
| 起動前のポートチェックと衝突相手の表示 | 5.3 | MVP（衝突相手の特定は安定化の状態ファイルで強化） |
| 指数バックオフの再接続（1s→上限 60s） | 5.2 | 安定化 |
| ヘルスチェック | 5.2 | 安定化 |
| mirrored（推奨） | 6.4 | MVP |
| relay（socat を CLI が管理） | 6.4 | 安定化 |
| native | 6.2, 6.4 | MVP |
| WSL の自動判定、`ssh: auto`、`wsl.mode: auto` | 6.1, 6.2 | MVP |
| `wslpath -w` によるパス変換 | 6.3 | MVP |
| 既定は実行バイナリ側の config、`ssh_config` / `aws_config` で切り替え | 6.3 | 安定化 |
| 多重化なし | 4.1 | MVP |
| relay の LAN 公開の注意 | 6.4, 9 doctor | 安定化 |
| 停止時に ssh.exe を taskkill | 6.5 | 安定化 |
| 環境の名前空間 `環境.名前`、同じ環境を優先して解決 | 3.5 | MVP |
| `up dev` / `up dev prod` / `up dev.db` | 3.5, 9 | MVP |
| `include` | 3.1, 3.5 | MVP |
| 環境をまたいだポート重複を validate | 3.6 | MVP |
| `protected` と `--yes` | 5.3 | MVP |
| status で環境名を色分け | 7.2 | 安定化 |
| ECS: `type` でドライバを切り替え、起動・監視・再接続は共通 | 4, 5 | 安定化 |
| ECS: 生成するコマンドを `cmd` で確認 | 4.2 | 安定化 |
| ECS: 接続・再接続のたびにタスクIDを引き直す | 4.2 | 安定化 |
| ECS: 1セッション1ポート、status ではトンネル単位で表示 | 4.2, 5.1, 7.2 | 安定化 |
| ECS: アイドルタイムアウト後の張り直し | 4.2 | 安定化 |
| ECS: 認証切れで停止し `aws sso login` を案内 | 4.2 | 安定化 |
| ECS: 前提条件（plugin、プロファイル認証）を doctor で確認 | 9 doctor 3, 4 | 安定化 |
| WSL では aws.exe、mirrored / relay は SSH と共通 | 6.2, 6.4, 12 #4 | 安定化 |
| バイナリ × config の組み合わせ表 | 6.3 | 安定化 |
| auto は実行バイナリと同じ側、cmd と doctor で config のパスを表示 | 6.3, 9 | MVP（cmd）/ 安定化（doctor） |
| IdentityFile の存在確認 | 9 doctor 8 | 安定化 |
| AWS プロファイル（`--profile`、AWS_PROFILE、default）、region | 4.2 | 安定化 |
| SSO のキャッシュは実行バイナリ側、同じバイナリで login を案内 | 4.2 | 安定化 |
| AssumeRole＋MFA の事前認証 | 4.2 | 安定化 |
| バックグラウンド実行では事前認証を必須にする | 11 拡張 | 拡張 |
| DB 以外の TCP、WebSocket、gRPC。UDP は対象外 | 1 | MVP |
| hosts ファイルでの対処（ユーザー作業） | 9 doctor 9 | 安定化（警告） |
| ダイナミックフォワード（`-D`） | 4.3, 11 拡張 | 拡張 |
| `hostname` を書くと doctor が hosts の設定漏れを警告 | 3.3, 9 doctor 9 | 安定化 |
| リモートフォワード（`-R`） | 4.3, 11 拡張 | 拡張 |
| 環境変数の展開、シェル補完、インストールスクリプト | 11 拡張 | 拡張 |
| 確認事項1（ssh.exe の ProxyJump）: 確認済み。3段以上と DB ログインは未確認 | 12 #6 | MVP |
| 確認事項2（mirrored）: 確認済み | 6.4 | — |
| 確認事項3（ECS Exec の有効化） | 12 ユーザー側 | 安定化の前 |
| 確認事項4（socat 中継の LAN 公開） | 6.4（仮想 NIC の IP で待ち受け）、9 doctor 10 | 安定化 |
| 実装中の確認: YAML の置き場所とポート帯 | 12 ユーザー側 | MVP |
| 実装中の確認: kill 時に ssh.exe が残るか | 12 #1 | 安定化 |
| 実装中の確認: `-F` に UNC パス | 12 #2 | 安定化 |
| 実装中の確認: WSLENV で AWS_CONFIG_FILE | 12 #3 | 安定化 |
| 実装中の確認: ECS の安定性 | 12 #5 | 安定化 |

計画書に無く、本書で追加したもの:

| 追加した項目 | 本書 | 理由 |
|---|---|---|
| `-o BatchMode=yes` | 4.1 | 子を別プロセスグループで動かすため、対話の入力待ちで止まらないようにする |
| 状態ファイルを安定化フェーズで導入 | 7.1 | `status` で再接続待ちや認証切れを区別するため（ブレストで決定） |
| aws.exe / session-manager-plugin.exe の後始末 | 6.5 | ssh.exe と同じ問題が起きるため |
| Windows ネイティブ対応 | 8 | ブレストで決定 |
| macOS の残骸判定（`sysctl`） | 8 | `/proc` が無いため |
| `status` / `doctor` の終了コード | 7.2, 9 | スクリプトから使えるようにするため |
| `version` コマンド | 9 | |
| `aws` キーのスカラー / マッピング両対応 | 3.3 | 計画書の例で `aws` が2つの意味で使われているため |
