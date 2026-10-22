#!/bin/sh
# client コンテナ内で実行する E2E のシナリオ。
set -eu

mkdir -p ~/.ssh
cp /keys/id_ed25519 ~/.ssh/id_ed25519
chmod 600 ~/.ssh/id_ed25519
cat > ~/.ssh/config <<'EOF'
Host *
  IdentityFile ~/.ssh/id_ed25519
  StrictHostKeyChecking accept-new
  UserKnownHostsFile /dev/null
  LogLevel ERROR
EOF

CFG=/e2e/hashigo.yaml

# busybox の pgrep -x は argv[0]（/usr/bin/ssh）と比べるので、-x ssh では一致しない
ssh_running() {
  pgrep -f '^/usr/bin/ssh ' >/dev/null
}

# sshd の起動を待つ（3段目まで届けば全段そろっている）
i=0
until ssh -o BatchMode=yes -o ConnectTimeout=2 -J hashigo@bastion,hashigo@mid hashigo@ap true 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 30 ]; then echo "NG: sshd が起動しない"; exit 1; fi
  sleep 1
done

# client は bastion にしか届かない。多段を経由しないと db に届かないことを確かめる
if socat -T2 - TCP:db:5432 </dev/null >/dev/null 2>&1; then
  echo "NG: client から db へ直接届いてしまう"
  exit 1
fi
for h in mid ap; do
  if ssh -o BatchMode=yes -o ConnectTimeout=2 hashigo@"$h" true >/dev/null 2>&1; then
    echo "NG: client から $h へ直接 ssh できてしまう"
    exit 1
  fi
done
echo "OK: client から mid / ap / db へ直接は届かない"

hashigo validate --config "$CFG"
hashigo cmd --config "$CFG" all

hashigo up --config "$CFG" all >/tmp/up.log 2>&1 &
pid=$!

check() {
  port=$1
  n=0
  while [ "$n" -lt 30 ]; do
    if ! kill -0 "$pid" 2>/dev/null; then
      echo "NG: up が途中で終了した"
      cat /tmp/up.log
      return 1
    fi
    got=$(echo ping | socat -t2 -T2 - TCP:127.0.0.1:"$port" 2>/dev/null || true)
    if [ "$got" = "ping" ]; then
      echo "OK: $port で db に届いた"
      return 0
    fi
    n=$((n + 1))
    sleep 1
  done
  echo "NG: $port で db に届かない"
  cat /tmp/up.log
  return 1
}
check 15432
check 25432

if ! ssh_running; then
  echo "NG: 停止前に ssh が動いていない"
  cat /tmp/up.log
  ps
  exit 1
fi

kill -INT "$pid"
n=0
while kill -0 "$pid" 2>/dev/null; do
  n=$((n + 1))
  if [ "$n" -ge 15 ]; then
    echo "NG: Ctrl-C から 15 秒たっても up が終了しない"
    cat /tmp/up.log
    kill -KILL "$pid"
    exit 1
  fi
  sleep 1
done
if ! wait "$pid"; then
  echo "NG: Ctrl-C 後の up の終了コードが 0 ではない"
  cat /tmp/up.log
  exit 1
fi
n=0
while ssh_running && [ "$n" -lt 5 ]; do
  n=$((n + 1))
  sleep 1
done
if ssh_running; then
  echo "NG: up の停止後に ssh が残っている"
  ps
  exit 1
fi
cat /tmp/up.log
echo "E2E OK"
