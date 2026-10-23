# hashigo

<p align="center"><img src="docs/assets/hashigo_2.png" width="420" alt="hashigo"></p>

[English](README.md)

YAML に定義したポートフォワードを、多段の SSH 踏み台や AWS SSM を経由して、まとめて起動・停止・監視する CLI です。

hashigo（梯子）は、踏み台を一段ずつ登る作業を代わりに行います。SSH 自体は実装していません。手元の `ssh` を子プロセスとして起動し、`-J` と `-L` を組み立てて渡すだけです。そのため、`~/.ssh/config`、`ProxyJump`、`ssh-agent`、鍵はそのまま使えます。WSL では Windows の `ssh.exe` を使うので、鍵と設定は Windows 側に置いたままにでき、WSL を作り直しても影響を受けません。

> **状態: 安定化。** 再接続、ヘルスチェック、`status`、`doctor`、ECS（SSM）のフォワード、WSL の relay モード、Windows ネイティブに対応しています。実機での確認の手順は [docs/acceptance.md](docs/acceptance.md) にあります。

## 必要なもの

- OpenSSH: Linux と macOS では `ssh`、Windows と WSL では `ssh.exe`（Windows 版 OpenSSH）
- 対話なしで使える鍵（ssh-agent か、パスフレーズのない鍵）。hashigo は `ssh` を `BatchMode=yes` で動かすため、パスフレーズやホスト鍵の確認には答えられません。最初に一度、`ssh <host>` を手で実行してホスト鍵を登録しておいてください。
- WSL では mirrored ネットワーク（`%UserProfile%\.wslconfig` に `networkingMode=mirrored`）を推奨します。NAT のままなら relay モードになり、WSL 側に `socat` が必要です。`wsl.mode: native` を指定すると Linux 版の `ssh` を使います。
- ECS のトンネルには `aws`（WSL では `aws.exe`）と `session-manager-plugin`、ECS Exec が有効なサービスが必要です。

## インストール

### mise

