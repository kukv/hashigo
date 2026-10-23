# hashigo 手動の受け入れ手順

単体テスト、偽バイナリの結合テスト、Docker E2E では確かめられない、実機の WSL・Windows・macOS・AWS での動きを確かめる手順です。各項目の「結果」に日付と OK / NG を書きます。NG のときは、[設計書](superpowers/specs/2026-10-23-hashigo-design.md) の 12 章「結果による変更」に従って設計とコードを直します。

## 0. 準備

- 3段以上の踏み台を経由して届く接続先と、そこからログインできる DB
- WSL2（mirrored と NAT を `.wslconfig` で切り替えられること）と Windows の OpenSSH（`ssh.exe`）。relay の確認には WSL 側の `socat`
- ECS Exec が有効な ECS サービス、Windows 側の `aws.exe` と `session-manager-plugin`、SSO のプロファイル。あれば AssumeRole＋MFA のプロファイル
- 設定ファイルは Windows 側の `%USERPROFILE%\.config\hashigo\config.yaml` に置き、WSL と共有する。実在のホスト名を書いた設定はリポジトリに commit しない
- 最初に `hashigo doctor` を実行し、✗ が無いことを確かめる

## 1. MVP の受け入れ（WSL、mirrored）

2026-10-23 時点で未実施。

| # | 手順 | 合格の条件 | 結果 |
|---|---|---|---|
| 1-1 | 2段のトンネルを `hashigo up <対象>` で起動し、DB クライアントでローカルポートに接続する | DB にログインできる | |
| 1-2 | 3段以上の `via` のトンネルで同じことをする（未確認事項 #6） | ログインできる。できなければ `ProxyCommand` 方式を検討する | |
| 1-3 | `up` を Ctrl-C で止め、PowerShell で `Get-CimInstance Win32_Process -Filter "Name='ssh.exe'"` を実行する（未確認事項 #1） | hashigo が起動した `ssh.exe` が残っていない。残っていた場合も、`up` の出力に「Windows 側に残っていた ssh.exe (pid N) を終了しました」が出て、片付いている | |
| 1-4 | `hashigo cmd <対象>` の出力をそのまま端末で実行する | `up` と同じように転送が張れる | |

## 2. 再接続とヘルスチェック

| # | 手順 | 合格の条件 | 結果 |
|---|---|---|---|
| 2-1 | `up` 中に、途中の踏み台の sshd を再起動するか、Wi-Fi を一度切る | `[id] 終了しました` → `[id] 1s 後に再接続します` → 2s、4s … と間隔が延び、つながると `[id] 接続を確認しました` が出る | |
| 2-2 | 2-1 のあと 60 秒以上つながった状態で、もう一度切る | 再接続の待ちが 1s から始まる | |
| 2-3 | 転送先のサービス（DB など）だけを止める | `ssh` は動き続け、ローカルポートの接続確認が通る間は up のまま。ローカルポートが閉じると `unhealthy` と表示されるが、プロセスは kill されない | |

## 3. status

| # | 手順 | 合格の条件 | 結果 |
|---|---|---|---|
| 3-1 | 別の端末で `hashigo status` を実行する | 起動中のトンネルが `up`、起動していないものが `停止`。全部 up なら終了コード 0、それ以外は 1 | |
| 3-2 | 2-1 の再接続待ちの間に `status` を実行する | `backoff (次回 Ns 後)` と再接続の回数、最終エラーが出る | |
| 3-3 | hashigo とは別のプロセスでローカルポートを使い、`status` を実行する | `hashigo 外のプロセスが使用中` と出る | |
| 3-4 | 端末で実行したときと、`NO_COLOR=1 hashigo status`、`hashigo status \| cat` を比べる | 端末では環境名が色付きで、protected の環境は赤の太字。後の2つは色が付かない | |
| 3-5 | `up` を `kill -9` で止めてから `status` を実行する | 残った状態ファイルが残骸として削除され、`停止` と表示される | |

## 4. ssh_config / aws_config の切り替え

| # | 手順 | 合格の条件 | 結果 |
|---|---|---|---|
| 4-1 | `ssh_config: wsl` にして `hashigo cmd` と `up` を実行する（ssh.exe が WSL 側の config を UNC パスの `-F` で読む。未確認事項 #2） | `cmd` に `-F \\wsl.localhost\<distro>\home\<user>\.ssh\config` が出て、`up` がつながる。権限エラーで読めなければ、ssh.exe × `ssh_config: wsl` を validate エラーにする | |
| 4-2 | `aws_config: wsl` で ECS トンネルを `up` する（WSLENV 経由の `AWS_CONFIG_FILE`。未確認事項 #3） | WSL 側の `~/.aws/config` のプロファイルで接続できる。使えなければ、aws.exe × `aws_config: wsl` を validate エラーにする | |
| 4-3 | `wsl.mode: native`、`ssh_config: windows` で `up` する | Linux 版 ssh が Windows 側の `~/.ssh/config` を `-F /mnt/c/Users/<名前>/.ssh/config` で読んでつながる | |
| 4-4 | `hashigo doctor` を実行する | 「ssh / aws の config」に、実際に読むパスが出る。「鍵ファイル（IdentityFile）」に実在する鍵が出る | |

