# hashigo MVP 実装プラン

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** YAML に定義した多段 SSH のポートフォワードを `hashigo up` で一括起動し、Ctrl-C で一括停止できる CLI（MVP）を作る。`validate` / `cmd` / `version` も含める。

**Architecture:** config（読み込み・include・正規化・名前解決・検証）→ driver（ssh の argv を組み立てる）→ supervisor（子プロセスの起動・監視・停止）→ platform（WSL 判定・バイナリ選択・OS ごとのプロセス操作）の4層。cli は cobra で薄く保つ。

**Tech Stack:** Go 1.27、`github.com/spf13/cobra`、`github.com/goccy/go-yaml`、`golang.org/x/term`、Docker Compose（E2E）、GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-10-23-hashigo-design.md`

## Global Constraints

- モジュールパス: `github.com/kukv/hashigo`。`go.mod` の go ディレクティブは `go 1.27`
- ユーザー向けのメッセージ、コメントは日本語。識別子は英語
- 子プロセスの ssh には常に `-N -o ExitOnForwardFailure=yes -o ServerAliveInterval=<keepalive秒> -o ServerAliveCountMax=3 -o BatchMode=yes` を付ける
- 待ち受けアドレスの既定は `127.0.0.1`。keepalive の既定は `30s`
- 外部コマンドは `platform.Runner` を通して実行する（テストで差し替えるため）
- MVP で未対応のもの（`type: ecs` の起動、`ssh_config` / `aws_config` の auto 以外、relay モード）は、設定としては受け付け、`up` / `cmd` で「安定化フェーズで対応します」というエラーにする
- MVP では再接続しない。プロセスが終了したらそのスロットを終了扱いにし、他は動かし続ける。全スロットが終了したら `up` は終了コード 1 で終わる
- テストは `go test ./...` で全 OS（ubuntu / macos / windows）で通ること。E2E は `-tags e2e` で分ける
- コミットメッセージの末尾に `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` を付ける

## ファイル構成

```
cmd/hashigo/main.go                 エントリポイント
internal/cli/root.go                Deps, Run, ルートコマンド, 共通の読み込み
internal/cli/version.go             version
internal/cli/validate.go            validate
internal/cli/build.go               トンネル → ドライバの組み立て
internal/cli/cmd.go                 cmd, シェル用のクォート
internal/cli/up.go                  up, protected の確認, ポートチェック
internal/config/forward.go          forwards の解釈
internal/config/model.go            Config / Env / Host / Tunnel など
internal/config/raw.go              YAML デコード用の型
internal/config/problem.go          Problem, 位置の特定
internal/config/load.go             Load, include, マージ
internal/config/resolve.go          LookupHost, Route, Select
internal/config/effective.go        継承の解決
internal/config/validate.go         検証
internal/config/search.go           設定ファイルの探索
internal/driver/driver.go           ProcessSpec, Driver
internal/driver/ssh/ssh.go          SSH ドライバ
internal/platform/host.go           Host, Runner, WSL 判定, バイナリ選択
internal/platform/proc_unix.go      Setpgid, SIGTERM/SIGKILL
internal/platform/proc_windows.go   Windows 用（MVP は最小限）
internal/supervisor/supervisor.go   スロットの起動・監視・停止
internal/testutil/net.go            FreePort, WaitPort, SyncBuffer
internal/testutil/fakebin/fakebin.go 偽 ssh
test/e2e/...                        Docker E2E
.github/workflows/ci.yml            CI
Makefile, .gitignore, README.md
```

---

### Task 1: プロジェクトの雛形と `version`

**Files:**
- Create: `go.mod`, `cmd/hashigo/main.go`, `internal/cli/root.go`, `internal/cli/version.go`, `Makefile`, `.gitignore`
- Test: `internal/cli/version_test.go`

**Interfaces:**
- Produces: `cli.Deps{Host *platform.Host; Stdin io.Reader; Stdout, Stderr io.Writer; IsTTY func() bool}`、`cli.Run(ctx context.Context, d Deps, args []string) int`。この時点の `platform.Host` は空の struct（Task 2 で中身を作る）

- [ ] **Step 1: モジュールを初期化する**

```bash
go mod init github.com/kukv/hashigo
go mod edit -go=1.27
go get github.com/spf13/cobra@latest golang.org/x/term@latest
mkdir -p internal/platform && printf 'package platform\n\n// Host は実行環境への窓口。Task 2 で中身を作る。\ntype Host struct{}\n' > internal/platform/host.go
```

- [ ] **Step 2: 失敗するテストを書く**

`internal/cli/version_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	code := Run(context.Background(), Deps{Stdout: &out, Stderr: io.Discard}, []string{"version"})
	if code != 0 {
		t.Fatalf("終了コード = %d, want 0", code)
	}
	got := strings.TrimSpace(out.String())
	if got == "" || strings.Contains(got, "\n") {
		t.Fatalf("version の出力 = %q, want 1行", got)
	}
}
```

- [ ] **Step 3: 失敗を確認する**

Run: `go test ./internal/cli/ -run TestVersion -v`
Expected: FAIL（`undefined: Run`）

- [ ] **Step 4: 実装する**

`internal/cli/root.go`:

```go
// Package cli は hashigo のサブコマンドを定義する。
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/platform"
)

// Deps はコマンドが使う外部とのつなぎ目。テストで差し替える。
type Deps struct {
	Host   *platform.Host
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	IsTTY  func() bool
}

type app struct {
	d          Deps
	configPath string
}

// Run はコマンドを実行し、終了コードを返す。
func Run(ctx context.Context, d Deps, args []string) int {
	a := &app{d: d}
	root := &cobra.Command{
		Use:           "hashigo",
		Short:         "多段 SSH のポートフォワードをまとめて起動・停止・監視する",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&a.configPath, "config", "", "設定ファイルのパス")
	root.SetArgs(args)
	root.SetIn(d.Stdin)
	root.SetOut(d.Stdout)
	root.SetErr(d.Stderr)
	root.AddCommand(newVersionCmd())

	if err := root.ExecuteContext(ctx); err != nil {
		printError(d.Stderr, err)
		return 1
	}
	return 0
}

func printError(w io.Writer, err error) {
	var multi interface{ Lines() []string }
	if errors.As(err, &multi) {
		for _, l := range multi.Lines() {
			fmt.Fprintln(w, l)
		}
		return
	}
	fmt.Fprintln(w, "エラー:", err)
}
```

`internal/cli/version.go`:

```go
package cli

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version はリリースビルドで -ldflags "-X github.com/kukv/hashigo/internal/cli.Version=v1.2.3" により設定する。
var Version = ""

func version() string {
	if Version != "" {
		return Version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "バージョンを表示する",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			fmt.Fprintln(c.OutOrStdout(), version())
			return nil
		},
	}
}
```

`cmd/hashigo/main.go`:

```go
package main

import (
	"context"
	"os"

	"golang.org/x/term"

	"github.com/kukv/hashigo/internal/cli"
	"github.com/kukv/hashigo/internal/platform"
)

func main() {
	d := cli.Deps{
		Host:   &platform.Host{},
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		IsTTY:  func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
	}
	os.Exit(cli.Run(context.Background(), d, os.Args[1:]))
}
```

`Makefile`:

```makefile
.PHONY: build test e2e

build:
	go build -o bin/hashigo ./cmd/hashigo

test:
	go vet ./...
	go test ./...

e2e:
	go test -tags e2e -count=1 -v ./test/e2e/...
```

`.gitignore`:

```
/bin/
/test/e2e/bin/
```

- [ ] **Step 5: テストが通ることを確認する**

Run: `go test ./internal/cli/ -run TestVersion -v && go build ./...`
Expected: PASS、ビルド成功

- [ ] **Step 6: Commit**

```bash
go mod tidy
git add go.mod go.sum cmd internal Makefile .gitignore
git commit -m "feat: プロジェクトの雛形と version コマンドを追加"
```

---

### Task 2: platform — WSL 判定とバイナリの選択

**Files:**
- Modify: `internal/platform/host.go`（全体を書き換える）
- Modify: `cmd/hashigo/main.go`（`&platform.Host{}` → `platform.Detect()`）
- Test: `internal/platform/host_test.go`

**Interfaces:**
- Produces:
  - `type Runner interface { Output(ctx context.Context, name string, args ...string) ([]byte, error) }`、`type ExecRunner struct{}`
  - `type Host struct { GOOS string; Getenv func(string) string; ReadFile func(string) ([]byte, error); LookPath func(string) (string, error); Stat func(string) (fs.FileInfo, error); HomeDir func() (string, error); Run Runner }`
  - `func Detect() *Host`
  - `func (h *Host) IsWSL() bool`
  - `func (h *Host) WindowsHome(ctx context.Context) (string, error)` — WSL 形式のパス（`/mnt/c/Users/x`）
  - `func (h *Host) NetworkMode(ctx context.Context) string` — `"mirrored"` / `"nat"` など
  - `func (h *Host) ResolveWSLMode(ctx context.Context, setting string) (string, error)` — 非 WSL では `""`
  - `func (h *Host) SSHBinary(setting, wslMode string) (string, error)`
  - `func (h *Host) SSHConfigPath(ctx context.Context, bin string) (string, error)`

- [ ] **Step 1: 失敗するテストを書く**

`internal/platform/host_test.go`:

```go
package platform

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner map[string]string

func (f fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	out, ok := f[key]
	if !ok {
		return nil, fmt.Errorf("想定外の呼び出し: %s", key)
	}
	return []byte(out), nil
}

func newHost(env map[string]string, files map[string]string, paths map[string]string, run fakeRunner) *Host {
	return &Host{
		GOOS:   "linux",
		Getenv: func(k string) string { return env[k] },
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
		LookPath: func(name string) (string, error) {
			if p, ok := paths[name]; ok {
				return p, nil
			}
			return "", exec.ErrNotFound
		},
		Stat: func(p string) (fs.FileInfo, error) {
			if _, ok := files[p]; ok {
				return nil, nil
			}
			return nil, fs.ErrNotExist
		},
		HomeDir: func() (string, error) { return "/home/u", nil },
		Run:     run,
	}
}

var wslRun = fakeRunner{
	"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
	"wslpath -u C:\\Users\\taro":     "/mnt/c/Users/taro\n",
}

func TestIsWSL(t *testing.T) {
	if !newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, nil).IsWSL() {
		t.Error("WSL_DISTRO_NAME があれば WSL")
	}
	if !newHost(nil, map[string]string{"/proc/version": "Linux version 6.18 (microsoft-standard-WSL2)"}, nil, nil).IsWSL() {
		t.Error("/proc/version に microsoft があれば WSL")
	}
	if newHost(nil, map[string]string{"/proc/version": "Linux version 6.8 (ubuntu)"}, nil, nil).IsWSL() {
		t.Error("ふつうの Linux は WSL ではない")
	}
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, nil)
	h.GOOS = "darwin"
	if h.IsWSL() {
		t.Error("linux 以外は WSL ではない")
	}
}

func TestWindowsHome(t *testing.T) {
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, wslRun)
	got, err := h.WindowsHome(context.Background())
	if err != nil || got != "/mnt/c/Users/taro" {
		t.Fatalf("WindowsHome = %q, %v", got, err)
	}
}

func TestResolveWSLMode(t *testing.T) {
	ctx := context.Background()
	wslEnv := map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}

	mirrored := fakeRunner{"wslinfo --networking-mode": "mirrored\n"}
	if got, _ := newHost(wslEnv, nil, nil, mirrored).ResolveWSLMode(ctx, "auto"); got != "mirrored" {
		t.Errorf("wslinfo が mirrored → %q", got)
	}

	// wslinfo が無いときは .wslconfig を読む
	files := map[string]string{"/mnt/c/Users/taro/.wslconfig": "[wsl2]\nnetworkingMode=mirrored\n"}
	if got, _ := newHost(wslEnv, files, nil, wslRun).ResolveWSLMode(ctx, ""); got != "mirrored" {
		t.Errorf(".wslconfig が mirrored → %q", got)
	}

	if got, _ := newHost(wslEnv, nil, nil, wslRun).ResolveWSLMode(ctx, "auto"); got != "relay" {
		t.Errorf("判定できなければ relay → %q", got)
	}
	if got, _ := newHost(wslEnv, nil, nil, nil).ResolveWSLMode(ctx, "native"); got != "native" {
		t.Errorf("明示した値はそのまま → %q", got)
	}
	if got, _ := newHost(nil, nil, nil, nil).ResolveWSLMode(ctx, "auto"); got != "" {
		t.Errorf("WSL でなければ空 → %q", got)
	}
}

func TestSSHBinary(t *testing.T) {
	wslEnv := map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}
	paths := map[string]string{"ssh": "/usr/bin/ssh", "ssh.exe": "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"}

	cases := []struct {
		name    string
		env     map[string]string
		paths   map[string]string
		files   map[string]string
		setting string
		mode    string
		want    string
		wantErr bool
	}{
		{name: "非WSLのauto", paths: paths, setting: "auto", want: "/usr/bin/ssh"},
		{name: "WSLのauto", env: wslEnv, paths: paths, setting: "auto", mode: "mirrored", want: "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"},
		{name: "WSLのnative", env: wslEnv, paths: paths, setting: "", mode: "native", want: "/usr/bin/ssh"},
		{name: "名前の指定", env: wslEnv, paths: paths, setting: "ssh", mode: "mirrored", want: "/usr/bin/ssh"},
		{name: "PATHに無いssh.exe", env: wslEnv, files: map[string]string{windowsSSH: ""}, setting: "auto", mode: "mirrored", want: windowsSSH},
		{name: "パスの指定", files: map[string]string{"/opt/ssh": ""}, setting: "/opt/ssh", want: "/opt/ssh"},
		{name: "存在しないパス", setting: "/nope/ssh", wantErr: true},
		{name: "見つからない", setting: "auto", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := newHost(c.env, c.files, c.paths, nil).SSHBinary(c.setting, c.mode)
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("SSHBinary = %q, %v; want %q (err=%v)", got, err, c.want, c.wantErr)
			}
		})
	}
}

func TestSSHConfigPath(t *testing.T) {
	ctx := context.Background()
	wsl := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, wslRun)
	if got, _ := wsl.SSHConfigPath(ctx, "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"); got != "/mnt/c/Users/taro/.ssh/config" {
		t.Errorf("ssh.exe → %q", got)
	}
	plain := newHost(nil, nil, nil, nil)
	if got, _ := plain.SSHConfigPath(ctx, "/usr/bin/ssh"); got != filepath.Join("/home/u", ".ssh", "config") {
		t.Errorf("ssh → %q", got)
	}
}

func TestDetect(t *testing.T) {
	h := Detect()
	if h.GOOS == "" || h.Getenv == nil || h.Run == nil {
		t.Fatal("Detect は全フィールドを埋める")
	}
	_ = errors.Is(nil, os.ErrNotExist)
}
```

- [ ] **Step 2: 失敗を確認する**

Run: `go test ./internal/platform/ -v`
Expected: FAIL（`undefined: Detect` など）

- [ ] **Step 3: 実装する**

`internal/platform/host.go`:

```go
// Package platform は OS と WSL の差分を引き受ける。
package platform

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Runner は外部コマンドを実行して標準出力を返す。
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner は os/exec で実際に実行する Runner。
type ExecRunner struct{}

