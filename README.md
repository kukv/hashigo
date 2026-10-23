# hashigo

<p align="center"><img src="docs/assets/hashigo_2.png" width="420" alt="hashigo"></p>

[日本語](README.ja.md)

A CLI that starts, stops, and monitors YAML-defined port forwards through multi-hop SSH
bastions and AWS SSM.

hashigo (梯子, "ladder") climbs bastion after bastion for you. It does not implement SSH
itself: it runs your `ssh` as a child process with the right `-J` and `-L` options, so
`~/.ssh/config`, `ProxyJump`, `ssh-agent` and your keys keep working as they are. On
WSL it runs Windows' `ssh.exe`, so the keys and the config stay on the Windows side and
survive rebuilding the WSL distribution.

> **Status: stabilization.** Reconnecting, health checks, `status`, `doctor`, ECS (SSM)
> forwards, the WSL relay mode and native Windows are supported. The manual acceptance
> steps for real machines are in [docs/acceptance.md](docs/acceptance.md).

## Requirements

- OpenSSH: `ssh` on Linux and macOS, `ssh.exe` (Windows OpenSSH) on Windows and WSL
- Keys that work without a prompt — an agent or an unencrypted key. hashigo runs `ssh`
  with `BatchMode=yes`, so it cannot answer a passphrase or host key prompt. Connect once
  with plain `ssh <host>` to record the host key first.
- On WSL: mirrored networking (`networkingMode=mirrored` in `%UserProfile%\.wslconfig`) is
  recommended. With NAT, hashigo uses the relay mode, which needs `socat` on the WSL side.
  `wsl.mode: native` uses the Linux `ssh` instead.
- For ECS tunnels: `aws` (`aws.exe` on WSL), `session-manager-plugin`, and a service with
  ECS Exec enabled.

## Install

### mise