## 5. relay（WSL、NAT モード）

| # | 手順 | 合格の条件 | 結果 |
|---|---|---|---|
| 5-1 | `.wslconfig` の `networkingMode=mirrored` を外して WSL を再起動し、`hashigo doctor` を実行する | 実行環境に「hashigo のモード relay」、socat に ✓ が出る | |
| 5-2 | `hashigo up <対象>` を実行し、WSL 側から `127.0.0.1:<ポート>` に接続する | 転送が通る。`cmd` に `-L <ゲートウェイIP>:<ポート>:...` と socat の行が出る | |
| 5-3 | PowerShell で `Get-NetTCPConnection -State Listen -LocalPort <ポート>` を実行する | 待ち受けアドレスが WSL 用の仮想 NIC の IP で、`0.0.0.0` ではない。`up` が LAN 公開の警告を出さない | |
| 5-4 | Windows を再起動してゲートウェイの IP を変えてから `up` する | 新しい IP で待ち受けて、つながる | |
| 5-5 | relay のまま ECS トンネルを `up` する | 「ECS トンネルは relay モードでは使えません」のエラーで起動しない（R2） | |

## 6. ECS（SSM）

| # | 手順 | 合格の条件 | 結果 |
|---|---|---|---|
| 6-1 | `hashigo cmd <ECS トンネル>` を実行する | `<task-id>` / `<runtime-id>` のプレースホルダーと、それを解決する list-tasks / describe-tasks のコマンドが出る | |
| 6-2 | `hashigo up <ECS トンネル>` を実行し、宛先に接続する | 接続できる | |
| 6-3 | forward を2本にして起動し、片方だけを 20 分以上使わずに置く | アイドルタイムアウトで切れた方だけが張り直され、もう片方は止まらない | |
| 6-4 | `up` 中にサービスを強制デプロイしてタスクを入れ替える（未確認事項 #5） | 新しいタスクに張り直される。デプロイ中は `準備に失敗しました: RUNNING のタスクがありません` のあと再接続を待つ | |
| 6-5 | SSO のセッションが切れた状態で `up` 中の再接続を起こす | `failed: AWS の認証が切れています。<aws> sso login --profile <名前> を…` で止まり、再接続を繰り返さない | |
| 6-6 | AssumeRole＋MFA のプロファイルで `up` する | 起動前に一度だけ MFA コードを聞かれ、入力すると起動する。`doctor` では「要認証（MFA）」と出る | |
| 6-7 | NAT モードの WSL で `aws.exe ssm start-session`（ポートフォワード）を手で実行し、PowerShell で待ち受けアドレスを見る（未確認事項 #4） | `127.0.0.1` でしか待ち受けないことを確かめる。`0.0.0.0` や仮想 NIC の IP でも待ち受けるなら、R2 を見直して relay で ECS を使えるようにする | |

## 7. Windows ネイティブ

| # | 手順 | 合格の条件 | 結果 |
|---|---|---|---|
| 7-1 | PowerShell で `hashigo up`、`status`、`doctor` を実行する | 動く。`status` の色が出る。`doctor` の WSL の項目は「対象外」 | |
| 7-2 | `up` を Ctrl-C で止める | `ssh.exe` が残らない | |
| 7-3 | タスクマネージャーで `hashigo.exe` を強制終了する | Job Object により、`ssh.exe`（ProxyJump の子も含む）が残らない | |
| 7-4 | `%LOCALAPPDATA%\hashigo\run\` を見る | `up` 中は `<pid>.json` があり、終了すると消える | |
| 7-5 | `ssh_config: wsl` を書いて `validate` する | 「Windows ネイティブでは ssh_config: wsl は使えません」のエラーになる | |

## 8. macOS

| # | 手順 | 合格の条件 | 結果 |
|---|---|---|---|
| 8-1 | `hashigo up`、`status`、`doctor` を実行する | 動く。状態ファイルは `~/.local/state/hashigo/run/` | |
| 8-2 | `up` を `kill -9` で止めてから `status` を実行する | 残骸と判定されて削除される（`sysctl kern.proc.pid` で起動時刻を比べる） | |
