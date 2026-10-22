#!/bin/sh
set -eu
mkdir -p /home/hashigo/.ssh
cp /keys/id_ed25519.pub /home/hashigo/.ssh/authorized_keys
chown -R hashigo:hashigo /home/hashigo/.ssh
chmod 700 /home/hashigo/.ssh
chmod 600 /home/hashigo/.ssh/authorized_keys
# Alpine の sshd_config は AllowTcpForwarding no なので、コマンドラインで上書きする
exec /usr/sbin/sshd -D -e \
  -o AllowTcpForwarding=yes \
  -o PasswordAuthentication=no \
  -o PermitRootLogin=no
