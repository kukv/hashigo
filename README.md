# hashigo

A CLI that starts, stops, and monitors YAML-defined port forwards through multi-hop SSH bastions and AWS SSM

YAML に定義した多段 SSH のポートフォワードを、1コマンドでまとめて起動・停止する CLI。WSL では Windows の `ssh.exe` を使い、鍵と設定は Windows 側のものをそのまま使う。

## インストール

```sh
go install github.com/kukv/hashigo/cmd/hashigo@latest
```

## 設定ファイル

次の順に探す: `--config` → カレントの `hashigo.yaml` → `~/.config/hashigo/config.yaml` → （WSL のみ）Windows ホームの `.config/hashigo/config.yaml`。

```yaml
hosts:
  bastion: { host: bastion.example.com, user: ec2-user }
  inner:   { host: 10.0.1.10, user: admin, via: bastion }

tunnels:
  prod-db:
    via: inner
    forwards:
      - 15432:db.internal:5432

environments:
  dev:
    hosts:
      bastion: { host: dev-bastion.example.com, user: ec2-user }
    tunnels:
      db:
        via: bastion
        forwards: ["25432:dev-db.internal:5432"]
```

詳細は [設計書](docs/superpowers/specs/2026-10-23-hashigo-design.md) を参照。

## 使い方

```sh
hashigo validate          # 設定を検証する
hashigo cmd prod-db       # 実行する ssh コマンドを表示する
hashigo up prod-db dev    # 起動する。Ctrl-C で全停止
hashigo up dev.db         # 環境内の1本だけ起動する
```

ssh は `BatchMode=yes` で動かすため、ホスト鍵の確認やパスワード入力はできない。初回は `ssh <host>` を手で実行してホスト鍵を登録しておく。
