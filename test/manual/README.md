# ssh 周りの簡易確認（WSL + Docker）

実機の踏み台が用意できないときに、[受け入れ手順](../../docs/acceptance.md) のうち ssh 周りを手元で確かめる手順です。test/e2e の多段 sshd（bastion → mid → ap → db）を Docker で立て、bastion だけを Windows 側の `127.0.0.1:2222` に公開します。WSL 上の hashigo は Windows の `ssh.exe` を使って多段で接続します。

db は Postgres ではなく TCP エコー（送った文字列がそのまま返る）です。相手はコンテナなので、結果は acceptance.md に「簡易確認」と書き、実機での確認は残してください。

## 必要なもの

- WSL2 と Docker Desktop（WSL 連携あり）、Windows の `ssh.exe`
- NAT モードの WSL では `socat`（`sudo apt install socat`）

## 準備と片付け

```sh
make build
test/manual/setup.sh      # 2222 が埋まっていれば POC_PORT=2223 test/manual/setup.sh
```

setup.sh は次のものを作り、最後に `ssh.exe` で ap まで届くことを確かめます。

| 置き場所 | 中身 |
|---|---|
| `%USERPROFILE%\.ssh\hashigo-poc\` | 鍵、`ssh.exe` 用の config、known_hosts |
| `test/manual/.work/hashigo.yaml` | hashigo の設定。`ssh_config` に上の config を指定する |
| `test/manual/.work/hashigo-unc.yaml` | WSL 側の config を UNC パスの `-F` で `ssh.exe` に読ませる設定 |

普段使っている `~/.ssh/config` には触れません。片付けは `test/manual/teardown.sh` です。

以下では `CFG=test/manual/.work/hashigo.yaml` とし、障害は別の端末から `dc` で起こします。

```sh
CFG=test/manual/.work/hashigo.yaml
dc() { docker compose -p hashigo-poc -f test/e2e/compose.yaml -f test/manual/compose.override.yaml "$@"; }
# 届いたかは echo ping | socat -t2 - TCP:127.0.0.1:15432 で見る。ping が返れば OK
```

## 確かめられる項目

| acceptance | 手順 | 合格の条件 |
|---|---|---|
| 準備 | `bin/hashigo doctor --config $CFG` | ✗ が無い |
| 1-1 | `bin/hashigo up --config $CFG two-hop`、15432 にエコー | ping が返る |
| 1-2 | `up --config $CFG three-hop`、25432 にエコー | ping が返る（3段の `-J`） |
| 1-3 | `up` を Ctrl-C で止め、`powershell.exe -c "Get-CimInstance Win32_Process -Filter \"Name='ssh.exe'\""` | hashigo が起動した `ssh.exe` が残っていない |
| 1-4 | `bin/hashigo cmd --config $CFG all` の出力を実行する | `up` と同じように届く |
| 2-1 | `up ... all` 中に `dc restart ap` | three-hop だけが `1s 後に再接続します` → 2s … と延び、`接続を確認しました` で戻る。two-hop は切れない |
| 2-2 | 2-1 のあと 60 秒以上待って、もう一度 `dc restart ap` | 待ちが 1s から始まる |
| 2-3 | `dc stop db`、終わったら `dc start db` | ssh は動き続け、up のまま |
| 3-1〜3-3 | 別の端末で `bin/hashigo status --config $CFG all` | acceptance.md の 3 章のとおり |
| 3-5 | `up` を `kill -9` で止めてから `status` | 残骸として削除され、`停止` |
| 4-1 | `bin/hashigo cmd --config test/manual/.work/hashigo-unc.yaml all` と `up` | `-F \\wsl.localhost\...` が出て、つながる |
| 4-4 | `doctor` | config に `C:\Users\...\hashigo-poc\config`、鍵に `id_ed25519` が出る |
| 5-1〜5-3 | NAT モードのまま `up`、PowerShell で `Get-NetTCPConnection -State Listen -LocalPort 15432` | 待ち受けが仮想 NIC の IP で `0.0.0.0` ではない。WSL から 127.0.0.1 で届く |

mirrored モードでの 1 章は、`.wslconfig` に `networkingMode=mirrored` を書いて WSL を再起動すれば、同じ手順で確かめられます。

## 確かめられない項目

- 6 章（ECS / SSM、SSO、MFA）
- 7 章（Windows ネイティブ）と 8 章（macOS）。ただし 7 章は、Windows 側で `hashigo.exe` を同じコンテナに向ければ一部を確かめられる
- 5-4（Windows の再起動でゲートウェイの IP が変わる）
- 実在の踏み台に固有のもの（sshd の設定、回線の遅延や切断、Wi-Fi の切断）
