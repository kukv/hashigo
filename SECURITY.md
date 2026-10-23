# Security Policy

## Supported versions

Only the latest release is supported. Fixes are not backported to older versions;
update to the newest release from the
[releases page](https://github.com/kukv/hashigo/releases), mise or `go install`.

## Reporting a vulnerability

Report privately through GitHub: open the **Security** tab of this repository and
choose **Report a vulnerability**. Please do not open a public issue.

Include what `hashigo version` prints, your operating system (and on WSL, the
networking mode), the command line you ran, what `hashigo cmd` prints for it, and the
part of the configuration that reproduces the problem. Replace real host names, IP
addresses and user names with placeholders.

## What counts as a security issue

hashigo runs `ssh` (and later `aws`) as child processes with arguments built from the
configuration, and opens local ports that reach into other networks. These count:

- **Argument or command injection.** A configuration value that makes hashigo pass an
  extra option, a `ProxyCommand` or any other command to `ssh` / `aws`, or run a program
  other than the one the configuration names.
- **Listening wider than intended.** A forward that listens on an address other than the
  one configured (by default `127.0.0.1`), so the port becomes reachable from the LAN.
- **Secrets leaking.** Private keys, passphrases, AWS credentials or SSO tokens appearing
  in hashigo's output or in any file hashigo writes.
- **A released binary that is not what this repository builds.**

Not a security issue:

- A tunnel that fails to start, reconnects badly or reports the wrong state, without the
  effects above. Open an ordinary issue.
- What the configuration explicitly asks for, such as `defaults.ssh` pointing at a
  binary of your choice, or `local: "0.0.0.0:8080"`. The configuration file is trusted
  input written by the person running hashigo.

## Handling

Reports are acknowledged and triaged by the maintainer. Once a fix is released, the
advisory is published with credit to the reporter unless anonymity is requested.