func (ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

// Host は実行環境への窓口。テストでは各関数を差し替える。
type Host struct {
	GOOS     string
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	LookPath func(string) (string, error)
	Stat     func(string) (fs.FileInfo, error)
	HomeDir  func() (string, error)
	Run      Runner

	winHome string // WindowsHome の結果を覚えておく
}

// Detect は実際の実行環境の Host を返す。
func Detect() *Host {
	return &Host{
		GOOS:     runtime.GOOS,
		Getenv:   os.Getenv,
		ReadFile: os.ReadFile,
		LookPath: exec.LookPath,
		Stat:     os.Stat,
		HomeDir:  os.UserHomeDir,
		Run:      ExecRunner{},
	}
}

// IsWSL は WSL 上で動いているかを返す。
func (h *Host) IsWSL() bool {
	if h.GOOS != "linux" {
		return false
	}
	if h.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	b, err := h.ReadFile("/proc/version")
	return err == nil && strings.Contains(strings.ToLower(string(b)), "microsoft")
}

// WindowsHome は Windows のユーザーホームを WSL 形式のパスで返す。
func (h *Host) WindowsHome(ctx context.Context) (string, error) {
	if h.winHome != "" {
		return h.winHome, nil
	}
	out, err := h.Run.Output(ctx, "cmd.exe", "/c", "echo %USERPROFILE%")
	if err != nil {
		return "", fmt.Errorf("Windows のホームを取得できません: %w", err)
	}
	win := strings.TrimSpace(string(out))
	if win == "" || strings.Contains(win, "%") {
		return "", errors.New("Windows のホームを取得できません: USERPROFILE が空です")
	}
	out, err = h.Run.Output(ctx, "wslpath", "-u", win)
	if err != nil {
		return "", fmt.Errorf("wslpath で %s を変換できません: %w", win, err)
	}
	h.winHome = strings.TrimSpace(string(out))
	return h.winHome, nil
}

var wslconfigMode = regexp.MustCompile(`(?im)^\s*networkingMode\s*=\s*(\w+)`)

// NetworkMode は WSL のネットワークモードを返す。判定できなければ "nat"。
func (h *Host) NetworkMode(ctx context.Context) string {
	if out, err := h.Run.Output(ctx, "wslinfo", "--networking-mode"); err == nil {
		if m := strings.ToLower(strings.TrimSpace(string(out))); m != "" {
			return m
		}
	}
	if home, err := h.WindowsHome(ctx); err == nil {
		if b, err := h.ReadFile(path.Join(home, ".wslconfig")); err == nil {
			if m := wslconfigMode.FindSubmatch(b); m != nil {
				return strings.ToLower(string(m[1]))
			}
		}
	}
	return "nat"
}

// ResolveWSLMode は wsl.mode の設定値から実際のモードを決める。WSL でなければ "" を返す。
func (h *Host) ResolveWSLMode(ctx context.Context, setting string) (string, error) {
	if !h.IsWSL() {
		return "", nil
	}
	switch setting {
	case "", "auto":
		if h.NetworkMode(ctx) == "mirrored" {
			return "mirrored", nil
		}
		return "relay", nil
	case "mirrored", "relay", "native":
		return setting, nil
	}
	return "", fmt.Errorf("wsl.mode は auto / mirrored / relay / native のいずれかです: %q", setting)
}

const windowsSSH = "/mnt/c/Windows/System32/OpenSSH/ssh.exe"

// SSHBinary は ssh の設定値と WSL のモードから、実行する ssh のパスを決める。
func (h *Host) SSHBinary(setting, wslMode string) (string, error) {
	name := setting
	switch setting {
	case "", "auto":
		name = "ssh"
		if h.IsWSL() && wslMode != "native" {
			name = "ssh.exe"
		}
	case "ssh", "ssh.exe":
	default:
		if _, err := h.Stat(setting); err != nil {
			return "", fmt.Errorf("ssh に指定したファイルがありません: %s", setting)
		}
		return setting, nil
	}
	if p, err := h.LookPath(name); err == nil {
		return p, nil
	}
	if name == "ssh.exe" && h.IsWSL() {
		if _, err := h.Stat(windowsSSH); err == nil {
			return windowsSSH, nil
		}
	}
	return "", fmt.Errorf("%s が見つかりません", name)
}

// SSHConfigPath は、そのバイナリが既定で読む ssh config のパスを返す（表示用）。
func (h *Host) SSHConfigPath(ctx context.Context, bin string) (string, error) {
	if h.IsWSL() && strings.HasSuffix(strings.ToLower(bin), ".exe") {
		home, err := h.WindowsHome(ctx)
		if err != nil {
			return "", err
		}
		return path.Join(home, ".ssh", "config"), nil
	}
	home, err := h.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ssh", "config"), nil
}
```

`cmd/hashigo/main.go` の `Host: &platform.Host{},` を `Host: platform.Detect(),` に変える。

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/platform/ -v && go build ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/platform cmd/hashigo/main.go
git commit -m "feat: WSL 判定と ssh バイナリの選択を追加"
```

---

### Task 3: config — forwards の解釈

**Files:**
- Create: `internal/config/forward.go`
- Test: `internal/config/forward_test.go`

**Interfaces:**
- Produces: `type Forward struct { Bind string; LocalPort int; RemoteHost string; RemotePort int }`、`func (f Forward) LocalAddr() string`、`func ParseForward(s string) (Forward, error)`、`func parseDetailedForward(local, remote string) (Forward, error)`、`func parsePort(s string) (int, error)`、`const defaultBind = "127.0.0.1"`

- [ ] **Step 1: 失敗するテストを書く**

`internal/config/forward_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

func TestParseForward(t *testing.T) {
	got, err := ParseForward("15432:db.internal:5432")
	want := Forward{Bind: "127.0.0.1", LocalPort: 15432, RemoteHost: "db.internal", RemotePort: 5432}
	if err != nil || got != want {
		t.Fatalf("ParseForward = %+v, %v", got, err)
	}
	for _, bad := range []string{"15432:db", "a:db:5432", "15432::5432", "99999:db:1", "1:db:0"} {
		if _, err := ParseForward(bad); err == nil {
			t.Errorf("%q はエラーになるべき", bad)
		}
	}
}

func TestParseDetailedForward(t *testing.T) {
	cases := []struct {
		local, remote string
		want          Forward
	}{
		{"15432", "db:5432", Forward{"127.0.0.1", 15432, "db", 5432}},
		{"0.0.0.0:8080", "localhost:80", Forward{"0.0.0.0", 8080, "localhost", 80}},
		{"[::1]:8080", "[fd00::1]:80", Forward{"::1", 8080, "fd00::1", 80}},
	}
	for _, c := range cases {
		got, err := parseDetailedForward(c.local, c.remote)
		if err != nil || got != c.want {
			t.Errorf("parseDetailedForward(%q, %q) = %+v, %v", c.local, c.remote, got, err)
		}
	}
	if _, err := parseDetailedForward("", "db:1"); err == nil || !strings.Contains(err.Error(), "local") {
		t.Errorf("local が無ければエラー: %v", err)
	}
	if _, err := parseDetailedForward("1", "db"); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Errorf("remote の形式エラー: %v", err)
	}
}

func TestLocalAddr(t *testing.T) {
	if got := (Forward{Bind: "::1", LocalPort: 80}).LocalAddr(); got != "[::1]:80" {
		t.Errorf("LocalAddr = %q", got)
	}
}
```

- [ ] **Step 2: 失敗を確認する**

Run: `go test ./internal/config/ -v`
Expected: FAIL（`undefined: ParseForward`）

- [ ] **Step 3: 実装する**

`internal/config/forward.go`:

```go
// Package config は設定ファイルの読み込み・正規化・名前解決・検証を行う。
package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const defaultBind = "127.0.0.1"

// Forward はローカルポートから宛先への転送1本。
type Forward struct {
	Bind       string
	LocalPort  int
	RemoteHost string
	RemotePort int
}

// LocalAddr は待ち受けアドレスを host:port の形で返す。
func (f Forward) LocalAddr() string {
	return net.JoinHostPort(f.Bind, strconv.Itoa(f.LocalPort))
}

// ParseForward は短縮記法 ローカルポート:宛先ホスト:宛先ポート を解釈する。
func ParseForward(s string) (Forward, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return Forward{}, fmt.Errorf("forwards は ローカルポート:宛先ホスト:宛先ポート の形式で書きます: %q", s)
	}
	lp, err := parsePort(parts[0])
	if err != nil {
		return Forward{}, err
	}
	if parts[1] == "" {
		return Forward{}, fmt.Errorf("forwards の宛先ホストが空です: %q", s)
	}
	rp, err := parsePort(parts[2])
	if err != nil {
		return Forward{}, err
	}
	return Forward{Bind: defaultBind, LocalPort: lp, RemoteHost: parts[1], RemotePort: rp}, nil
}

// parseDetailedForward は詳細記法 {local, remote} を解釈する。local はポート番号か アドレス:ポート。
func parseDetailedForward(local, remote string) (Forward, error) {
	f := Forward{Bind: defaultBind}
	if local == "" {
		return Forward{}, fmt.Errorf("forwards に local がありません")
	}
	if strings.Contains(local, ":") {
		h, p, err := net.SplitHostPort(local)
		if err != nil {
			return Forward{}, fmt.Errorf("local は ポート番号 か アドレス:ポート で書きます: %q", local)
		}
		if h != "" {
			f.Bind = h
		}
		local = p
	}
	lp, err := parsePort(local)
	if err != nil {
		return Forward{}, err
	}
	h, p, err := net.SplitHostPort(remote)
	if err != nil || h == "" {
		return Forward{}, fmt.Errorf("remote は 宛先ホスト:宛先ポート の形式で書きます: %q", remote)
	}
	rp, err := parsePort(p)
	if err != nil {
		return Forward{}, err
	}
	f.LocalPort, f.RemoteHost, f.RemotePort = lp, h, rp
	return f, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("ポート番号は 1〜65535 の整数で書きます: %q", s)
	}
	return n, nil
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat: forwards の短縮記法と詳細記法を解釈する"
```

---

### Task 4: config — モデル、YAML デコード、単一ファイルの Load

**Files:**
- Create: `internal/config/model.go`, `internal/config/raw.go`, `internal/config/problem.go`, `internal/config/load.go`
- Test: `internal/config/load_test.go`, `internal/config/helpers_test.go`

**Interfaces:**
- Consumes: Task 3 の `Forward`, `ParseForward`, `parseDetailedForward`
- Produces:
  - `type Settings struct { SSH, SSHConfig, AWS, AWSConfig, Keepalive string; Reconnect *bool; Profile, Region string }`
  - `type Source struct { File string; Path []any }`、`func (s Source) child(keys ...any) Source`
  - `type Host struct { Name, Env, Host, User, Via string; Src Source }`、`func (h *Host) ID() string`
  - `type Tunnel struct { Name, Env, Type, Host, Via, Cluster, Service, Container, Hostname string; Forwards []Forward; Settings Settings; Src Source }`、`func (t *Tunnel) ID() string`
  - `type Env struct { Name string; Protected bool; Settings Settings; Hosts map[string]*Host; Tunnels map[string]*Tunnel; Src Source }`、`func (e *Env) sortedTunnels() []*Tunnel`
  - `type Group struct { Name string; Members []string; Src Source }`
  - `type Config struct { Files []string; Defaults Settings; WSLMode string; Envs map[string]*Env; Groups map[string]*Group; defaultsSrc, wslSrc Source }`、`func (c *Config) Tunnels() []*Tunnel`
  - `type Problem struct { File string; Line, Col int; Msg string; path []any }`、`func (p Problem) String() string`
  - `type Problems []Problem`（`error` を実装、`Lines() []string` を持つ）
  - `func Load(path, goos string) (*Config, error)` — 問題があれば `Problems` を返す

- [ ] **Step 1: goccy/go-yaml を追加する**

```bash
go get github.com/goccy/go-yaml@latest
```

- [ ] **Step 2: 失敗するテストを書く**

`internal/config/helpers_test.go`:

```go
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// loadString は YAML 文字列を一時ファイルに書いて Load する。
func loadString(t *testing.T, yaml, goos string) (*Config, Problems) {
	t.Helper()
	p := writeFile(t, t.TempDir(), "hashigo.yaml", yaml)
	cfg, err := Load(p, goos)
	if err == nil {
		return cfg, nil
	}
	var ps Problems
	if !errors.As(err, &ps) {
		t.Fatalf("Load のエラーが Problems ではない: %v", err)
	}
	return nil, ps
}

func mustLoad(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, ps := loadString(t, yaml, "linux")
	if ps != nil {
		t.Fatalf("想定外の問題:\n%s", ps.Error())
	}
	return cfg
}

func wantProblem(t *testing.T, ps Problems, substr string) {
	t.Helper()
	for _, p := range ps {
		if strings.Contains(p.Msg, substr) {
			return
		}
	}
	t.Errorf("%q を含む問題がない。実際:\n%s", substr, ps.Error())
}
```

`internal/config/load_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

const rootYAML = `
defaults:
  ssh: auto
  keepalive: 30s
  reconnect: true
wsl:
  mode: auto
hosts:
  bastion:
    host: bastion.example.com
    user: ec2-user
  inner:
    host: 10.0.1.10
    user: admin
    via: bastion
tunnels:
  prod-db:
    via: inner
    forwards:
      - 15432:db.internal:5432
      - 16379:redis.internal:6379
  web:
    host: app1
    forwards:
      - { local: "127.0.0.1:8080", remote: "localhost:80" }
groups:
  prod: [prod-db, web]
`

func TestLoadRoot(t *testing.T) {
	cfg := mustLoad(t, rootYAML)
	root := cfg.Envs[""]
	if root == nil {
		t.Fatal("ルート環境が無い")
	}
	db := root.Tunnels["prod-db"]
	if db == nil || db.Type != "ssh" || db.Via != "inner" || len(db.Forwards) != 2 {
		t.Fatalf("prod-db = %+v", db)
	}
	if db.ID() != "prod-db" {
		t.Errorf("ID = %q", db.ID())
	}
	if f := root.Tunnels["web"].Forwards[0]; f.LocalPort != 8080 || f.RemoteHost != "localhost" {
		t.Errorf("web の forward = %+v", f)
	}
	if h := root.Hosts["inner"]; h.User != "admin" || h.Via != "bastion" {
		t.Errorf("inner = %+v", h)
	}
	if cfg.Defaults.Keepalive != "30s" || cfg.WSLMode != "auto" {
		t.Errorf("defaults = %+v, wsl = %q", cfg.Defaults, cfg.WSLMode)
	}
	if g := cfg.Groups["prod"]; g == nil || strings.Join(g.Members, ",") != "prod-db,web" {
		t.Errorf("groups = %+v", cfg.Groups)
	}
	if len(cfg.Files) != 1 {
		t.Errorf("Files = %v", cfg.Files)
	}
}

func TestLoadEnvironments(t *testing.T) {
	cfg := mustLoad(t, `
environments:
  prod:
    protected: true
    ssh_config: wsl
    aws:
      profile: prod
    hosts:
      bastion: { host: prod-bastion.example.com, user: ec2-user }
    tunnels:
      db:
        via: bastion
        forwards:
          - 25432:prod-db.internal:5432
`)
	prod := cfg.Envs["prod"]
	if prod == nil || !prod.Protected || prod.Settings.SSHConfig != "wsl" || prod.Settings.Profile != "prod" {
		t.Fatalf("prod = %+v", prod)
	}
	if db := prod.Tunnels["db"]; db.ID() != "prod.db" || db.Env != "prod" {
		t.Errorf("db = %+v", db)
	}
	if prod.Hosts["bastion"].ID() != "prod.bastion" {
		t.Errorf("host ID = %q", prod.Hosts["bastion"].ID())
	}
}

func TestLoadAWSForms(t *testing.T) {
	cfg := mustLoad(t, `
defaults:
  aws: aws.exe
environments:
  stg:
    tunnels:
      db:
        type: ecs
        profile: stg
        region: ap-northeast-1
        cluster: c
        service: s
        container: app
        forwards: ["35432:db:5432"]
`)
	if cfg.Defaults.AWS != "aws.exe" {
		t.Errorf("defaults.aws = %q", cfg.Defaults.AWS)
	}
	db := cfg.Envs["stg"].Tunnels["db"]
	if db.Type != "ecs" || db.Settings.Profile != "stg" || db.Settings.Region != "ap-northeast-1" {
		t.Errorf("db = %+v", db)
	}

	_, ps := loadString(t, `
environments:
  stg:
    profile: a
    aws: { profile: b }
`, "linux")
	wantProblem(t, ps, "profile と aws.profile は同時に書けません")
}

func TestLoadProblems(t *testing.T) {
	_, ps := loadString(t, "tunnels:\n  t1:\n    hots: x\n", "linux")
	if len(ps) == 0 || !strings.Contains(ps[0].String(), "hashigo.yaml:3:") {
		t.Fatalf("未知のキーは3行目を指す: %v", ps)
	}

	_, ps = loadString(t, "tunnels:\n  t1:\n    host: x\n    forwards:\n      - 15432:db\n      - { local: 1, remote: db, extra: 1 }\n", "linux")
	wantProblem(t, ps, "ローカルポート:宛先ホスト:宛先ポート")
	wantProblem(t, ps, `未知のキー "extra"`)
	if !strings.Contains(ps[0].String(), "hashigo.yaml:5:") {
		t.Errorf("forwards の問題は該当行を指す: %s", ps[0].String())
	}

	_, ps = loadString(t, "tunnels:\n  t1:\n    host: x\n", "linux")
	wantProblem(t, ps, "forwards がありません")

	_, ps = loadString(t, "defaults:\n  profile: x\n", "linux")
	wantProblem(t, ps, "defaults には profile / region を書けません")
}
```

- [ ] **Step 3: 失敗を確認する**

Run: `go test ./internal/config/ -v`
Expected: FAIL（`undefined: Load` など）

- [ ] **Step 4: 実装する**

`internal/config/model.go`:

```go
package config

import "sort"

// Settings は defaults / 環境 / トンネルで上書きできる設定。空文字と nil は未指定を表す。
type Settings struct {
	SSH       string
	SSHConfig string
	AWS       string // aws バイナリ
	AWSConfig string
	Keepalive string
	Reconnect *bool
	Profile   string
	Region    string
}

// Source は定義元のファイルと YAML 上のパス。Path の要素は string（キー）か int（添字）。
type Source struct {
	File string
	Path []any
}

func (s Source) child(keys ...any) Source {
	p := append(append([]any{}, s.Path...), keys...)
	return Source{File: s.File, Path: p}
}

// Host は hosts の1件。
type Host struct {
	Name, Env       string
	Host, User, Via string
	Src             Source
}

// ID は外から参照するときの名前（環境.名前、ルート環境なら名前）。
func (h *Host) ID() string { return qualify(h.Env, h.Name) }

// Tunnel は tunnels の1件。Type は "ssh" か "ecs"。
type Tunnel struct {
	Name, Env                   string
	Type                        string
	Host, Via                   string
	Cluster, Service, Container string
	Hostname                    string
	Forwards                    []Forward
	Settings                    Settings
	Src                         Source
}

// ID は外から参照するときの名前（環境.名前、ルート環境なら名前）。
func (t *Tunnel) ID() string { return qualify(t.Env, t.Name) }

func qualify(env, name string) string {
	if env == "" {
		return name
	}
	return env + "." + name
}

// Env は環境。Name が "" のものはルート環境。
type Env struct {
	Name      string
	Protected bool
	Settings  Settings
	Hosts     map[string]*Host
	Tunnels   map[string]*Tunnel
	Src       Source
}

func (e *Env) sortedTunnels() []*Tunnel {
	ts := make([]*Tunnel, 0, len(e.Tunnels))
	for _, t := range e.Tunnels {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	return ts
}

// Group は groups の1件。
type Group struct {
	Name    string
	Members []string
	Src     Source
}

// Config は正規化した設定全体。
type Config struct {
	Files    []string // 読み込んだファイル（先頭がメイン）
	Defaults Settings
	WSLMode  string
	Envs     map[string]*Env // キー "" はルート環境
	Groups   map[string]*Group

	defaultsSrc Source
	wslSrc      Source
}

func newConfig() *Config {
	return &Config{
		Envs:   map[string]*Env{"": {Hosts: map[string]*Host{}, Tunnels: map[string]*Tunnel{}}},
		Groups: map[string]*Group{},
	}
}

// Tunnels は全環境のトンネルを ID 順に返す。
func (c *Config) Tunnels() []*Tunnel {
	var ts []*Tunnel
	for _, e := range c.Envs {
		ts = append(ts, e.sortedTunnels()...)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID() < ts[j].ID() })
	return ts
}
```

`internal/config/raw.go`:

```go
package config

import (
	"errors"
	"fmt"
)

// SettingsYAML は継承できるキーの YAML 表現。デコード専用で、パッケージ外からは使わない。
type SettingsYAML struct {
	SSH       string  `yaml:"ssh"`
	SSHConfig string  `yaml:"ssh_config"`
	AWS       *rawAWS `yaml:"aws"`
	AWSConfig string  `yaml:"aws_config"`
	Keepalive string  `yaml:"keepalive"`
	Reconnect *bool   `yaml:"reconnect"`
	Profile   string  `yaml:"profile"`
	Region    string  `yaml:"region"`
}

type rawFile struct {
	Include      []string             `yaml:"include"`
	Defaults     *SettingsYAML        `yaml:"defaults"`
	WSL          *rawWSL              `yaml:"wsl"`
	Hosts        map[string]rawHost   `yaml:"hosts"`
	Tunnels      map[string]rawTunnel `yaml:"tunnels"`
	Groups       map[string][]string  `yaml:"groups"`
	Environments map[string]rawEnv    `yaml:"environments"`
}

type rawWSL struct {
	Mode string `yaml:"mode"`
}

type rawHost struct {
	Host string `yaml:"host"`
	User string `yaml:"user"`
	Via  string `yaml:"via"`
}

type rawEnv struct {
	SettingsYAML `yaml:",inline"`
	Protected    bool                 `yaml:"protected"`
	Hosts        map[string]rawHost   `yaml:"hosts"`
	Tunnels      map[string]rawTunnel `yaml:"tunnels"`
}

type rawTunnel struct {
	SettingsYAML `yaml:",inline"`
	Type         string       `yaml:"type"`
	Host         string       `yaml:"host"`
	Via          string       `yaml:"via"`
	Cluster      string       `yaml:"cluster"`
	Service      string       `yaml:"service"`
	Container    string       `yaml:"container"`
	Hostname     string       `yaml:"hostname"`
	Forwards     []rawForward `yaml:"forwards"`
}

// rawAWS は aws: のスカラー（バイナリ）とマッピング（{path, profile, region}）の両方を受ける。
// 形式の誤りはデコードを止めずに Err に残し、ほかの問題と一緒に報告する。
type rawAWS struct {
	Path, Profile, Region string
	Err                   error
}

func (a *rawAWS) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	switch x := v.(type) {
	case string:
		a.Path = x
	case map[string]any:
		for k, val := range x {
			s, ok := val.(string)
			if !ok {
				a.Err = fmt.Errorf("aws.%s は文字列で書きます", k)
				return nil
			}
			switch k {
			case "path":
				a.Path = s
			case "profile":
				a.Profile = s
			case "region":
				a.Region = s
			default:
				a.Err = fmt.Errorf("aws に未知のキー %q があります", k)
				return nil
			}
		}
	default:
		a.Err = errors.New("aws は文字列か {path, profile, region} のマッピングで書きます")
	}
	return nil
}

// rawForward は forwards の短縮記法（文字列）と詳細記法（{local, remote}）の両方を受ける。
type rawForward struct {
	Forward
	Err error
}

func (f *rawForward) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	switch x := v.(type) {
	case string:
		f.Forward, f.Err = ParseForward(x)
	case map[string]any:
		var local, remote string
		for k, val := range x {
			switch k {
			case "local":
				local = fmt.Sprint(val)
			case "remote":
				remote = fmt.Sprint(val)
			default:
				f.Err = fmt.Errorf("forwards に未知のキー %q があります", k)
				return nil
			}
		}
		f.Forward, f.Err = parseDetailedForward(local, remote)
	default:
		f.Err = errors.New("forwards は文字列か {local, remote} で書きます")
	}
	return nil
}
```

`internal/config/problem.go`:

```go
package config

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/parser"
)

// Problem は設定ファイルの問題1件。Line が 0 なら位置が分からない。
type Problem struct {
	File string
	Line int
	Col  int
	Msg  string
	path []any
}

func (p Problem) String() string {
	switch {
	case p.File != "" && p.Line > 0:
		return fmt.Sprintf("%s:%d:%d: %s", p.File, p.Line, p.Col, p.Msg)
	case p.File != "":
		return fmt.Sprintf("%s: %s", p.File, p.Msg)
	}
	return p.Msg
}

// Problems は見つかった問題の一覧。
type Problems []Problem

// Lines は1件1行の表示用の文字列を返す。
func (ps Problems) Lines() []string {
	lines := make([]string, len(ps))
	for i, p := range ps {
		lines[i] = p.String()
	}
	return lines
}

func (ps Problems) Error() string { return strings.Join(ps.Lines(), "\n") }

var posRe = regexp.MustCompile(`(?s)^\[(\d+):(\d+)\]\s*(.*)$`)

// syntaxProblem は goccy/go-yaml のエラーを Problem にする。
func syntaxProblem(file string, err error) Problem {
	msg := strings.TrimSpace(yaml.FormatError(err, false, false))
	if m := posRe.FindStringSubmatch(msg); m != nil {
		line, _ := strconv.Atoi(m[1])
		col, _ := strconv.Atoi(m[2])
		return Problem{File: file, Line: line, Col: col, Msg: firstLine(m[3])}
	}
	return Problem{File: file, Msg: firstLine(msg)}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// locate は YAML 上のパスに対応する行と列を返す。そのパスが無ければ親をたどる。
func locate(data []byte, path []any) (int, int) {
	f, err := parser.ParseBytes(data, 0)
	if err != nil {
		return 0, 0
	}
	for n := len(path); n > 0; n-- {
		b := (&yaml.PathBuilder{}).Root()
		for _, e := range path[:n] {
			switch v := e.(type) {
			case string:
				b = b.Child(v)
			case int:
				b = b.Index(uint(v))
			}
		}
		node, err := b.Build().FilterFile(f)
		if err == nil && node != nil {
			pos := node.GetToken().Position
			return pos.Line, pos.Column
		}
	}
	return 0, 0
}

func sortProblems(ps Problems) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Col < b.Col
	})
}
```

`internal/config/load.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

type loader struct {
	cfg         *Config
	data        map[string][]byte // ファイルごとの内容（位置の特定に使う）
	problems    Problems
	hasDefaults bool
	hasWSL      bool
}

// Load は設定ファイルを読み込み、正規化して返す。問題があれば Problems を返す。
// goos は検証に使う OS 名（runtime.GOOS と同じ値）。
func Load(path, goos string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	l := &loader{cfg: newConfig(), data: map[string][]byte{}}
	l.loadFile(abs, Source{})
	if len(l.problems) > 0 {
		return nil, l.finish()
	}
	return l.cfg, nil
}

func (l *loader) problemf(src Source, format string, args ...any) {
	l.problems = append(l.problems, Problem{File: src.File, path: src.Path, Msg: fmt.Sprintf(format, args...)})
}

func (l *loader) finish() Problems {
	for i := range l.problems {
		p := &l.problems[i]
		if p.Line == 0 && p.path != nil {
			p.Line, p.Col = locate(l.data[p.File], p.path)
		}
	}
	sortProblems(l.problems)
	return l.problems
}

func (l *loader) loadFile(path string, from Source) {
	data, err := os.ReadFile(path)
	if err != nil {
		l.problemf(from, "設定ファイルを読めません: %v", err)
		return
	}
	l.cfg.Files = append(l.cfg.Files, path)
	l.data[path] = data

	var r rawFile
	if err := yaml.UnmarshalWithOptions(data, &r, yaml.DisallowUnknownField()); err != nil {
		l.problems = append(l.problems, syntaxProblem(path, err))
		return
	}
	l.merge(path, &r)
}

func (l *loader) merge(file string, r *rawFile) {
	if r.Defaults != nil {
		src := Source{File: file, Path: []any{"defaults"}}
		if l.hasDefaults {
			l.problemf(src, "defaults は %s でも定義されています", l.cfg.defaultsSrc.File)
		} else {
			l.hasDefaults = true
			l.cfg.defaultsSrc = src
			l.cfg.Defaults = l.settings(src, *r.Defaults)
			if r.Defaults.Profile != "" || r.Defaults.Region != "" {
				l.problemf(src, "defaults には profile / region を書けません。aws.profile / aws.region を使います")
			}
		}
	}
	if r.WSL != nil {
		src := Source{File: file, Path: []any{"wsl"}}
		if l.hasWSL {
			l.problemf(src, "wsl は %s でも定義されています", l.cfg.wslSrc.File)
		} else {
			l.hasWSL = true
			l.cfg.wslSrc = src
			l.cfg.WSLMode = r.WSL.Mode
		}
	}

	root := l.cfg.Envs[""]
	for name, h := range r.Hosts {
		l.addHost(root, name, h, Source{File: file, Path: []any{"hosts", name}})
	}
	for name, t := range r.Tunnels {
		l.addTunnel(root, name, t, Source{File: file, Path: []any{"tunnels", name}})
	}
	for name, e := range r.Environments {
		src := Source{File: file, Path: []any{"environments", name}}
		if old, ok := l.cfg.Envs[name]; ok {
			l.problemf(src, "環境 %q は %s でも定義されています", name, old.Src.File)
			continue
		}
		env := &Env{
			Name:      name,
			Protected: e.Protected,
			Settings:  l.settings(src, e.SettingsYAML),
			Hosts:     map[string]*Host{},
			Tunnels:   map[string]*Tunnel{},
			Src:       src,
		}
		l.cfg.Envs[name] = env
		for hn, h := range e.Hosts {
			l.addHost(env, hn, h, src.child("hosts", hn))
		}
		for tn, t := range e.Tunnels {
			l.addTunnel(env, tn, t, src.child("tunnels", tn))
		}
	}
	for name, members := range r.Groups {
		src := Source{File: file, Path: []any{"groups", name}}
		if old, ok := l.cfg.Groups[name]; ok {
			l.problemf(src, "グループ %q は %s でも定義されています", name, old.Src.File)
			continue
		}
		l.cfg.Groups[name] = &Group{Name: name, Members: members, Src: src}
	}
}

func (l *loader) settings(src Source, r SettingsYAML) Settings {
	s := Settings{
		SSH:       r.SSH,
		SSHConfig: r.SSHConfig,
		AWSConfig: r.AWSConfig,
		Keepalive: r.Keepalive,
		Reconnect: r.Reconnect,
		Profile:   r.Profile,
		Region:    r.Region,
	}
	if a := r.AWS; a != nil {
		if a.Err != nil {
			l.problemf(src.child("aws"), "%v", a.Err)
		}
		s.AWS = a.Path
		if a.Profile != "" {
			if s.Profile != "" {
				l.problemf(src.child("aws"), "profile と aws.profile は同時に書けません")
			} else {
				s.Profile = a.Profile
			}
		}
		if a.Region != "" {
			if s.Region != "" {
				l.problemf(src.child("aws"), "region と aws.region は同時に書けません")
			} else {
				s.Region = a.Region
			}
		}
	}
	return s
}

func (l *loader) addHost(env *Env, name string, r rawHost, src Source) {
	if old, ok := env.Hosts[name]; ok {
		l.problemf(src, "ホスト %q は %s でも定義されています", name, old.Src.File)
		return
	}
	env.Hosts[name] = &Host{Name: name, Env: env.Name, Host: r.Host, User: r.User, Via: r.Via, Src: src}
}

func (l *loader) addTunnel(env *Env, name string, r rawTunnel, src Source) {
	if old, ok := env.Tunnels[name]; ok {
		l.problemf(src, "トンネル %q は %s でも定義されています", name, old.Src.File)
		return
	}
	t := &Tunnel{
		Name: name, Env: env.Name, Type: r.Type,
		Host: r.Host, Via: r.Via,
		Cluster: r.Cluster, Service: r.Service, Container: r.Container,
		Hostname: r.Hostname,
		Settings: l.settings(src, r.SettingsYAML),
		Src:      src,
	}
	if t.Type == "" {
		t.Type = "ssh"
	}
	if len(r.Forwards) == 0 {
		l.problemf(src, "forwards がありません")
	}
	for i, f := range r.Forwards {
		if f.Err != nil {
			l.problemf(src.child("forwards", i), "%v", f.Err)
			continue
		}
		t.Forwards = append(t.Forwards, f.Forward)
	}
	env.Tunnels[name] = t
}
```

- [ ] **Step 5: テストが通ることを確認する**

Run: `go test ./internal/config/ -v`
Expected: PASS

うまくいかないときの確認点（goccy/go-yaml の挙動に依存する箇所）:
- `TestLoadProblems` の「3行目」が合わない: `yaml.FormatError(err, false, false)` の出力を `t.Log` で見て、`posRe` を出力の形式に合わせる
- 詳細記法の map が `map[string]any` 以外（`map[any]any` など）で渡ってくる: `rawForward` / `rawAWS` の型 switch に、その型の case を追加してキーを `fmt.Sprint` で文字列にする
- inline 埋め込みのキーが未知のキー扱いになる: goccy のバージョンを確認し、`SettingsYAML` の埋め込みに `yaml:",inline"` が付いていることを確認する

- [ ] **Step 6: Commit**

```bash
go mod tidy
git add go.mod go.sum internal/config
git commit -m "feat: 設定ファイルを読み込み、正規化する"
```

---

### Task 5: config — include

**Files:**
- Modify: `internal/config/load.go`
- Test: `internal/config/include_test.go`

**Interfaces:**
- Consumes: Task 4 の `loader`
- Produces: `loader` に `stack []string`、`seen map[string]bool` を追加。`include:` を読み込んだファイルからの相対パスで解決する

- [ ] **Step 1: 失敗するテストを書く**

`internal/config/include_test.go`:

```go
package config

import (
	"errors"
	"testing"
)

func loadFiles(t *testing.T, files map[string]string, main string) (*Config, Problems) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, dir, name, content)
	}
	cfg, err := Load(dir+"/"+main, "linux")
	var ps Problems
	if err != nil && !errors.As(err, &ps) {
		t.Fatal(err)
	}
	return cfg, ps
}

func TestInclude(t *testing.T) {
	cfg, ps := loadFiles(t, map[string]string{
		"main.yaml": "include: [dev.yaml]\ngroups:\n  all: [dev]\n",
		"dev.yaml":  "environments:\n  dev:\n    tunnels:\n      db: { host: x, forwards: [\"15432:db:5432\"] }\n",
	}, "main.yaml")
	if ps != nil {
		t.Fatal(ps.Error())
	}
	if cfg.Envs["dev"].Tunnels["db"] == nil || len(cfg.Files) != 2 {
		t.Fatalf("include が読まれていない: %+v", cfg.Files)
	}
}

func TestIncludeDuplicate(t *testing.T) {
	_, ps := loadFiles(t, map[string]string{
		"main.yaml": "include: [a.yaml]\nenvironments:\n  dev: {}\n",
		"a.yaml":    "environments:\n  dev: {}\n",
	}, "main.yaml")
	wantProblem(t, ps, `環境 "dev" は`)
}

func TestIncludeLoop(t *testing.T) {
	_, ps := loadFiles(t, map[string]string{
		"main.yaml": "include: [a.yaml]\n",
		"a.yaml":    "include: [main.yaml]\n",
	}, "main.yaml")
	wantProblem(t, ps, "include がループしています")
}

func TestIncludeMissing(t *testing.T) {
	_, ps := loadFiles(t, map[string]string{"main.yaml": "include: [nope.yaml]\n"}, "main.yaml")
	wantProblem(t, ps, "設定ファイルを読めません")
}
```

- [ ] **Step 2: 失敗を確認する**

Run: `go test ./internal/config/ -run TestInclude -v`
Expected: FAIL（include が読まれず、ループも検出されない）

- [ ] **Step 3: 実装する**

`internal/config/load.go` を変更する。

`loader` に2つのフィールドを足す:

```go
type loader struct {
	cfg         *Config
	data        map[string][]byte // ファイルごとの内容（位置の特定に使う）
	problems    Problems
	hasDefaults bool
	hasWSL      bool
	stack       []string        // 読み込み中のファイル（ループ検出用）
	seen        map[string]bool // 読み込み済みのファイル
}
```

`Load` の中の `l := ...` を次にする:

```go
	l := &loader{cfg: newConfig(), data: map[string][]byte{}, seen: map[string]bool{}}
```

`loadFile` を次に置き換える（`import` に `"slices"` と `"strings"` を足す）:

```go
func (l *loader) loadFile(path string, from Source) {
	if slices.Contains(l.stack, path) {
		l.problemf(from, "include がループしています: %s", strings.Join(append(l.stack, path), " → "))
		return
	}
	if l.seen[path] {
		return
	}
	l.seen[path] = true

	data, err := os.ReadFile(path)
	if err != nil {
		l.problemf(from, "設定ファイルを読めません: %v", err)
		return
	}
	l.cfg.Files = append(l.cfg.Files, path)
	l.data[path] = data

	var r rawFile
	if err := yaml.UnmarshalWithOptions(data, &r, yaml.DisallowUnknownField()); err != nil {
		l.problems = append(l.problems, syntaxProblem(path, err))
		return
	}
	l.merge(path, &r)

	l.stack = append(l.stack, path)
	defer func() { l.stack = l.stack[:len(l.stack)-1] }()
	for i, inc := range r.Include {
		p := inc
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(path), p)
		}
		l.loadFile(filepath.Clean(p), Source{File: path, Path: []any{"include", i}})
	}
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/config/ -v`
Expected: PASS（Task 4 のテストも含めて）

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat: include で設定ファイルを分割できるようにする"
```

---

### Task 6: config — 名前解決と継承

**Files:**
- Create: `internal/config/resolve.go`, `internal/config/effective.go`
- Test: `internal/config/resolve_test.go`

**Interfaces:**
- Consumes: Task 4 のモデル
- Produces:
  - `func (c *Config) LookupHost(env, name string) *Host` — 同じ環境 → ルート環境
  - `type cycleError struct{ path []string }`（`error` を実装）
  - `func (c *Config) hostChain(h *Host) ([]*Host, error)` — `[h, h.via, h.via.via, ...]`
  - `type Route struct { Dest string; Jumps []string }`、`func (c *Config) Route(t *Tunnel) (Route, error)` — Jumps は手元に近い順
  - `func (c *Config) lookupTarget(name string, allowGroup bool) ([]*Tunnel, bool)`
  - `func (c *Config) Select(targets []string) ([]*Tunnel, error)`
  - `type Effective struct { SSH, SSHConfig, AWS, AWSConfig string; Keepalive time.Duration; Reconnect bool; Profile, Region string; Protected bool }`、`func (c *Config) Effective(t *Tunnel) Effective`

- [ ] **Step 1: 失敗するテストを書く**

`internal/config/resolve_test.go`:

```go
package config

import (
	"strings"
	"testing"
	"time"
)

const envYAML = `
defaults:
  keepalive: 20s
hosts:
  shared: { host: shared.example.com, user: ops }
environments:
  dev:
    keepalive: 40s
    hosts:
      bastion: { host: dev-bastion, user: ec2-user }
      mid:     { host: 10.0.0.2, via: bastion }
      ap:      { host: 10.0.1.10, user: admin, via: mid }
    tunnels:
      db:
        via: ap
        forwards: ["15432:dev-db:5432"]
      cache:
        via: shared
        forwards: ["16379:redis:6379"]
  prod:
    protected: true
    tunnels:
      db:
        host: prod-alias
        keepalive: 50s
        forwards: ["25432:prod-db:5432"]
tunnels:
  web:
    host: app1
    forwards: ["8080:localhost:80"]
groups:
  all-db: [dev.db, prod.db]
`

func TestRoute(t *testing.T) {
	cfg := mustLoad(t, envYAML)
	r, err := cfg.Route(cfg.Envs["dev"].Tunnels["db"])
	if err != nil {
		t.Fatal(err)
	}
	if r.Dest != "admin@10.0.1.10" || strings.Join(r.Jumps, ",") != "ec2-user@dev-bastion,10.0.0.2" {
		t.Errorf("dev.db の経路 = %+v", r)
	}
	r, _ = cfg.Route(cfg.Envs["dev"].Tunnels["cache"])
	if r.Dest != "ops@shared.example.com" || len(r.Jumps) != 0 {
		t.Errorf("ルート環境のホストへのフォールバック = %+v", r)
	}
	r, _ = cfg.Route(cfg.Envs["prod"].Tunnels["db"])
	if r.Dest != "prod-alias" {
		t.Errorf("hosts に無い host はそのまま = %+v", r)
	}
}

func TestSelect(t *testing.T) {
	cfg := mustLoad(t, envYAML)
	ids := func(targets ...string) string {
		ts, err := cfg.Select(targets)
		if err != nil {
			return "ERR: " + err.Error()
		}
		var s []string
		for _, t := range ts {
			s = append(s, t.ID())
		}
		return strings.Join(s, ",")
	}
	cases := map[string]string{
		"dev":           "dev.cache,dev.db",
		"dev.db":        "dev.db",
		"all-db":        "dev.db,prod.db",
		"web":           "web",
		"dev prod":      "dev.cache,dev.db,prod.db",
		"dev.db all-db": "dev.db,prod.db",
	}
	for in, want := range cases {
		if got := ids(strings.Fields(in)...); got != want {
			t.Errorf("Select(%s) = %s, want %s", in, got, want)
		}
	}
	if got := ids("nope"); !strings.Contains(got, `"nope" に一致する環境・グループ・トンネルがありません`) {
		t.Errorf("存在しない対象 = %s", got)
	}
}

func TestEffective(t *testing.T) {
	cfg := mustLoad(t, envYAML)
	if e := cfg.Effective(cfg.Envs[""].Tunnels["web"]); e.Keepalive != 20*time.Second || e.SSH != "auto" || !e.Reconnect {
		t.Errorf("web = %+v", e)
	}
	if e := cfg.Effective(cfg.Envs["dev"].Tunnels["db"]); e.Keepalive != 40*time.Second {
		t.Errorf("環境の値で上書き = %+v", e)
	}
	if e := cfg.Effective(cfg.Envs["prod"].Tunnels["db"]); e.Keepalive != 50*time.Second || !e.Protected {
		t.Errorf("トンネルの値で上書き = %+v", e)
	}
}
```

- [ ] **Step 2: 失敗を確認する**

Run: `go test ./internal/config/ -run 'TestRoute|TestSelect|TestEffective' -v`
Expected: FAIL（`cfg.Route undefined` など）

- [ ] **Step 3: 実装する**

`internal/config/resolve.go`:

```go
package config

import (
	"fmt"
	"slices"
	"strings"
)

// LookupHost は環境内のホストを探し、無ければルート環境を探す。
func (c *Config) LookupHost(env, name string) *Host {
	if e := c.Envs[env]; e != nil {
		if h := e.Hosts[name]; h != nil {
			return h
		}
	}
	if env != "" {
		return c.Envs[""].Hosts[name]
	}
	return nil
}

type cycleError struct{ path []string }

func (e *cycleError) Error() string {
	return "via が循環しています: " + strings.Join(e.path, " → ")
}

// hostChain は h から via をたどったホストの列 [h, h の via, ...] を返す。
func (c *Config) hostChain(h *Host) ([]*Host, error) {
	chain := []*Host{h}
	for cur := h; cur.Via != ""; {
		next := c.LookupHost(cur.Env, cur.Via)
		if next == nil {
			return nil, fmt.Errorf("via のホスト %q が見つかりません", cur.Via)
		}
		if slices.Contains(chain, next) {
			var ids []string
			for _, x := range chain {
				ids = append(ids, x.ID())
			}
			return nil, &cycleError{path: append(ids, next.ID())}
		}
		chain = append(chain, next)
		cur = next
	}
	return chain, nil
}

// Route は SSH の接続経路。Dest は最終接続先、Jumps は -J に渡す踏み台（手元に近い順）。
type Route struct {
	Dest  string
	Jumps []string
}

func sshAddr(h *Host) string {
	if h.User == "" {
		return h.Host
	}
	return h.User + "@" + h.Host
}

// Route はトンネルの接続経路を組み立てる。
func (c *Config) Route(t *Tunnel) (Route, error) {
	name := t.Via
	if name == "" {
		name = t.Host
	}
	h := c.LookupHost(t.Env, name)
	if h == nil {
		if t.Via != "" {
			return Route{}, fmt.Errorf("%s: via のホスト %q が見つかりません", t.ID(), t.Via)
		}
		return Route{Dest: t.Host}, nil
	}
	chain, err := c.hostChain(h)
	if err != nil {
		return Route{}, fmt.Errorf("%s: %w", t.ID(), err)
	}
	r := Route{Dest: sshAddr(chain[0])}
	for i := len(chain) - 1; i >= 1; i-- {
		r.Jumps = append(r.Jumps, sshAddr(chain[i]))
	}
	return r, nil
}

// lookupTarget は 環境.名前 → 環境名 → グループ名（allowGroup のとき）→ ルート環境のトンネル名 の順に照合する。
func (c *Config) lookupTarget(name string, allowGroup bool) ([]*Tunnel, bool) {
	if env, tn, ok := strings.Cut(name, "."); ok {
		if e := c.Envs[env]; e != nil && env != "" {
			if t := e.Tunnels[tn]; t != nil {
				return []*Tunnel{t}, true
			}
		}
		return nil, false
	}
	if e := c.Envs[name]; e != nil && name != "" {
		return e.sortedTunnels(), true
	}
	if allowGroup {
		if g := c.Groups[name]; g != nil {
			var ts []*Tunnel
			for _, m := range g.Members {
				got, ok := c.lookupTarget(m, false)
				if !ok {
					return nil, false
				}
				ts = append(ts, got...)
			}
			return ts, true
		}
	}
	if t := c.Envs[""].Tunnels[name]; t != nil {
		return []*Tunnel{t}, true
	}
	return nil, false
}

// Select はコマンドの引数で指定された対象をトンネルの一覧にする。重複は除き、指定順を保つ。
func (c *Config) Select(targets []string) ([]*Tunnel, error) {
	var out []*Tunnel
	seen := map[*Tunnel]bool{}
	for _, name := range targets {
		ts, ok := c.lookupTarget(name, true)
		if !ok {
			return nil, fmt.Errorf("%q に一致する環境・グループ・トンネルがありません", name)
		}
		for _, t := range ts {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out, nil
}
```

`internal/config/effective.go`:

```go
package config

import "time"

// Effective は defaults → 環境 → トンネルの順に上書きした、トンネルの実際の設定。
type Effective struct {
	SSH, SSHConfig, AWS, AWSConfig string
	Keepalive                      time.Duration
	Reconnect                      bool
	Profile, Region                string
	Protected                      bool
}

// Effective はトンネルの実際の設定を返す。
func (c *Config) Effective(t *Tunnel) Effective {
	e := Effective{
		SSH: "auto", SSHConfig: "auto", AWS: "auto", AWSConfig: "auto",
		Keepalive: 30 * time.Second,
		Reconnect: true,
	}
	e.apply(c.Defaults)
	if env := c.Envs[t.Env]; env != nil {
		e.apply(env.Settings)
		e.Protected = env.Protected
	}
	e.apply(t.Settings)
	return e
}

func (e *Effective) apply(s Settings) {
	if s.SSH != "" {
		e.SSH = s.SSH
	}
	if s.SSHConfig != "" {
		e.SSHConfig = s.SSHConfig
	}
	if s.AWS != "" {
		e.AWS = s.AWS
	}
	if s.AWSConfig != "" {
		e.AWSConfig = s.AWSConfig
	}
	if s.Keepalive != "" {
		if d, err := time.ParseDuration(s.Keepalive); err == nil {
			e.Keepalive = d
		}
	}
	if s.Reconnect != nil {
		e.Reconnect = *s.Reconnect
	}
	if s.Profile != "" {
		e.Profile = s.Profile
	}
	if s.Region != "" {
		e.Region = s.Region
	}
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat: 環境の名前空間と via の経路を解決する"
```

---

### Task 7: config — validate

**Files:**
- Create: `internal/config/validate.go`
- Modify: `internal/config/load.go`（`Load` から `validate` を呼ぶ）
- Test: `internal/config/validate_test.go`

**Interfaces:**
- Consumes: Task 6 の `LookupHost`, `hostChain`, `cycleError`, `lookupTarget`、Task 4 の `Tunnels()`
- Produces: `func validate(c *Config, goos string) []Problem`。`Load` はマージで問題が無ければ続けて validate する

- [ ] **Step 1: 失敗するテストを書く**

`internal/config/validate_test.go`:

```go
package config

import "testing"

func TestValidate(t *testing.T) {
	cases := []struct {
		name, yaml, goos, want string
	}{
		{"via の循環", `
hosts:
  a: { host: a.example.com, via: b }
  b: { host: b.example.com, via: a }
`, "linux", "via が循環しています: a → b → a"},
		{"存在しない via", `
tunnels:
  t1: { via: nope, forwards: ["15432:db:5432"] }
`, "linux", `via のホスト "nope" が見つかりません`},
		{"ポートの重複", `
tunnels:
  t1: { host: x, forwards: ["15432:db:5432"] }
environments:
  dev:
    tunnels:
      t2: { host: y, forwards: ["15432:db:5432"] }
`, "linux", "ローカルポート 15432 は"},
		{"環境名とトンネル名の衝突", `
tunnels:
  dev: { host: x, forwards: ["15432:db:5432"] }
environments:
  dev: {}
`, "linux", `トンネル "dev" は環境名と衝突しています`},
		{"グループ名とトンネル名の衝突", `
tunnels:
  web: { host: x, forwards: ["15432:db:5432"] }
groups:
  web: [web]
`, "linux", `グループ "web" はトンネル名と衝突しています`},
		{"host と via の両方", `
hosts:
  b: { host: b }
tunnels:
  t1: { host: x, via: b, forwards: ["15432:db:5432"] }
`, "linux", "host か via のどちらか一方を書きます"},
		{"host のないホスト", `
hosts:
  b: { user: u }
`, "linux", "host がありません"},
		{"ecs の必須項目", `
tunnels:
  t1: { type: ecs, cluster: c, service: s, forwards: ["15432:db:5432"] }
`, "linux", "container がありません"},
		{"ecs に host", `
tunnels:
  t1: { type: ecs, host: x, cluster: c, service: s, container: a, forwards: ["15432:db:5432"] }
`, "linux", "type: ecs では host / via を書けません"},
		{"ssh に profile", `
tunnels:
  t1: { host: x, profile: p, forwards: ["15432:db:5432"] }
`, "linux", "profile は type: ecs でだけ使えます"},
		{"ecs に keepalive", `
tunnels:
  t1: { type: ecs, cluster: c, service: s, container: a, keepalive: 10s, forwards: ["15432:db:5432"] }
`, "linux", "keepalive は type: ssh でだけ使えます"},
		{"不正な type", `
tunnels:
  t1: { type: k8s, host: x, forwards: ["15432:db:5432"] }
`, "linux", `type は ssh か ecs です: "k8s"`},
		{"不正な keepalive", `
defaults:
  keepalive: 500ms
`, "linux", "keepalive は 30s のような 1 秒以上の時間で書きます"},
		{"Windows ネイティブの ssh_config: wsl", `
defaults:
  ssh_config: wsl
`, "windows", "Windows ネイティブでは ssh_config: wsl は使えません"},
		{"存在しないグループのメンバー", `
groups:
  g: [x]
`, "linux", `グループのメンバー "x" が見つかりません`},
		{"不正な wsl.mode", `
wsl:
  mode: bridge
`, "linux", "wsl.mode は auto / mirrored / relay / native のいずれかです"},
		{"名前にドット", `
environments:
  a.b: {}
`, "linux", "名前に . は使えません"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ps := loadString(t, c.yaml, c.goos)
			wantProblem(t, ps, c.want)
		})
	}
}

func TestValidateOK(t *testing.T) {
	mustLoad(t, envYAML)
	mustLoad(t, rootYAML)
}
```

- [ ] **Step 2: 失敗を確認する**

Run: `go test ./internal/config/ -run TestValidate -v`
Expected: FAIL（検証が無いので問題が出ない）

- [ ] **Step 3: 実装する**

`internal/config/validate.go`:

```go
package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type validator struct {
	c        *Config
	goos     string
	problems []Problem
}

type kv struct{ k, v string }

func validate(c *Config, goos string) []Problem {
	v := &validator{c: c, goos: goos}
	v.names()
	v.settings()
	v.hosts()
	v.tunnels()
	v.groups()
	v.ports()
	return v.problems
}

func (v *validator) add(src Source, format string, args ...any) {
	v.problems = append(v.problems, Problem{File: src.File, path: src.Path, Msg: fmt.Sprintf(format, args...)})
}

func (v *validator) names() {
	root := v.c.Envs[""]
	for name, e := range v.c.Envs {
		if name == "" {
			continue
		}
		if strings.Contains(name, ".") {
			v.add(e.Src, "名前に . は使えません: %q", name)
		}
		if t := root.Tunnels[name]; t != nil {
			v.add(t.Src, "トンネル %q は環境名と衝突しています", name)
		}
		if g := v.c.Groups[name]; g != nil {
			v.add(g.Src, "グループ %q は環境名と衝突しています", name)
		}
	}
	for _, t := range v.c.Tunnels() {
		if strings.Contains(t.Name, ".") {
			v.add(t.Src, "名前に . は使えません: %q", t.Name)
		}
	}
	for name, g := range v.c.Groups {
		if strings.Contains(name, ".") {
			v.add(g.Src, "名前に . は使えません: %q", name)
		}
		if root.Tunnels[name] != nil {
			v.add(g.Src, "グループ %q はトンネル名と衝突しています", name)
		}
	}
}

func (v *validator) settings() {
	if m := v.c.WSLMode; m != "" && !slices.Contains([]string{"auto", "mirrored", "relay", "native"}, m) {
		v.add(v.c.wslSrc.child("mode"), "wsl.mode は auto / mirrored / relay / native のいずれかです: %q", m)
	}
	v.checkSettings(v.c.defaultsSrc, v.c.Defaults)
	for _, e := range v.c.Envs {
		if e.Name != "" {
			v.checkSettings(e.Src, e.Settings)
		}
	}
	for _, t := range v.c.Tunnels() {
		v.checkSettings(t.Src, t.Settings)
	}
}

func (v *validator) checkSettings(src Source, s Settings) {
	if s.Keepalive != "" {
		if d, err := time.ParseDuration(s.Keepalive); err != nil || d < time.Second {
			v.add(src.child("keepalive"), "keepalive は 30s のような 1 秒以上の時間で書きます: %q", s.Keepalive)
		}
	}
	if v.goos == "windows" {
		if s.SSHConfig == "wsl" {
			v.add(src.child("ssh_config"), "Windows ネイティブでは ssh_config: wsl は使えません。パスで指定します")
		}
		if s.AWSConfig == "wsl" {
			v.add(src.child("aws_config"), "Windows ネイティブでは aws_config: wsl は使えません。パスで指定します")
		}
	}
}

func (v *validator) hosts() {
	for _, e := range v.c.Envs {
		for _, h := range e.Hosts {
			if h.Host == "" {
				v.add(h.Src, "host がありません")
			}
			if h.Via == "" {
				continue
			}
			if v.c.LookupHost(h.Env, h.Via) == nil {
				v.add(h.Src.child("via"), "via のホスト %q が見つかりません", h.Via)
				continue
			}
			var ce *cycleError
			if _, err := v.c.hostChain(h); errors.As(err, &ce) {
				v.add(h.Src.child("via"), "%v", err)
			}
		}
	}
}

func (v *validator) tunnels() {
	for _, t := range v.c.Tunnels() {
		s := t.Settings
		switch t.Type {
		case "ssh":
			if (t.Host == "") == (t.Via == "") {
				v.add(t.Src, "host か via のどちらか一方を書きます")
			}
			if t.Via != "" && v.c.LookupHost(t.Env, t.Via) == nil {
				v.add(t.Src.child("via"), "via のホスト %q が見つかりません", t.Via)
			}
			for _, x := range []kv{{"cluster", t.Cluster}, {"service", t.Service}, {"container", t.Container},
				{"aws", s.AWS}, {"aws_config", s.AWSConfig}, {"profile", s.Profile}, {"region", s.Region}} {
				if x.v != "" {
					v.add(t.Src.child(x.k), "%s は type: ecs でだけ使えます", x.k)
				}
			}
		case "ecs":
			if t.Host != "" || t.Via != "" {
				v.add(t.Src, "type: ecs では host / via を書けません")
			}
			for _, x := range []kv{{"cluster", t.Cluster}, {"service", t.Service}, {"container", t.Container}} {
				if x.v == "" {
					v.add(t.Src, "%s がありません", x.k)
				}
			}
			for _, x := range []kv{{"ssh", s.SSH}, {"ssh_config", s.SSHConfig}, {"keepalive", s.Keepalive}} {
				if x.v != "" {
					v.add(t.Src.child(x.k), "%s は type: ssh でだけ使えます", x.k)
				}
			}
		default:
			v.add(t.Src.child("type"), "type は ssh か ecs です: %q", t.Type)
		}
	}
}

func (v *validator) groups() {
	for _, g := range v.c.Groups {
		for i, m := range g.Members {
			if _, ok := v.c.lookupTarget(m, false); !ok {
				v.add(g.Src.child(i), "グループのメンバー %q が見つかりません（グループの入れ子はできません）", m)
			}
		}
	}
}

func (v *validator) ports() {
	owner := map[int]string{}
	for _, t := range v.c.Tunnels() {
		for _, f := range t.Forwards {
			if prev, ok := owner[f.LocalPort]; ok {
				v.add(t.Src.child("forwards"), "ローカルポート %d は %s と重複しています", f.LocalPort, prev)
				continue
			}
			owner[f.LocalPort] = t.ID()
		}
	}
}
```

`internal/config/load.go` の `Load` で、`l.loadFile(abs, Source{})` の直後に次を足す:

```go
	if len(l.problems) == 0 {
		l.problems = append(l.problems, validate(l.cfg, goos)...)
	}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/config/ -v`
Expected: PASS（全テスト）

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat: 設定の参照・循環・衝突・ポート重複を検証する"
```

---

### Task 8: config — 設定ファイルの探索

**Files:**
- Create: `internal/config/search.go`
- Test: `internal/config/search_test.go`

**Interfaces:**
- Consumes: Task 2 の `platform.Host`（`Stat`, `Getenv`, `HomeDir`, `IsWSL`, `WindowsHome`）
- Produces: `func Find(ctx context.Context, explicit string, h *platform.Host) (string, error)`

- [ ] **Step 1: 失敗するテストを書く**

`internal/config/search_test.go`:

```go
package config

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukv/hashigo/internal/platform"
)

type mapRunner map[string]string

func (m mapRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	if out, ok := m[key]; ok {
		return []byte(out), nil
	}
	return nil, fmt.Errorf("想定外の呼び出し: %s", key)
}

func searchHost(env map[string]string, exists ...string) *platform.Host {
	set := map[string]bool{}
	for _, p := range exists {
		set[p] = true
	}
	return &platform.Host{
		GOOS:     "linux",
		Getenv:   func(k string) string { return env[k] },
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		Stat: func(p string) (fs.FileInfo, error) {
			if set[p] {
				return nil, nil
			}
			return nil, fs.ErrNotExist
		},
		HomeDir: func() (string, error) { return "/home/u", nil },
		Run: mapRunner{
			"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
			"wslpath -u C:\\Users\\taro":     "/mnt/c/Users/taro\n",
		},
	}
}

func TestFind(t *testing.T) {
	ctx := context.Background()
	home := filepath.Join("/home/u", ".config", "hashigo", "config.yaml")
	xdg := filepath.Join("/xdg", "hashigo", "config.yaml")
	win := "/mnt/c/Users/taro/.config/hashigo/config.yaml"
	wsl := map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}

	cases := []struct {
		name     string
		explicit string
		env      map[string]string
		exists   []string
		want     string
	}{
		{"--config", "/x.yaml", nil, []string{"/x.yaml", "hashigo.yaml"}, "/x.yaml"},
		{"カレント", "", nil, []string{"hashigo.yaml", home}, "hashigo.yaml"},
		{"ホーム", "", nil, []string{home}, home},
		{"XDG", "", map[string]string{"XDG_CONFIG_HOME": "/xdg"}, []string{xdg, home}, xdg},
		{"WSL の Windows ホーム", "", wsl, []string{win}, win},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Find(ctx, c.explicit, searchHost(c.env, c.exists...))
			if err != nil || got != c.want {
				t.Fatalf("Find = %q, %v; want %q", got, err, c.want)
			}
		})
	}

	if _, err := Find(ctx, "/nope.yaml", searchHost(nil)); err == nil {
		t.Error("--config のファイルが無ければエラー")
	}
	_, err := Find(ctx, "", searchHost(wsl))
	if err == nil || !strings.Contains(err.Error(), win) {
		t.Errorf("見つからないときは探した場所を出す: %v", err)
	}
}
```

- [ ] **Step 2: 失敗を確認する**

Run: `go test ./internal/config/ -run TestFind -v`
Expected: FAIL（`undefined: Find`）

- [ ] **Step 3: 実装する**

`internal/config/search.go`:

```go
package config

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/kukv/hashigo/internal/platform"
)

// Find は設定ファイルを探す。順番は --config → カレントの hashigo.yaml →
// $XDG_CONFIG_HOME/hashigo/config.yaml（無ければ ~/.config/hashigo/config.yaml）→
// WSL なら Windows ホームの .config/hashigo/config.yaml。
func Find(ctx context.Context, explicit string, h *platform.Host) (string, error) {
	if explicit != "" {
		if _, err := h.Stat(explicit); err != nil {
			return "", fmt.Errorf("設定ファイルがありません: %s", explicit)
		}
		return explicit, nil
	}
	cands := []string{"hashigo.yaml"}
	if x := h.Getenv("XDG_CONFIG_HOME"); x != "" && h.GOOS != "windows" {
		cands = append(cands, filepath.Join(x, "hashigo", "config.yaml"))
	} else if home, err := h.HomeDir(); err == nil {
		cands = append(cands, filepath.Join(home, ".config", "hashigo", "config.yaml"))
	}
	if h.IsWSL() {
		if wh, err := h.WindowsHome(ctx); err == nil {
			cands = append(cands, path.Join(wh, ".config", "hashigo", "config.yaml"))
		}
	}
	for _, c := range cands {
		if _, err := h.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("設定ファイルが見つかりません（探した場所: %s）", strings.Join(cands, ", "))
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat: 設定ファイルを探索する"
```

---

### Task 9: driver — SSH ドライバ

**Files:**
- Create: `internal/driver/driver.go`, `internal/driver/ssh/ssh.go`
- Test: `internal/driver/ssh/ssh_test.go`

**Interfaces:**
- Consumes: Task 6 の `config.Route`、Task 3 の `config.Forward`
- Produces:
  - `driver.Listen{Addr string; Port int}`、`driver.ProcessSpec{Tunnel, Path string; Args, Env []string; Ports []Listen}`
  - `driver.Driver interface { Preview() []ProcessSpec; Prepare(ctx context.Context) ([]ProcessSpec, error) }`
  - `ssh.Options{Tunnel, Bin string; Route config.Route; Forwards []config.Forward; Keepalive time.Duration}`、`ssh.New(o Options) *ssh.Driver`

- [ ] **Step 1: 失敗するテストを書く**

`internal/driver/ssh/ssh_test.go`:

```go
package ssh

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
)

func TestArgs(t *testing.T) {
	d := New(Options{
		Tunnel: "dev.db",
		Bin:    "/usr/bin/ssh",
		Route:  config.Route{Dest: "admin@10.0.1.10", Jumps: []string{"ec2-user@bastion", "10.0.0.2"}},
		Forwards: []config.Forward{
			{Bind: "127.0.0.1", LocalPort: 15432, RemoteHost: "db.internal", RemotePort: 5432},
			{Bind: "::1", LocalPort: 16379, RemoteHost: "fd00::1", RemotePort: 6379},
		},
		Keepalive: 30 * time.Second,
	})
	want := "-N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o BatchMode=yes " +
		"-J ec2-user@bastion,10.0.0.2 -L 127.0.0.1:15432:db.internal:5432 -L [::1]:16379:[fd00::1]:6379 admin@10.0.1.10"
	specs := d.Preview()
	if len(specs) != 1 {
		t.Fatalf("spec は1つ: %d", len(specs))
	}
	if got := strings.Join(specs[0].Args, " "); got != want {
		t.Errorf("args =\n%s\nwant\n%s", got, want)
	}
	if specs[0].Path != "/usr/bin/ssh" || specs[0].Tunnel != "dev.db" {
		t.Errorf("spec = %+v", specs[0])
	}
	wantPorts := []driver.Listen{{Addr: "127.0.0.1", Port: 15432}, {Addr: "::1", Port: 16379}}
	if !reflect.DeepEqual(specs[0].Ports, wantPorts) {
		t.Errorf("ports = %+v", specs[0].Ports)
	}
	prepared, err := d.Prepare(context.Background())
	if err != nil || !reflect.DeepEqual(prepared, specs) {
		t.Errorf("Prepare は Preview と同じ: %+v, %v", prepared, err)
	}
}

func TestArgsNoJumpAndKeepalive(t *testing.T) {
	d := New(Options{
		Tunnel: "web", Bin: "ssh",
		Route:     config.Route{Dest: "app1"},
		Forwards:  []config.Forward{{Bind: "127.0.0.1", LocalPort: 8080, RemoteHost: "localhost", RemotePort: 80}},
		Keepalive: 1500 * time.Millisecond,
	})
	got := strings.Join(d.Preview()[0].Args, " ")
	if strings.Contains(got, "-J") || !strings.Contains(got, "ServerAliveInterval=2") || !strings.HasSuffix(got, " app1") {
		t.Errorf("args = %s", got)
	}
}
```

- [ ] **Step 2: 失敗を確認する**

Run: `go test ./internal/driver/... -v`
Expected: FAIL（パッケージが無い）

- [ ] **Step 3: 実装する**

`internal/driver/driver.go`:

```go
// Package driver は、トンネル1本を張るために起動するプロセスの組み立て方を定義する。
package driver

import "context"

// Listen はプロセスが開くローカルの待ち受け。
type Listen struct {
	Addr string
	Port int
}

// ProcessSpec は起動するプロセス1つ分。
type ProcessSpec struct {
	Tunnel string   // "dev.db" など。表示と集計の単位
	Path   string   // 実行するバイナリ
	Args   []string
	Env    []string // 追加する環境変数（KEY=VALUE）
	Ports  []Listen // このプロセスが開くローカルポート
}

// Driver はトンネルの種類ごとのコマンドの組み立て方。
type Driver interface {
	// Preview は外部呼び出しをせずに、起動するプロセスを返す（cmd 用）。
	Preview() []ProcessSpec
	// Prepare は接続のたびに呼ばれ、起動するプロセスを返す。並び順は毎回同じにする。
	Prepare(ctx context.Context) ([]ProcessSpec, error)
}
```

`internal/driver/ssh/ssh.go`:

```go
// Package ssh は ssh / ssh.exe でポートフォワードを張るドライバ。
package ssh

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
)

// Options は SSH ドライバの入力。
type Options struct {
	Tunnel    string
	Bin       string
	Route     config.Route
	Forwards  []config.Forward
	Keepalive time.Duration
}

// Driver は1トンネルを1つの ssh プロセスで張る。
type Driver struct{ o Options }

// New は SSH ドライバを作る。
func New(o Options) *Driver { return &Driver{o: o} }

func bracket(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

func (d *Driver) args() []string {
	ka := int(math.Ceil(d.o.Keepalive.Seconds()))
	if ka < 1 {
		ka = 1
	}
	args := []string{
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=" + strconv.Itoa(ka),
		"-o", "ServerAliveCountMax=3",
		"-o", "BatchMode=yes",
	}
	if len(d.o.Route.Jumps) > 0 {
		args = append(args, "-J", strings.Join(d.o.Route.Jumps, ","))
	}
	for _, f := range d.o.Forwards {
		args = append(args, "-L", fmt.Sprintf("%s:%d:%s:%d", bracket(f.Bind), f.LocalPort, bracket(f.RemoteHost), f.RemotePort))
	}
	return append(args, d.o.Route.Dest)
}

// Preview は起動する ssh プロセスを返す。
func (d *Driver) Preview() []driver.ProcessSpec {
	ports := make([]driver.Listen, len(d.o.Forwards))
	for i, f := range d.o.Forwards {
		ports[i] = driver.Listen{Addr: f.Bind, Port: f.LocalPort}
	}
	return []driver.ProcessSpec{{Tunnel: d.o.Tunnel, Path: d.o.Bin, Args: d.args(), Ports: ports}}
}

// Prepare は Preview と同じものを返す。
func (d *Driver) Prepare(context.Context) ([]driver.ProcessSpec, error) {
	return d.Preview(), nil
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/driver/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/driver
git commit -m "feat: ssh コマンドを組み立てるドライバを追加"
```

---

### Task 10: cli — `validate` と `cmd`

**Files:**
- Create: `internal/cli/validate.go`, `internal/cli/build.go`, `internal/cli/cmd.go`
- Modify: `internal/cli/root.go`（`load` を足し、コマンドを登録する）
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: `config.Find`, `config.Load`, `config.Select`, `config.Effective`, `config.Route`, `platform.Host`（`ResolveWSLMode`, `SSHBinary`, `SSHConfigPath`, `IsWSL`）, `ssh.New`
- Produces:
  - `func (a *app) load(ctx context.Context) (*config.Config, string, error)`
  - `type builtUnit struct { ID, Bin, SSHConfig string; Driver driver.Driver }`
  - `func (a *app) buildUnits(ctx context.Context, cfg *config.Config, tunnels []*config.Tunnel) ([]builtUnit, error)`
  - `func shellJoin(args []string) string`

- [ ] **Step 1: 失敗するテストを書く**

`internal/cli/cli_test.go`:

```go
package cli

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kukv/hashigo/internal/platform"
)

// testHost は WSL ではない Linux として振る舞う Host。ファイルの存在確認だけは本物を使う。
func testHost() *platform.Host {
	return &platform.Host{
		GOOS:     "linux",
		Getenv:   func(string) string { return "" },
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		LookPath: func(name string) (string, error) {
			if name == "ssh" {
				return "/usr/bin/ssh", nil
			}
			return "", exec.ErrNotFound
		},
		Stat:    os.Stat,
		HomeDir: func() (string, error) { return "/home/u", nil },
		Run:     platform.ExecRunner{},
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hashigo.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	d := Deps{Host: testHost(), Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut, IsTTY: func() bool { return false }}
	code := Run(context.Background(), d, args)
	return code, out.String(), errOut.String()
}

const cmdYAML = `
hosts:
  bastion: { host: bastion.example.com, user: ec2-user }
  inner: { host: 10.0.1.10, user: admin, via: bastion }
tunnels:
  prod-db:
    via: inner
    forwards:
      - 15432:db.internal:5432
      - 16379:redis.internal:6379
  web:
    host: app1
    keepalive: 45s
    forwards:
      - { local: "127.0.0.1:8080", remote: "localhost:80" }
  task:
    type: ecs
    cluster: c
    service: s
    container: app
    forwards: ["35432:db:5432"]
`

func TestValidateCmd(t *testing.T) {
	p := writeConfig(t, cmdYAML)
	code, out, _ := run(t, "validate", "--config", p)
	if code != 0 || !strings.Contains(out, "OK: "+p) {
		t.Fatalf("code=%d out=%q", code, out)
	}

	bad := writeConfig(t, "tunnels:\n  t1:\n    hots: x\n")
	code, _, errOut := run(t, "validate", "--config", bad)
	if code != 1 || !strings.Contains(errOut, bad+":3:") {
		t.Fatalf("code=%d stderr=%q", code, errOut)
	}
}

func TestCmdCmd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("パス区切りが異なるため Unix 系でのみ確認する")
	}
	p := writeConfig(t, cmdYAML)
	code, out, errOut := run(t, "cmd", "--config", p, "prod-db", "web")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	want := `# 実行環境: linux
# prod-db  ssh=/usr/bin/ssh  ssh_config=/home/u/.ssh/config
/usr/bin/ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o BatchMode=yes -J ec2-user@bastion.example.com -L 127.0.0.1:15432:db.internal:5432 -L 127.0.0.1:16379:redis.internal:6379 admin@10.0.1.10
# web  ssh=/usr/bin/ssh  ssh_config=/home/u/.ssh/config
/usr/bin/ssh -N -o ExitOnForwardFailure=yes -o ServerAliveInterval=45 -o ServerAliveCountMax=3 -o BatchMode=yes -L 127.0.0.1:8080:localhost:80 app1
`
	if out != want {
		t.Errorf("cmd の出力 =\n%s\nwant\n%s", out, want)
	}
}

func TestCmdNotYet(t *testing.T) {
	p := writeConfig(t, cmdYAML)
	code, _, errOut := run(t, "cmd", "--config", p, "task")
	if code != 1 || !strings.Contains(errOut, "安定化フェーズで対応します") {
		t.Fatalf("ecs は未対応: code=%d stderr=%q", code, errOut)
	}
	p = writeConfig(t, "defaults:\n  ssh_config: wsl\ntunnels:\n  t: { host: x, forwards: [\"1:a:1\"] }\n")
	code, _, errOut = run(t, "cmd", "--config", p, "t")
	if code != 1 || !strings.Contains(errOut, "ssh_config の切り替えは安定化フェーズで対応します") {
		t.Fatalf("ssh_config は auto のみ: code=%d stderr=%q", code, errOut)
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"/usr/bin/ssh", "-o", "Foo=bar baz", "it's"})
	if got != `/usr/bin/ssh -o 'Foo=bar baz' 'it'\''s'` {
		t.Errorf("shellJoin = %s", got)
	}
}
```

- [ ] **Step 2: 失敗を確認する**

Run: `go test ./internal/cli/ -v`
Expected: FAIL（`unknown command "validate"`、`undefined: shellJoin`）

- [ ] **Step 3: 実装する**

`internal/cli/root.go` に `load` を足し（`import` に `"github.com/kukv/hashigo/internal/config"` を追加）、`root.AddCommand(newVersionCmd())` を次に変える:

```go
	root.AddCommand(newVersionCmd(), a.validateCmd(), a.cmdCmd())
```

```go
// load は設定ファイルを探して読み込む。
func (a *app) load(ctx context.Context) (*config.Config, string, error) {
	p, err := config.Find(ctx, a.configPath, a.d.Host)
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(p, a.d.Host.GOOS)
	return cfg, p, err
}
```

`internal/cli/validate.go`:

```go
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "設定ファイルの構文と参照を検証する",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			_, p, err := a.load(c.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), "OK:", p)
			return nil
		},
	}
}
```

`internal/cli/build.go`:

```go
package cli