[mise](https://mise.jdx.dev/) で、GitHub Releases の配布バイナリを入れられます。

    mise use -g github:kukv/hashigo@latest

### 手動でダウンロード

[リリースページ](https://github.com/kukv/hashigo/releases) から自分の環境のアーカイブを取得して展開し、`hashigo` を `PATH` の通った場所に置きます。

    tar xzf hashigo_<version>_linux_amd64.tar.gz
    install -m 0755 hashigo ~/.local/bin/

Windows では zip を展開し、`hashigo.exe` を `PATH` の通ったディレクトリに置きます。

アーカイブと一緒に `checksums.txt` も公開しています。

    sha256sum -c checksums.txt --ignore-missing

### go install

    go install github.com/kukv/hashigo/cmd/hashigo@latest

### ソースからビルド

    git clone https://github.com/kukv/hashigo.git
    cd hashigo
    make build   # bin/hashigo

## 設定ファイル

次の順に探し、最初に見つかったファイルを使います。

1. `--config <path>`
2. カレントディレクトリの `hashigo.yaml`
3. `$XDG_CONFIG_HOME/hashigo/config.yaml`（未設定なら `~/.config/hashigo/config.yaml`。Windows では `%USERPROFILE%\.config\hashigo\config.yaml`）
4. WSL のみ: Windows のホームの `.config/hashigo/config.yaml`。WSL と Windows で同じファイルを共有できます。

```yaml
hosts:
  bastion: { host: bastion.example.com, user: ec2-user }
  inner:   { host: 10.0.1.10, user: admin, via: bastion }   # via で多段にする

tunnels:
  prod-db:
    via: inner                       # ssh -J ec2-user@bastion.example.com admin@10.0.1.10
    forwards:
      - 15432:db.internal:5432       # ローカルポート:宛先ホスト:宛先ポート
      - { local: "127.0.0.1:16379", remote: "redis.internal:6379" }
  web:
    host: app1                       # ~/.ssh/config のエイリアスも使える
    forwards: ["8080:localhost:80"]

groups:
  prod: [prod-db, web]
```

複数の環境は `environments` の下に書きます。名前は `環境.名前` で参照します。環境の中の `via` は、まず同じ環境のホストを探し、無ければトップレベルを探します。

```yaml
environments:
  dev:
    hosts:
      bastion: { host: dev-bastion.example.com, user: ec2-user }
    tunnels:
      db: { via: bastion, forwards: ["15432:dev-db.internal:5432"] }
  prod:
    protected: true                  # up の前に確認を出す
    hosts:
      bastion: { host: prod-bastion.example.com, user: ec2-user }
    tunnels:
      db: { via: bastion, forwards: ["25432:prod-db.internal:5432"] }

groups:
  all-db: [dev.db, prod.db]
```

| キー | 置ける場所 | 内容 |
|---|---|---|
| `include` | トップレベル | 一緒に読み込むファイル。読み込んだファイルからの相対パス |
| `defaults` | トップレベル | `ssh`（`auto` / `ssh` / `ssh.exe` / パス）、`ssh_config` と `aws_config`（`auto` / `windows` / `wsl` / パス）、`aws`、`keepalive`（既定 `30s`）、`reconnect`（既定 `true`）。下の階層に継承する |
| `wsl.mode` | トップレベル | `auto`（既定）、`mirrored`、`relay`、`native` |
| `hosts.<名前>` | トップレベル、環境 | `host`（必須）、`user`、`via` |
| `tunnels.<名前>` | トップレベル、環境 | ssh: `via` か `host`（どちらか一方）、`forwards`、`hostname`、`keepalive`、`ssh`、`ssh_config`。ECS: `type: ecs`、`cluster`、`service`、`container`、`forwards`、`profile`、`region`、`aws`、`aws_config` |
| `groups.<名前>` | トップレベル | `環境.名前`、環境名、トップレベルのトンネル名の一覧 |
| `environments.<名前>` | トップレベル | `protected`、`hosts`、`tunnels`、その環境のトンネルに継承する設定 |

ECS のトンネルは SSM Session Manager（ECS Exec）でつなぎます。タスク ID は接続のたびに引き直します。

```yaml
environments:
  stg:
    aws: { profile: stg }            # 環境内の ECS トンネルに継承する
    tunnels:
      db:
        type: ecs
        region: ap-northeast-1
        cluster: stg-cluster
        service: api
        container: app
        forwards: ["35432:stg-db.internal:5432"]
```

フォワードは、アドレスを指定しない限り `127.0.0.1` で待ち受けます。`hashigo validate` は、未知のキー、存在しない参照、`via` の循環、環境をまたいだローカルポートの重複などを、`ファイル:行:列` 付きですべて報告します。

実際のホスト名を書いた `hashigo.yaml` は、共有リポジトリに commit しないでください。

## 使い方

```sh
hashigo validate                 # 設定を検証する
hashigo cmd prod-db              # 実行する ssh / aws コマンドを表示する
hashigo up prod-db               # トンネルを起動する。Ctrl-C で全部止まる
hashigo up dev                   # 環境内の全トンネル
hashigo up dev prod              # 複数をまとめて
hashigo up dev.db                # 環境内の1本だけ
hashigo up all-db --yes          # protected 環境の確認を省略する
hashigo status                   # 状態を表示する（全部 up なら終了コード 0）
hashigo doctor                   # 実行環境と設定の前提を確かめる
hashigo version
```

対象は `環境.名前` → 環境名 → グループ名 → トップレベルのトンネル名 の順に照合します。どのコマンドも `--config <path>` を受け付けます。

`up` はフォアグラウンドで動きます。ローカルポートがすでに使われていると起動せず、使っている相手（別の `hashigo up` か、hashigo 外のプロセスか）を表示します。トンネルが切れたら、1s → 2s → 4s … 最大 60s の間隔で張り直します（`reconnect: false` なら張り直しません）。10 秒ごとにローカルポートへの接続を確かめ、つながらなければ `unhealthy` と表示します。SSO の期限切れのように張り直しても直らない失敗は、`aws sso login` などの案内を出して止めます。Ctrl-C、SIGTERM、端末を閉じることのどれでも、起動したプロセスをすべて止めます。WSL では、Windows 側に残った `ssh.exe` / `aws.exe` も片付けます。

`status` は、`up` が書く状態ファイル（`~/.local/state/hashigo/run/`、Windows は `%LOCALAPPDATA%\hashigo\run\`）と TCP 接続で、トンネルごとの状態、再接続の回数、最後のエラーを表示します。`doctor` は、WSL のネットワークモード、ssh / aws / session-manager-plugin / socat の有無、AWS の認証、ポートの衝突、実際に読む config、鍵ファイル、hosts の設定漏れ（`hostname` を書いたトンネル）、relay の LAN 公開を確かめます。

## ロードマップ

| フェーズ | 内容 |
|---|---|
| MVP | `validate`、`cmd`、フォアグラウンドの `up`、environments と `include`、WSL の `ssh.exe`（mirrored） |
| 安定化（現在） | 指数バックオフでの再接続、ヘルスチェック、`status`、`doctor`、ECS（SSM）のフォワード、WSL の relay モード、`ssh_config` / `aws_config` の切り替え、Windows ネイティブ |
| 拡張 | `up -d` / `down` / `logs`、`-R` と `-D` のフォワード、シェル補完 |

詳しくは [設計書](docs/superpowers/specs/2026-10-23-hashigo-design.md) を参照してください。

## 開発に参加する

[CONTRIBUTING.md](CONTRIBUTING.md) を参照してください。セキュリティの問題は [SECURITY.md](SECURITY.md) へ。

## ライセンス

[MIT](LICENSE)
