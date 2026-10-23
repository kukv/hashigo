# hashigo

[English](README.md)

YAML に定義したポートフォワードを、多段の SSH 踏み台や AWS SSM を経由して、まとめて起動・停止・監視する CLI です。

hashigo（梯子）は、踏み台を一段ずつ登る作業を代わりに行います。SSH 自体は実装していません。手元の `ssh` を子プロセスとして起動し、`-J` と `-L` を組み立てて渡すだけです。そのため、`~/.ssh/config`、`ProxyJump`、`ssh-agent`、鍵はそのまま使えます。WSL では Windows の `ssh.exe` を使うので、鍵と設定は Windows 側に置いたままにでき、WSL を作り直しても影響を受けません。

> **状態: MVP。** フォアグラウンドの `up`（Ctrl-C で停止）、`cmd`、`validate` が動きます。再接続、`status`、`doctor`、ECS（SSM）のフォワード、WSL の relay モードは次のフェーズで対応します（[ロードマップ](#ロードマップ)）。

## 必要なもの

- OpenSSH: Linux と macOS では `ssh`、Windows と WSL では `ssh.exe`（Windows 版 OpenSSH）
- 対話なしで使える鍵（ssh-agent か、パスフレーズのない鍵）。hashigo は `ssh` を `BatchMode=yes` で動かすため、パスフレーズやホスト鍵の確認には答えられません。最初に一度、`ssh <host>` を手で実行してホスト鍵を登録しておいてください。
- WSL では mirrored ネットワーク（`%UserProfile%\.wslconfig` に `networkingMode=mirrored`）。使えない場合は `wsl.mode: native` を指定して、Linux 版の `ssh` を使います。

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
| `defaults` | トップレベル | `ssh`（`auto` / `ssh` / `ssh.exe` / パス）、`keepalive`（既定 `30s`）など、下の階層に継承する設定 |
| `wsl.mode` | トップレベル | `auto`（既定）、`mirrored`、`relay`、`native` |
| `hosts.<名前>` | トップレベル、環境 | `host`（必須）、`user`、`via` |
| `tunnels.<名前>` | トップレベル、環境 | `via` か `host`（どちらか一方）、`forwards`、`keepalive`、`ssh` |
| `groups.<名前>` | トップレベル | `環境.名前`、環境名、トップレベルのトンネル名の一覧 |
| `environments.<名前>` | トップレベル | `protected`、`hosts`、`tunnels`、その環境のトンネルに継承する設定 |

フォワードは、アドレスを指定しない限り `127.0.0.1` で待ち受けます。`hashigo validate` は、未知のキー、存在しない参照、`via` の循環、環境をまたいだローカルポートの重複などを、`ファイル:行:列` 付きですべて報告します。

実際のホスト名を書いた `hashigo.yaml` は、共有リポジトリに commit しないでください。

## 使い方

```sh
hashigo validate                 # 設定を検証する
hashigo cmd prod-db              # 実行する ssh コマンドを表示する
hashigo up prod-db               # トンネルを起動する。Ctrl-C で全部止まる
hashigo up dev                   # 環境内の全トンネル
hashigo up dev prod              # 複数をまとめて
hashigo up dev.db                # 環境内の1本だけ
hashigo up all-db --yes          # protected 環境の確認を省略する
hashigo version
```

対象は `環境.名前` → 環境名 → グループ名 → トップレベルのトンネル名 の順に照合します。どのコマンドも `--config <path>` を受け付けます。

`up` はフォアグラウンドで動きます。ローカルポートがすでに使われていると、起動しません。途中でトンネルが終了したら、それを表示し、ほかのトンネルは動かし続けます。Ctrl-C、SIGTERM、端末を閉じることのどれでも、起動した `ssh` をすべて止めます。

## ロードマップ

| フェーズ | 内容 |
|---|---|
| MVP（現在） | `validate`、`cmd`、フォアグラウンドの `up`、environments と `include`、WSL の `ssh.exe`（mirrored） |
| 安定化 | 指数バックオフでの再接続、ヘルスチェック、`status`、`doctor`、ECS（SSM）のフォワード、WSL の relay モード、`ssh_config` / `aws_config` の切り替え、Windows ネイティブ |
| 拡張 | `up -d` / `down` / `logs`、`-R` と `-D` のフォワード、シェル補完 |

詳しくは [設計書](docs/superpowers/specs/2026-10-23-hashigo-design.md) を参照してください。後のフェーズで使う設定キー（`type: ecs`、`ssh_config`、`aws` など）はすでに書けます。まだ対応していないものは、`up` / `cmd` がそのことをエラーで伝えます。

## 開発に参加する

[CONTRIBUTING.md](CONTRIBUTING.md) を参照してください。セキュリティの問題は [SECURITY.md](SECURITY.md) へ。

## ライセンス

[MIT](LICENSE)