import (
	"context"
	"fmt"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/driver/ssh"
)

// builtUnit は1トンネル分のドライバと、表示用の情報。
type builtUnit struct {
	ID        string
	Bin       string
	SSHConfig string
	Driver    driver.Driver
}

// buildUnits はトンネルごとにドライバを組み立てる。MVP で未対応のものはここでエラーにする。
func (a *app) buildUnits(ctx context.Context, cfg *config.Config, tunnels []*config.Tunnel) ([]builtUnit, error) {
	h := a.d.Host
	mode, err := h.ResolveWSLMode(ctx, cfg.WSLMode)
	if err != nil {
		return nil, err
	}
	if mode == "relay" {
		return nil, fmt.Errorf("relay モードは安定化フェーズで対応します。.wslconfig で networkingMode=mirrored を有効にするか、wsl.mode: native を指定してください")
	}
	var units []builtUnit
	for _, t := range tunnels {
		if t.Type == "ecs" {
			return nil, fmt.Errorf("%s: type: ecs は安定化フェーズで対応します", t.ID())
		}
		eff := cfg.Effective(t)
		if eff.SSHConfig != "auto" {
			return nil, fmt.Errorf("%s: ssh_config の切り替えは安定化フェーズで対応します（現在は auto のみ）", t.ID())
		}
		bin, err := h.SSHBinary(eff.SSH, mode)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", t.ID(), err)
		}
		route, err := cfg.Route(t)
		if err != nil {
			return nil, err
		}
		sshConfig, err := h.SSHConfigPath(ctx, bin)
		if err != nil {
			sshConfig = "(不明)"
		}
		units = append(units, builtUnit{
			ID:        t.ID(),
			Bin:       bin,
			SSHConfig: sshConfig,
			Driver: ssh.New(ssh.Options{
				Tunnel: t.ID(), Bin: bin, Route: route, Forwards: t.Forwards, Keepalive: eff.Keepalive,
			}),
		})
	}
	return units, nil
}

