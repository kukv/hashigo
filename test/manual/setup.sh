#!/bin/sh
# 簡易確認の環境を作る。test/e2e の多段 sshd（bastion → mid → ap → db）を Docker で立て、
# ssh.exe 用の鍵と ssh config を Windows 側に、hashigo の設定を .work/ に置く。
set -eu

cd "$(dirname "$0")"
PORT=${POC_PORT:-2222}
WORK=$(pwd)/.work

compose() {
  docker compose -p hashigo-poc -f ../e2e/compose.yaml -f compose.override.yaml "$@"
}

# cmd.exe は WSL 側のカレントディレクトリだと警告を出すので、/mnt/c から呼ぶ
win() {
  (cd /mnt/c && cmd.exe /c "$@" 2>/dev/null | tr -d '\r')
}

for c in docker ssh.exe cmd.exe icacls.exe wslpath; do
  command -v "$c" >/dev/null || {
    echo "$c が見つかりません"
    exit 1
  }
done

compose up -d --build bastion mid ap db

WIN_HOME=$(win 'echo %USERPROFILE%')
WIN_USER=$(win 'echo %USERNAME%')
WIN_DIR_W="$WIN_HOME\\.ssh\\hashigo-poc"
WIN_DIR=$(wslpath -u "$WIN_DIR_W")
# ssh config には / 区切りの Windows パスを書く（ssh.exe はどちらも読める）
WIN_DIR_F=$(printf '%s' "$WIN_DIR_W" | tr '\\' '/')

mkdir -p "$WIN_DIR" "$WORK"
rm -f "$WIN_DIR/id_ed25519" "$WIN_DIR/known_hosts"
compose cp bastion:/keys/id_ed25519 "$WIN_DIR/id_ed25519"
# ssh.exe は他のユーザーも読める鍵を拒むので、継承を外して自分だけにする
icacls.exe "$WIN_DIR_W\\id_ed25519" /inheritance:r /grant:r "$WIN_USER:F" >/dev/null

# ssh.exe 向けの config。コンテナを作り直すとホスト鍵が変わるので、known_hosts は毎回作り直す
ssh_config() {
  cat <<CFG
Host hashigo-bastion
  HostName 127.0.0.1
  Port $PORT

Host hashigo-bastion mid ap
  User hashigo
  IdentityFile $WIN_DIR_F/id_ed25519
  IdentitiesOnly yes
  StrictHostKeyChecking accept-new
  UserKnownHostsFile $WIN_DIR_F/known_hosts
  LogLevel ERROR
CFG
}
ssh_config >"$WIN_DIR/config"
# 同じ内容を WSL 側にも置く。ssh.exe が UNC パスの -F で読めるか（未確認事項 #2）を確かめるため
ssh_config >"$WORK/ssh_config"

hashigo_yaml() {
  cat <<YAML
# setup.sh が生成する簡易確認用の設定
defaults:
  ssh_config: $1
  keepalive: 5s

hosts:
  bastion: { host: hashigo-bastion, user: hashigo }
  mid:     { host: mid, user: hashigo, via: bastion }
  ap:      { host: ap, user: hashigo, via: mid }

tunnels:
  two-hop:
    via: mid
    forwards: ["15432:db:5432"]
  three-hop:
    via: ap
    forwards: ["25432:db:5432"]

groups:
  all: [two-hop, three-hop]
YAML
}
hashigo_yaml "$WIN_DIR/config" >"$WORK/hashigo.yaml"
hashigo_yaml "$WORK/ssh_config" >"$WORK/hashigo-unc.yaml"

# ssh.exe で3段目まで届けば準備完了
i=0
until ssh.exe -F "$WIN_DIR_W\\config" -o BatchMode=yes -o ConnectTimeout=3 -J hashigo-bastion,mid ap true; do
  i=$((i + 1))
  if [ "$i" -ge 30 ]; then
    echo "NG: ssh.exe で ap まで届きません"
    exit 1
  fi
  sleep 1
done

cat <<MSG
OK: ssh.exe で bastion → mid → ap に届きました

  hashigo の設定:         $WORK/hashigo.yaml
  UNC の -F を試す設定:   $WORK/hashigo-unc.yaml
  ssh.exe の鍵と config:  $WIN_DIR_W

手順は test/manual/README.md、片付けは test/manual/teardown.sh
MSG
