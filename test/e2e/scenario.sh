#!/bin/sh
# client コンテナ内で実行する E2E のシナリオ。
# e2e_test.go が start →（ap の再起動）→ reconnect → stop の順に docker compose exec で呼ぶ。
set -eu

CFG=/e2e/hashigo.yaml
LOG=/tmp/up.log
STATE_DIR="$HOME/.local/state/hashigo/run"

# busybox の pgrep -x は argv[0]（/usr/bin/ssh）と比べるので、-x ssh では一致しない
ssh_running() {
  pgrep -f '^/usr/bin/ssh ' >/dev/null
}

up_pid() {
  pgrep -f '^hashigo up ' || true
}

fail() {
  echo "NG: $1"
  cat "$LOG" 2>/dev/null || true
  ps
  exit 1
}

# echo_ok はポート $1 の先の db（TCP エコー）に届くかを返す
echo_ok() {
  got=$(echo ping | socat -t2 -T2 - TCP:127.0.0.1:"$1" 2>/dev/null || true)
  [ "$got" = "ping" ]
}

# wait_echo は最大 $2 秒、ポート $1 で db に届くまで待つ
wait_echo() {
  n=0
  while [ "$n" -lt "$2" ]; do
    if [ -z "$(up_pid)" ]; then
      fail "up が途中で終了した"
    fi
    if echo_ok "$1"; then
      echo "OK: $1 で db に届いた"
      return 0
    fi
    n=$((n + 1))
    sleep 1
  done
  fail "$1 で db に届かない"
}

check_status() {
  if ! out=$(hashigo status --config "$CFG" all); then
    echo "$out"
    fail "status の終了コードが 0 ではない"
  fi
  echo "$out"
}

phase_start() {
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

  # sshd の起動を待つ（3段目まで届けば全段そろっている）
  i=0
  until ssh -o BatchMode=yes -o ConnectTimeout=2 -J hashigo@bastion,hashigo@mid hashigo@ap true 2>/dev/null; do
    i=$((i + 1))
    if [ "$i" -ge 30 ]; then fail "sshd が起動しない"; fi
    sleep 1
  done

  # client は bastion にしか届かない。多段を経由しないと db に届かないことを確かめる
  if socat -T2 - TCP:db:5432 </dev/null >/dev/null 2>&1; then
    fail "client から db へ直接届いてしまう"
  fi
  for h in mid ap; do
    if ssh -o BatchMode=yes -o ConnectTimeout=2 hashigo@"$h" true >/dev/null 2>&1; then
      fail "client から $h へ直接 ssh できてしまう"
    fi
  done
  echo "OK: client から mid / ap / db へ直接は届かない"

  hashigo validate --config "$CFG"
  hashigo cmd --config "$CFG" all

  # 段階をまたいで動かすため、新しいセッションで背景に起動する
  setsid hashigo up --config "$CFG" all >"$LOG" 2>&1 </dev/null &
  # fork 直後、exec までの間は cmdline が親のままなので、up の pid が見えるまで待つ
  n=0
  while [ -z "$(up_pid)" ] && [ "$n" -lt 50 ]; do
    n=$((n + 1))
    sleep 0.1
  done
  wait_echo 15432 30
  wait_echo 25432 30
  if ! ssh_running; then fail "ssh が動いていない"; fi
  if ! ls "$STATE_DIR"/*.json >/dev/null 2>&1; then fail "状態ファイルが無い"; fi
  check_status
}

phase_reconnect() {
  # ap の再起動で、ap を経由する three-hop だけが切れ、バックオフのあとで張り直される
  n=0
  until grep -q '\[three-hop\] .*後に再接続します' "$LOG"; do
    n=$((n + 1))
    if [ "$n" -ge 60 ]; then fail "three-hop の再接続が始まらない"; fi
    sleep 1
  done
  wait_echo 25432 90
  if ! echo_ok 15432; then fail "two-hop が止まっている"; fi
  if grep -q '\[two-hop\] 終了しました' "$LOG"; then fail "ap の再起動で two-hop まで切れた"; fi
  check_status
  echo "OK: ap の再起動後に three-hop が張り直された"
}

phase_stop() {
  pid=$(up_pid)
  if [ -z "$pid" ]; then fail "up が動いていない"; fi
  kill -INT "$pid"
  n=0
  while kill -0 "$pid" 2>/dev/null; do
    n=$((n + 1))
    if [ "$n" -ge 15 ]; then
      kill -KILL "$pid"
      fail "Ctrl-C から 15 秒たっても up が終了しない"
    fi
    sleep 1
  done
  for t in two-hop three-hop; do
    if ! grep -q "\[$t\] 停止しました" "$LOG"; then fail "$t の停止が出力されない"; fi
  done
  if grep -q 'すべてのトンネルが終了しました' "$LOG"; then fail "Ctrl-C ではなく全トンネルの終了で止まった"; fi
  n=0
  while ssh_running && [ "$n" -lt 5 ]; do
    n=$((n + 1))
    sleep 1
  done
  if ssh_running; then fail "up の停止後に ssh が残っている"; fi
  if ls "$STATE_DIR"/*.json >/dev/null 2>&1; then fail "up の停止後に状態ファイルが残っている"; fi
  cat "$LOG"
  echo "E2E OK"
}

case "${1:-}" in
  start) phase_start ;;
  reconnect) phase_reconnect ;;
  stop) phase_stop ;;
  *)
    echo "使い方: scenario.sh start|reconnect|stop"
    exit 2
    ;;
esac