func (a *app) describeEnv(ctx context.Context, cfg *config.Config) string {
	h := a.d.Host
	if !h.IsWSL() {
		return h.GOOS
	}
	mode, err := h.ResolveWSLMode(ctx, cfg.WSLMode)
	if err != nil {
		mode = "不明"
	}
	return fmt.Sprintf("%s (WSL, %s)", h.GOOS, mode)
}
```

`internal/cli/cmd.go`:

```go
package cli

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

func (a *app) cmdCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cmd <対象...>",
		Short: "生成される ssh コマンドを表示する（実行はしない）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			cfg, _, err := a.load(ctx)
			if err != nil {
				return err
			}
			tunnels, err := cfg.Select(args)
			if err != nil {
				return err
			}
			units, err := a.buildUnits(ctx, cfg, tunnels)
			if err != nil {
				return err
			}
			w := c.OutOrStdout()
			fmt.Fprintf(w, "# 実行環境: %s\n", a.describeEnv(ctx, cfg))
			for _, u := range units {
				fmt.Fprintf(w, "# %s  ssh=%s  ssh_config=%s\n", u.ID, u.Bin, u.SSHConfig)
				for _, sp := range u.Driver.Preview() {
					fmt.Fprintln(w, shellJoin(append([]string{sp.Path}, sp.Args...)))
				}
			}
			return nil
		},
	}
}

var safeArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./\[\]-]+$`)

// shellJoin は引数をシェルに貼り付けられる形でつなぐ。
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, s := range args {
		if safeArg.MatchString(s) {
			quoted[i] = s
		} else {
			quoted[i] = "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/cli/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat: validate と cmd コマンドを追加"
```

---

### Task 11: supervisor — 子プロセスの起動・監視・停止

**Files:**
- Create: `internal/platform/proc_unix.go`, `internal/platform/proc_windows.go`, `internal/supervisor/supervisor.go`, `internal/testutil/net.go`, `internal/testutil/fakebin/fakebin.go`
- Test: `internal/supervisor/supervisor_test.go`

**Interfaces:**
- Consumes: Task 9 の `driver.Driver`, `driver.ProcessSpec`
- Produces:
  - `platform.PrepareCmd(cmd *exec.Cmd)`、`platform.Terminate(p *os.Process) error`、`platform.Kill(p *os.Process) error`
  - `supervisor.Supervisor{Out io.Writer; StopTimeout time.Duration}`、`supervisor.Unit{ID string; Driver driver.Driver}`
  - `func (s *Supervisor) Run(ctx context.Context, units []Unit) error` — ctx のキャンセルで全停止して nil、全スロットが自然に終了したら `ErrAllExited`
  - `supervisor.ErrAllExited`
  - `testutil.FreePort(t testing.TB) int`、`testutil.PortOpen(port int) bool`、`testutil.WaitPort(t testing.TB, port int, timeout time.Duration)`、`testutil.SyncBuffer`（`Write`, `String`）、`testutil.Eventually(t testing.TB, timeout time.Duration, cond func() bool, msg string)`
  - `fakebin.Enabled() bool`、`fakebin.Main()` — 環境変数 `HASHIGO_FAKE=1` のときに偽 ssh として振る舞う。`-L [bind:]port:host:port` で待ち受けてエコーする。`FAKE_EXIT_AFTER`（例 `200ms`）後に `FAKE_EXIT_CODE`（既定 1）で終了する。`FAKE_STDERR` を stderr に出す。`FAKE_IGNORE_TERM=1` なら SIGTERM を無視する