[mise](https://mise.jdx.dev/) installs the released binary from GitHub Releases:

    mise use -g github:kukv/hashigo@latest

### Manual download

Grab the archive for your platform from the
[releases page](https://github.com/kukv/hashigo/releases), extract it, and put
`hashigo` somewhere on your `PATH`:

    tar xzf hashigo_<version>_linux_amd64.tar.gz
    install -m 0755 hashigo ~/.local/bin/

On Windows, unzip the archive and place `hashigo.exe` in a directory on `PATH`.

`checksums.txt` is published alongside the archives:

    sha256sum -c checksums.txt --ignore-missing

### go install

    go install github.com/kukv/hashigo/cmd/hashigo@latest

### Build from source

    git clone https://github.com/kukv/hashigo.git
    cd hashigo
    make build   # bin/hashigo

## Configuration

hashigo reads the first file it finds:

1. `--config <path>`
2. `hashigo.yaml` in the current directory
3. `$XDG_CONFIG_HOME/hashigo/config.yaml`, or `~/.config/hashigo/config.yaml` when that
   is unset (on Windows, `%USERPROFILE%\.config\hashigo\config.yaml`)
4. On WSL only: `.config/hashigo/config.yaml` under your Windows home, so WSL and
   Windows can share one file

```yaml
hosts:
  bastion: { host: bastion.example.com, user: ec2-user }
  inner:   { host: 10.0.1.10, user: admin, via: bastion }   # chain hops with via

tunnels:
  prod-db:
    via: inner                       # ssh -J ec2-user@bastion.example.com admin@10.0.1.10
    forwards:
      - 15432:db.internal:5432       # local-port:remote-host:remote-port
      - { local: "127.0.0.1:16379", remote: "redis.internal:6379" }
  web:
    host: app1                       # an alias from ~/.ssh/config works too
    forwards: ["8080:localhost:80"]

groups:
  prod: [prod-db, web]
```

Several environments go under `environments`. Names are referred to as `env.name`, and
`via` inside an environment looks in the same environment first, then at the top level:

```yaml
environments:
  dev:
    hosts:
      bastion: { host: dev-bastion.example.com, user: ec2-user }
    tunnels:
      db: { via: bastion, forwards: ["15432:dev-db.internal:5432"] }
  prod:
    protected: true                  # up asks before starting
    hosts:
      bastion: { host: prod-bastion.example.com, user: ec2-user }
    tunnels:
      db: { via: bastion, forwards: ["25432:prod-db.internal:5432"] }

groups:
  all-db: [dev.db, prod.db]
```

| Key | Where | Meaning |
|---|---|---|
| `include` | top level | Other files to merge, relative to the including file |
| `defaults` | top level | `ssh` (`auto` / `ssh` / `ssh.exe` / a path), `ssh_config` and `aws_config` (`auto` / `windows` / `wsl` / a path), `aws`, `keepalive` (default `30s`), `reconnect` (default `true`); inherited by the levels below |
| `wsl.mode` | top level | `auto` (default), `mirrored`, `relay`, `native` |
| `hosts.<name>` | top level, environment | `host` (required), `user`, `via` |
| `tunnels.<name>` | top level, environment | ssh: `via` or `host` (one of them), `forwards`, `hostname`, `keepalive`, `ssh`, `ssh_config`. ECS: `type: ecs`, `cluster`, `service`, `container`, `forwards`, `profile`, `region`, `aws`, `aws_config` |
| `groups.<name>` | top level | A list of `env.name`, environment names or top-level tunnel names |
| `environments.<name>` | top level | `protected`, `hosts`, `tunnels`, and settings inherited by its tunnels |

ECS tunnels connect through SSM Session Manager (ECS Exec). The task ID is looked up again
on every connection.

```yaml
environments:
  stg:
    aws: { profile: stg }            # inherited by the ECS tunnels of the environment
    tunnels:
      db:
        type: ecs
        region: ap-northeast-1
        cluster: stg-cluster
        service: api
        container: app
        forwards: ["35432:stg-db.internal:5432"]
```

Forwards listen on `127.0.0.1` unless you give an address. `hashigo validate` reports
every problem with `file:line:col`: unknown keys, missing references, `via` cycles, and
local ports used twice across all environments.

Keep a `hashigo.yaml` with real host names out of shared repositories.

## Usage

```sh
hashigo validate                 # check the configuration
hashigo cmd prod-db              # print the ssh / aws commands hashigo would run
hashigo up prod-db               # start the tunnels; Ctrl-C stops them all
hashigo up dev                   # every tunnel of an environment
hashigo up dev prod              # several at once
hashigo up dev.db                # one tunnel of an environment
hashigo up all-db --yes          # skip the confirmation for protected environments
hashigo status                   # show the state (exit code 0 when everything is up)
hashigo doctor                   # check the environment and the configuration
hashigo version
```

A target is matched as `env.name`, then an environment, then a group, then a top-level
tunnel. Every command takes `--config <path>`.

`up` runs in the foreground. It refuses to start when a local port is already in use and
names who uses it — another `hashigo up` or a process outside hashigo. When a tunnel drops,
it reconnects after 1s, 2s, 4s … up to 60s (not at all with `reconnect: false`). Every 10
seconds it checks that the local ports accept connections and reports `unhealthy` when they
do not. Failures that a retry cannot fix, such as an expired SSO session, stop the tunnel
with a hint like `aws sso login`. Ctrl-C, SIGTERM or closing the terminal stops every
process it started; on WSL it also cleans up `ssh.exe` / `aws.exe` left on the Windows side.

`status` shows the state of each tunnel, its reconnect count and its last error, from the
state files that `up` writes (`~/.local/state/hashigo/run/`, `%LOCALAPPDATA%\hashigo\run\`
on Windows) and a TCP check. `doctor` checks the WSL networking mode, whether ssh, aws,
session-manager-plugin and socat are available, AWS authentication, port conflicts, the
config files actually read, key files, missing hosts entries (for tunnels with
`hostname`) and LAN exposure in the relay mode.

## Roadmap

| Phase | Contents |
|---|---|
| MVP | `validate`, `cmd`, foreground `up`, environments and `include`, WSL `ssh.exe` (mirrored) |
| Stabilization (current) | Reconnect with backoff, health checks, `status`, `doctor`, ECS (SSM) forwards, WSL relay mode, `ssh_config` / `aws_config` switching, Windows native |
| Extensions | `up -d` / `down` / `logs`, `-R` and `-D` forwards, shell completion |

The details are in the
[design document](docs/superpowers/specs/2026-10-23-hashigo-design.md).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Security issues: [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE)