- [ ] **Step 1: テスト用のヘルパーと偽 ssh を書く**

`internal/testutil/net.go`:

```go
// Package testutil はテスト用のヘルパー。
package testutil

import (
	"bytes"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

// FreePort は空いているローカルポートを返す。
func FreePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// PortOpen は 127.0.0.1:port に接続できるかを返す。
func PortOpen(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Eventually は cond が true になるまで待つ。timeout を過ぎたら失敗にする。
func Eventually(t testing.TB, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s（%s 待っても満たされない）", msg, timeout)
}

// WaitPort はポートに接続できるようになるまで待つ。
func WaitPort(t testing.TB, port int, timeout time.Duration) {
	t.Helper()
	Eventually(t, timeout, func() bool { return PortOpen(port) }, "ポート "+strconv.Itoa(port)+" が開かない")
}

// SyncBuffer は複数の goroutine から書き込める bytes.Buffer。
type SyncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *SyncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *SyncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
```

`internal/testutil/fakebin/fakebin.go`:

```go
// Package fakebin はテストバイナリ自身を偽の ssh として振る舞わせる。
// テストの TestMain で `if fakebin.Enabled() { fakebin.Main() }` と呼ぶ。
package fakebin

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Enabled は偽バイナリとして起動されたかを返す。
func Enabled() bool { return os.Getenv("HASHIGO_FAKE") == "1" }

// Main は偽 ssh として動き、戻らない。
func Main() {
	if os.Getenv("FAKE_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	if s := os.Getenv("FAKE_STDERR"); s != "" {
		fmt.Fprintln(os.Stderr, s)
	}
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] != "-L" || i+1 >= len(args) {
			continue
		}
		i++
		ln, err := net.Listen("tcp", listenAddr(args[i]))
		if err != nil {
			fmt.Fprintln(os.Stderr, "bind に失敗しました:", err)
			os.Exit(255)
		}
		go serve(ln)
	}
	if d, err := time.ParseDuration(os.Getenv("FAKE_EXIT_AFTER")); err == nil {
		time.Sleep(d)
		code := 1
		if c, err := strconv.Atoi(os.Getenv("FAKE_EXIT_CODE")); err == nil {
			code = c
		}
		os.Exit(code)
	}
	for {
		time.Sleep(time.Hour)
	}
}

// listenAddr は -L の値から待ち受けアドレスを取り出す。
func listenAddr(spec string) string {
	parts := strings.Split(spec, ":")
	if len(parts) == 4 {
		return net.JoinHostPort(parts[0], parts[1])
	}
	return net.JoinHostPort("127.0.0.1", parts[0])
}

func serve(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			io.Copy(c, c)
		}()
	}
}
```

- [ ] **Step 2: 失敗するテストを書く**

`internal/supervisor/supervisor_test.go`:

```go
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/testutil"
	"github.com/kukv/hashigo/internal/testutil/fakebin"
)

func TestMain(m *testing.M) {
	if fakebin.Enabled() {
		fakebin.Main()
	}
	os.Exit(m.Run())
}

type staticDriver struct{ specs []driver.ProcessSpec }

func (d staticDriver) Preview() []driver.ProcessSpec { return d.specs }
func (d staticDriver) Prepare(context.Context) ([]driver.ProcessSpec, error) {
	return d.specs, nil
}

func fakeUnit(id string, port int, env ...string) Unit {
	sp := driver.ProcessSpec{
		Tunnel: id,
		Path:   os.Args[0],
		Args:   []string{"-L", fmt.Sprintf("127.0.0.1:%d:db:5432", port)},
		Env:    append([]string{"HASHIGO_FAKE=1"}, env...),
		Ports:  []driver.Listen{{Addr: "127.0.0.1", Port: port}},
	}
	return Unit{ID: id, Driver: staticDriver{specs: []driver.ProcessSpec{sp}}}
}

func start(t *testing.T, s *Supervisor, units ...Unit) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, units) }()
	t.Cleanup(cancel)
	return cancel, done
}

func wait(t *testing.T, done <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatalf("Run が %s 以内に戻らない", timeout)
		return nil
	}
}

func TestStopOnCancel(t *testing.T) {
	var out testutil.SyncBuffer
	port := testutil.FreePort(t)
	cancel, done := start(t, &Supervisor{Out: &out}, fakeUnit("a", port))
	testutil.WaitPort(t, port, 5*time.Second)

	cancel()
	if err := wait(t, done, 5*time.Second); err != nil {
		t.Fatalf("キャンセル時は nil: %v", err)
	}
	if testutil.PortOpen(port) {
		t.Error("停止後もポートが開いている")
	}
	if s := out.String(); !strings.Contains(s, "[a] 起動しました") || !strings.Contains(s, "[a] 停止しました") {
		t.Errorf("出力 = %s", s)
	}
}

func TestEscalateToKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows では Terminate が即 Kill になる")
	}
	var out testutil.SyncBuffer
	port := testutil.FreePort(t)
	cancel, done := start(t, &Supervisor{Out: &out, StopTimeout: 300 * time.Millisecond},
		fakeUnit("a", port, "FAKE_IGNORE_TERM=1"))
	testutil.WaitPort(t, port, 5*time.Second)

	cancel()
	if err := wait(t, done, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if testutil.PortOpen(port) {
		t.Error("SIGTERM を無視するプロセスが SIGKILL で止まっていない")
	}
}

func TestOtherSlotsKeepRunning(t *testing.T) {
	var out testutil.SyncBuffer
	pa, pb := testutil.FreePort(t), testutil.FreePort(t)
	cancel, done := start(t, &Supervisor{Out: &out},
		fakeUnit("a", pa, "FAKE_EXIT_AFTER=300ms"), fakeUnit("b", pb))
	testutil.WaitPort(t, pb, 5*time.Second)
	testutil.Eventually(t, 5*time.Second, func() bool { return strings.Contains(out.String(), "[a] 終了しました") }, "a の終了が出力されない")

	if !testutil.PortOpen(pb) {
		t.Error("a が終了しても b は動き続ける")
	}
	cancel()
	if err := wait(t, done, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestAllExited(t *testing.T) {
	var out testutil.SyncBuffer
	_, done := start(t, &Supervisor{Out: &out},
		fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=100ms"),
		fakeUnit("b", testutil.FreePort(t), "FAKE_EXIT_AFTER=100ms"))
	if err := wait(t, done, 5*time.Second); !errors.Is(err, ErrAllExited) {
		t.Fatalf("全スロットが終了したら ErrAllExited: %v", err)
	}
}

func TestStderrPrefixed(t *testing.T) {
	var out testutil.SyncBuffer
	cancel, done := start(t, &Supervisor{Out: &out}, fakeUnit("dev.db", testutil.FreePort(t), "FAKE_STDERR=hello"))
	testutil.Eventually(t, 5*time.Second, func() bool { return strings.Contains(out.String(), "[dev.db] hello") }, "stderr に接頭辞が付かない")
	cancel()
	wait(t, done, 5*time.Second)
}

func TestStartFailure(t *testing.T) {
	var out testutil.SyncBuffer
	u := Unit{ID: "x", Driver: staticDriver{specs: []driver.ProcessSpec{{Tunnel: "x", Path: "/nonexistent/ssh"}}}}
	_, done := start(t, &Supervisor{Out: &out}, u)
	if err := wait(t, done, 5*time.Second); !errors.Is(err, ErrAllExited) {
		t.Fatalf("起動できなければ終了扱い: %v", err)
	}
	if !strings.Contains(out.String(), "[x] 起動できませんでした") {
		t.Errorf("出力 = %s", out.String())
	}
}
```

- [ ] **Step 3: 失敗を確認する**

Run: `go test ./internal/supervisor/ -v`
Expected: FAIL（`undefined: Supervisor` など）

- [ ] **Step 4: 実装する**

`internal/platform/proc_unix.go`:

```go
//go:build !windows

package platform

import (
	"os"
	"os/exec"
	"syscall"
)

// PrepareCmd は子プロセスを別のプロセスグループで起動するよう設定する。
// 端末の Ctrl-C が子に直接届かないようにし、停止はスーパーバイザが行う。
func PrepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Terminate はプロセスグループに SIGTERM を送る。
func Terminate(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGTERM) }

// Kill はプロセスグループに SIGKILL を送る。
func Kill(p *os.Process) error { return syscall.Kill(-p.Pid, syscall.SIGKILL) }
```

`internal/platform/proc_windows.go`:

```go
//go:build windows

package platform

import (
	"os"
	"os/exec"
	"syscall"
)

// PrepareCmd は子プロセスを別のプロセスグループで起動するよう設定する。
// Windows ネイティブの本対応（Job Object）は安定化フェーズで行う。
func PrepareCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// Terminate は Windows ではプロセスを終了させる。
func Terminate(p *os.Process) error { return p.Kill() }

// Kill はプロセスを終了させる。
func Kill(p *os.Process) error { return p.Kill() }
```

`internal/supervisor/supervisor.go`:

```go
// Package supervisor は子プロセスを起動・監視・停止する。
package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/platform"
)

// ErrAllExited は、停止の指示が無いまま全スロットが終了したことを表す。
var ErrAllExited = errors.New("すべてのトンネルが終了しました")

// Unit は監視する1トンネル。
type Unit struct {
	ID     string
	Driver driver.Driver
}

// Supervisor はトンネルのプロセスを起動し、停止の指示まで監視する。
// MVP では再接続しない。終了したスロットはそのまま終了扱いにする。
type Supervisor struct {
	Out         io.Writer     // 状態の変化と子プロセスの出力を書く先
	StopTimeout time.Duration // SIGTERM から SIGKILL までの猶予。0 なら 5 秒
}

// Run は全ユニットを起動する。ctx がキャンセルされたら全停止して nil を返す。
// 停止の指示が無いまま全スロットが終了したら ErrAllExited を返す。
func (s *Supervisor) Run(ctx context.Context, units []Unit) error {
	out := &syncWriter{w: s.Out}
	var wg sync.WaitGroup
	for _, u := range units {
		specs, err := u.Driver.Prepare(ctx)
		if err != nil {
			out.printf("[%s] 準備に失敗しました: %v\n", u.ID, err)
			continue
		}
		for _, sp := range specs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.runSlot(ctx, out, sp)
			}()
		}
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return ErrAllExited
}

func (s *Supervisor) runSlot(ctx context.Context, out *syncWriter, sp driver.ProcessSpec) {
	label := "[" + sp.Tunnel + "] "
	lw := &lineWriter{out: out, label: label}
	cmd := exec.Command(sp.Path, sp.Args...)
	cmd.Env = append(os.Environ(), sp.Env...)
	cmd.Stdout, cmd.Stderr = lw, lw
	cmd.WaitDelay = 2 * time.Second
	platform.PrepareCmd(cmd)

	if err := cmd.Start(); err != nil {
		out.printf("%s起動できませんでした: %v\n", label, err)
		return
	}
	out.printf("%s起動しました (pid %d)\n", label, cmd.Process.Pid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		lw.flush()
		out.printf("%s終了しました: %s\n", label, exitText(err))
	case <-ctx.Done():
		s.stop(cmd, done)
		lw.flush()
		out.printf("%s停止しました\n", label)
	}
}

func (s *Supervisor) stop(cmd *exec.Cmd, done <-chan error) {
	_ = platform.Terminate(cmd.Process)
	timeout := s.StopTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-done:
		return
	case <-time.After(timeout):
	}
	_ = platform.Kill(cmd.Process)
	<-done
}

func exitText(err error) string {
	if err == nil {
		return "終了コード 0"
	}
	return err.Error()
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) printf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.w, format, args...)
}

// lineWriter は子プロセスの出力を行ごとに区切り、接頭辞を付けて書く。
type lineWriter struct {
	out   *syncWriter
	label string
	mu    sync.Mutex
	buf   []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.out.printf("%s%s\n", l.label, strings.TrimRight(string(l.buf[:i]), "\r"))
		l.buf = l.buf[i+1:]
	}
	return len(p), nil
}

func (l *lineWriter) flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.buf) > 0 {
		l.out.printf("%s%s\n", l.label, strings.TrimRight(string(l.buf), "\r"))
		l.buf = nil
	}
}
```

- [ ] **Step 5: テストが通ることを確認する**

Run: `go test ./internal/supervisor/ -v -count=1`
Expected: PASS（6 テスト）

- [ ] **Step 6: Commit**

```bash
git add internal/platform internal/supervisor internal/testutil
git commit -m "feat: 子プロセスを起動・監視・停止するスーパーバイザを追加"
```

---

### Task 12: cli — `up`

**Files:**
- Create: `internal/cli/up.go`
- Modify: `internal/cli/root.go`（`a.upCmd()` を登録する）
- Test: `internal/cli/up_test.go`, `internal/cli/main_test.go`

**Interfaces:**
- Consumes: `buildUnits`、`supervisor.Supervisor`、`config.Tunnel`、`Deps.IsTTY` / `Deps.Stdin`
- Produces: `func (a *app) confirmProtected(cfg *config.Config, tunnels []*config.Tunnel, yes bool) error`、`func checkPorts(tunnels []*config.Tunnel) error`

- [ ] **Step 1: 失敗するテストを書く**

`internal/cli/main_test.go`:

```go
package cli

import (
	"os"
	"testing"

	"github.com/kukv/hashigo/internal/testutil/fakebin"
)

func TestMain(m *testing.M) {
	if fakebin.Enabled() {
		fakebin.Main()
	}
	os.Exit(m.Run())
}
```

`internal/cli/up_test.go`:

```go
package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/testutil"
)

// upYAML は偽 ssh（テストバイナリ自身）を ssh として使う設定。
func upYAML(port int, protected bool) string {
	return fmt.Sprintf(`
defaults:
  ssh: %q
environments:
  prod:
    protected: %t
    tunnels:
      db:
        host: example
        forwards: ["%d:db:5432"]
`, os.Args[0], protected, port)
}

func startUp(t *testing.T, stdin string, tty bool, args ...string) (context.CancelFunc, <-chan int, *testutil.SyncBuffer) {
	t.Helper()
	t.Setenv("HASHIGO_FAKE", "1")
	var stderr testutil.SyncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := Deps{Host: testHost(), Stdin: strings.NewReader(stdin), Stdout: io.Discard, Stderr: &stderr, IsTTY: func() bool { return tty }}
	done := make(chan int, 1)
	go func() { done <- Run(ctx, d, args) }()
	return cancel, done, &stderr
}

func waitCode(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case c := <-done:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("up が終わらない")
		return -1
	}
}

func TestUpRunsAndStops(t *testing.T) {
	port := testutil.FreePort(t)
	p := writeConfig(t, upYAML(port, false))
	cancel, done, stderr := startUp(t, "", false, "up", "--config", p, "prod")
	testutil.WaitPort(t, port, 5*time.Second)
	cancel()
	if code := waitCode(t, done); code != 0 {
		t.Fatalf("Ctrl-C で止めたら終了コード 0: %d\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "[prod.db] 停止しました") {
		t.Errorf("stderr = %s", stderr.String())
	}
}

func TestUpProtected(t *testing.T) {
	port := testutil.FreePort(t)
	p := writeConfig(t, upYAML(port, true))

	_, done, stderr := startUp(t, "", false, "up", "--config", p, "prod")
	if code := waitCode(t, done); code != 1 || !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("TTY でなければ --yes が必要: code=%d %s", code, stderr.String())
	}

	_, done, stderr = startUp(t, "n\n", true, "up", "--config", p, "prod")
	if code := waitCode(t, done); code != 1 || !strings.Contains(stderr.String(), "起動を取りやめました") {
		t.Fatalf("n で取りやめ: code=%d %s", code, stderr.String())
	}

	cancel, done, stderr := startUp(t, "y\n", true, "up", "--config", p, "prod")
	testutil.WaitPort(t, port, 5*time.Second)
	cancel()
	if code := waitCode(t, done); code != 0 {
		t.Fatalf("y で起動: code=%d %s", code, stderr.String())
	}

	cancel, done, _ = startUp(t, "", false, "up", "--config", p, "--yes", "prod")
	testutil.WaitPort(t, port, 5*time.Second)
	cancel()
	if code := waitCode(t, done); code != 0 {
		t.Fatalf("--yes で確認を省略: code=%d", code)
	}
}

func TestUpPortBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	p := writeConfig(t, upYAML(port, false))

	_, done, stderr := startUp(t, "", false, "up", "--config", p, "prod")
	if code := waitCode(t, done); code != 1 || !strings.Contains(stderr.String(), "使用中") {
		t.Fatalf("使用中のポートがあれば起動しない: code=%d %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "起動しました") {
		t.Error("何も起動してはいけない")
	}
}
```

- [ ] **Step 2: 失敗を確認する**

Run: `go test ./internal/cli/ -run TestUp -v -count=1`
Expected: FAIL（`unknown command "up"`）

- [ ] **Step 3: 実装する**

`internal/cli/up.go`:

```go
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/supervisor"
)

func (a *app) upCmd() *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "up <対象...>",
		Short: "トンネルを起動する（Ctrl-C で全停止）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return a.up(ctx, c, args, yes)
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "protected 環境の確認を省略する")
	return c
}

func (a *app) up(ctx context.Context, c *cobra.Command, args []string, yes bool) error {
	cfg, _, err := a.load(ctx)
	if err != nil {
		return err
	}
	tunnels, err := cfg.Select(args)
	if err != nil {
		return err
	}
	if len(tunnels) == 0 {
		return errors.New("起動するトンネルがありません")
	}
	built, err := a.buildUnits(ctx, cfg, tunnels)
	if err != nil {
		return err
	}
	if err := a.confirmProtected(cfg, tunnels, yes); err != nil {
		return err
	}
	if err := checkPorts(tunnels); err != nil {
		return err
	}
	units := make([]supervisor.Unit, len(built))
	for i, b := range built {
		units[i] = supervisor.Unit{ID: b.ID, Driver: b.Driver}
	}
	sup := &supervisor.Supervisor{Out: c.ErrOrStderr()}
	return sup.Run(ctx, units)
}

// confirmProtected は protected の環境を含むとき、起動してよいかを確認する。
func (a *app) confirmProtected(cfg *config.Config, tunnels []*config.Tunnel, yes bool) error {
	set := map[string]bool{}
	for _, t := range tunnels {
		if e := cfg.Envs[t.Env]; e != nil && e.Protected {
			set[t.Env] = true
		}
	}
	if len(set) == 0 || yes {
		return nil
	}
	var envs []string
	for e := range set {
		envs = append(envs, e)
	}
	sort.Strings(envs)
	names := strings.Join(envs, ", ")
	if !a.d.IsTTY() {
		return fmt.Errorf("%s は protected です。端末から実行して確認に答えるか、--yes を付けてください", names)
	}
	fmt.Fprintf(a.d.Stderr, "%s は protected です。起動しますか? [y/N] ", names)
	line, _ := bufio.NewReader(a.d.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errors.New("起動を取りやめました")
}

// checkPorts はローカルポートに接続を試し、使用中のものがあればエラーにする。
func checkPorts(tunnels []*config.Tunnel) error {
	var busy []string
	for _, t := range tunnels {
		for _, f := range t.Forwards {
			addr := net.JoinHostPort(dialHost(f.Bind), strconv.Itoa(f.LocalPort))
			conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
			if err != nil {
				continue
			}
			conn.Close()
			busy = append(busy, fmt.Sprintf("%s の %s", t.ID(), addr))
		}
	}
	if len(busy) == 0 {
		return nil
	}
	return fmt.Errorf("ローカルポートが使用中のため起動しません（別の hashigo up か、hashigo 外のプロセスが使っています）:\n  %s",
		strings.Join(busy, "\n  "))
}

func dialHost(bind string) string {
	switch bind {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	}
	return bind
}
```

`internal/cli/root.go` の `root.AddCommand(...)` を次に変える:

```go
	root.AddCommand(newVersionCmd(), a.validateCmd(), a.cmdCmd(), a.upCmd())
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/cli/ -v -count=1 && go test ./... -count=1`
Expected: PASS（全パッケージ）

- [ ] **Step 5: Commit**

```bash
git add internal/cli
git commit -m "feat: up コマンドを追加（protected の確認、ポートチェック、Ctrl-C で全停止）"
```

---

### Task 13: Docker E2E（多段フォワードと停止時の後始末）

**Files:**
- Create: `test/e2e/compose.yaml`, `test/e2e/sshd/Dockerfile`, `test/e2e/sshd/entrypoint.sh`, `test/e2e/db/Dockerfile`, `test/e2e/client/Dockerfile`, `test/e2e/hashigo.yaml`, `test/e2e/scenario.sh`, `test/e2e/e2e_test.go`

**Interfaces:**
- Consumes: `cmd/hashigo` のバイナリ（`validate`, `cmd`, `up`）

ネットワーク構成（client は bastion にしか届かない。多段を経由しないと db に届かないことを確かめる）:

```
client ─front─ bastion ─n1─ mid ─n2─ ap ─back─ db
                                   └──n2────── db
```

- 2段: `two-hop`（`-J bastion`、接続先 `mid`、`mid` から `db:5432`）→ ローカル 15432
- 3段: `three-hop`（`-J bastion,mid`、接続先 `ap`、`ap` から `db:5432`）→ ローカル 25432

- [ ] **Step 1: コンテナの定義を書く**

`test/e2e/compose.yaml`:

```yaml
name: hashigo-e2e

services:
  keys:
    image: alpine:3.20
    command: >-
      sh -c "apk add --no-cache openssh-keygen >/dev/null &&
             rm -f /keys/id_ed25519 /keys/id_ed25519.pub &&
             ssh-keygen -q -t ed25519 -N '' -f /keys/id_ed25519 &&
             chmod 644 /keys/id_ed25519"
    volumes: [keys:/keys]

  bastion:
    build: ./sshd
    depends_on: { keys: { condition: service_completed_successfully } }
    volumes: [keys:/keys:ro]
    networks: [front, n1]

  mid:
    build: ./sshd
    depends_on: { keys: { condition: service_completed_successfully } }
    volumes: [keys:/keys:ro]
    networks: [n1, n2]

  ap:
    build: ./sshd
    depends_on: { keys: { condition: service_completed_successfully } }
    volumes: [keys:/keys:ro]
    networks: [n2, back]

  db:
    build: ./db
    networks: [n2, back]

  client:
    build:
      context: .
      dockerfile: client/Dockerfile
    depends_on: [bastion, mid, ap, db]
    volumes: [keys:/keys:ro]
    networks: [front]

networks:
  front: {}
  n1: {}
  n2: {}
  back: {}

volumes:
  keys: {}
```

`test/e2e/sshd/Dockerfile`:

```dockerfile
FROM alpine:3.20
RUN apk add --no-cache openssh \
 && ssh-keygen -A \
 && adduser -D -s /bin/sh hashigo \
 && echo "hashigo:$(head -c 16 /dev/urandom | base64)" | chpasswd
COPY entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/bin/sh", "/entrypoint.sh"]
```

`test/e2e/sshd/entrypoint.sh`:

```sh
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
```

`test/e2e/db/Dockerfile`:

```dockerfile
FROM alpine:3.20
RUN apk add --no-cache socat
CMD ["socat", "TCP-LISTEN:5432,fork,reuseaddr", "EXEC:cat"]
```

`test/e2e/client/Dockerfile`:

```dockerfile
FROM alpine:3.20
RUN apk add --no-cache openssh-client socat
COPY bin/hashigo /usr/local/bin/hashigo
COPY hashigo.yaml scenario.sh /e2e/
ENTRYPOINT ["/bin/sh", "/e2e/scenario.sh"]
```

`test/e2e/hashigo.yaml`:

```yaml
hosts:
  bastion: { host: bastion, user: hashigo }
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
```

`test/e2e/scenario.sh`:

```sh
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

# sshd の起動を待つ（3段目まで届けば全段そろっている）
i=0
until ssh -o BatchMode=yes -o ConnectTimeout=2 -J hashigo@bastion,hashigo@mid hashigo@ap true 2>/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 30 ]; then echo "NG: sshd が起動しない"; exit 1; fi
  sleep 1
done

hashigo validate --config "$CFG"
hashigo cmd --config "$CFG" all

hashigo up --config "$CFG" all >/tmp/up.log 2>&1 &
pid=$!

check() {
  port=$1
  n=0
  while [ "$n" -lt 30 ]; do
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

kill -INT "$pid"
if ! wait "$pid"; then
  echo "NG: Ctrl-C 後の up の終了コードが 0 ではない"
  cat /tmp/up.log
  exit 1
fi
sleep 1
if pgrep -x ssh >/dev/null; then
  echo "NG: up の停止後に ssh が残っている"
  ps
  exit 1
fi
cat /tmp/up.log
echo "E2E OK"
```

- [ ] **Step 2: Go から E2E を実行するテストを書く**

`test/e2e/e2e_test.go`:

```go
//go:build e2e

// Package e2e は Docker で多段の sshd を立て、hashigo を本物の ssh で動かす。
package e2e

import (
	"os"
	"os/exec"
	"testing"
)

func run(t *testing.T, env []string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %v: %v", name, args, err)
	}
}

func TestE2E(t *testing.T) {
	run(t, []string{"CGO_ENABLED=0", "GOOS=linux"}, "go", "build", "-o", "bin/hashigo", "../../cmd/hashigo")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "down", "-v", "--remove-orphans")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_ = cmd.Run()
	})
	run(t, nil, "docker", "compose", "build")
	run(t, nil, "docker", "compose", "run", "--rm", "client")
}
```

- [ ] **Step 3: E2E を実行する**

Run: `make e2e`
Expected: `OK: 15432 で db に届いた`、`OK: 25432 で db に届いた`、`E2E OK`、`--- PASS: TestE2E`

うまくいかないときの確認点:
- `sshd が起動しない`: `docker compose logs bastion mid ap` で sshd のエラーを見る（authorized_keys の権限、ユーザーのロックなど）
- `db に届かない`: `/tmp/up.log` の `[two-hop]` の行に出ている ssh のエラーを見る。`hashigo cmd` の出力を client で手で実行して切り分ける

- [ ] **Step 4: Commit**

```bash
git add test/e2e
git commit -m "test: Docker で2段・3段のフォワードと停止時の後始末を確認する E2E を追加"
```

---

### Task 14: CI、README、実機での受け入れ確認

**Files:**
- Create: `.github/workflows/ci.yml`
- Modify: `README.md`

- [ ] **Step 1: CI を書く**

`.github/workflows/ci.yml`:

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

jobs:
  test:
    strategy:
      fail-fast: false
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go vet ./...
      - run: go test -count=1 ./...

  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go test -tags e2e -count=1 -v ./test/e2e/...
```

- [ ] **Step 2: README に使い方を書く**

`README.md` を次の内容にする:

````markdown
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
````

- [ ] **Step 3: 全テストを通す**

Run: `go vet ./... && go test -count=1 ./... && make e2e`
Expected: すべて PASS

- [ ] **Step 4: Commit**

```bash
git add .github README.md
git commit -m "ci: 3 OS のテストと E2E を追加し、README に使い方を書く"
```

- [ ] **Step 5: 実機での受け入れ確認（ユーザーと一緒に行う）**

WSL の実環境で、spec の 12章 #6 と MVP の前提を確認する。設定ファイルの置き場所とポート帯（12章のユーザー側の判断）は、ここでユーザーに決めてもらう。

1. `go build -o bin/hashigo ./cmd/hashigo`
2. ユーザーが実際の踏み台・AP・DB を `hashigo.yaml` に書く（2段と、可能なら3段以上の経路）
3. `bin/hashigo validate` → `OK`
4. `bin/hashigo cmd <対象>` → `ssh=/mnt/c/.../ssh.exe`、`実行環境: linux (WSL, mirrored)`、`ssh_config=/mnt/c/Users/<名前>/.ssh/config` と表示される
5. `bin/hashigo up <対象>` → `[<id>] 起動しました`。PowerShell で `Test-NetConnection 127.0.0.1 -Port <ポート>` が `TcpTestSucceeded : True` になり、DB クライアントでログインできる
6. 3段以上の経路でも 5 と同じ結果になる
7. Ctrl-C → `[<id>] 停止しました`、終了コード 0。PowerShell で `Get-Process ssh -ErrorAction SilentlyContinue` を実行し、ssh.exe が残っているかを記録する（spec 12章 #1 の事前情報になる。残っていても MVP では失敗にしない）
8. 別の端末で同じ対象を `up` → ポート使用中で起動しない
9. protected の環境を `up` → 確認が出る

結果は spec 12章の表に追記して commit する:

```bash
git add docs/superpowers/specs/2026-10-23-hashigo-design.md
git commit -m "docs: MVP の実機確認の結果を設計書に追記"
```
