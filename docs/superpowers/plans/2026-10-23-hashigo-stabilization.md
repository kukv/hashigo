# hashigo 安定化 実装プラン

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** MVP の hashigo を「放置しても使い続けられる状態」にする。再接続・ヘルスチェック・状態ファイル、`status` / `doctor`、relay モードと Windows 側プロセスの後始末、ECS（SSM）ドライバ、`ssh_config` / `aws_config` の切り替え、Windows ネイティブ対応、`docs/acceptance.md` を入れる。

**Architecture:** MVP の4層（config → driver → supervisor → platform、cli は薄く）を保つ。新しく `internal/state`（状態ファイル）、`internal/doctor`（確認項目）、`internal/driver/ecs`（SSM）、`internal/driver/relay`（socat 中継）を足す。外部コマンドは引き続き `platform.Runner` と supervisor の子プロセス起動だけを通す。

**Tech Stack:** Go 1.27、`github.com/spf13/cobra`、`github.com/goccy/go-yaml`、`golang.org/x/term`、`golang.org/x/sys`（直接依存に格上げ）、Docker Compose（E2E）、GitHub Actions。

**Spec:** `docs/superpowers/specs/2026-10-23-hashigo-design.md`

## Global Constraints

- モジュールパス: `github.com/kukv/hashigo`。`go.mod` の go ディレクティブは `go 1.27`
- ユーザー向けのメッセージ、コメントは日本語。識別子は英語
- 子プロセスの ssh には常に `-N -o ExitOnForwardFailure=yes -o ServerAliveInterval=<keepalive秒> -o ServerAliveCountMax=3 -o BatchMode=yes` を付け、宛先の前に `--` を置く
- 再接続のバックオフ: 1s → 2s → 4s → … と倍にし、上限 60s。60 秒以上動いたら 1s に戻す
- ヘルスチェック: 10 秒ごと、各ローカルポートへ TCP 接続、タイムアウト 2 秒。失敗しても kill しない
- 停止: Unix は SIGTERM → 5 秒待つ → SIGKILL。Windows は Job Object で終了
- 状態ファイル: `$XDG_STATE_HOME/hashigo/run/<pid>.json`（既定 `~/.local/state/hashigo/run/`）、Windows は `%LOCALAPPDATA%\hashigo\run\`。一時ファイルに書いて rename。`up` の終了時に削除
- 外部コマンドは `platform.Runner`（`Output(ctx, name, args...)`）を通す。環境変数・端末の接続・stderr の取り込みが要るときは `Host.Runner(platform.RunOptions{Env, Interactive, Combined})` で Runner を得る。`os/exec` を直接使ってよいのは `internal/platform/host.go` の `ExecRunner` と、`internal/platform/proc_*.go` / `internal/supervisor/supervisor.go` の子プロセス起動だけ（`.golangci.yml` の G204 例外の前提）
- WSL や Windows でしか動かない処理は「出力を解釈する純粋関数（全 OS で単体テスト）＋ Runner を呼ぶ薄い関数」に分ける。CI の octocov は `acceptable: 80%` で、ubuntu で測ったカバレッジが下回ると test ジョブが落ちる
- テストは `go test ./...` で ubuntu / macos / windows の全部で通ること。`/proc`、`Setpgid`、SIGTERM の無視などに依存するテストは `runtime.GOOS` で分ける。E2E は `-tags e2e`
- 新しいパッケージを作るタスクでは、同じタスクで `.golangci.yml` の depguard に層のルールを足す。`make lint` と `make tidy-check` が通ること
- コミットメッセージの末尾に `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` を付ける

## プランで決めたこと（仕様の空白と食い違いの裁定）

| # | 決めたこと | 理由 |
|---|---|---|
| R1 | relay の書き換え（仕様 6.4 の `Wrap`）は `internal/driver/relay` に置く。ゲートウェイ IP の取得など OS 依存の部分だけを platform に置く | depguard で platform → driver を禁じているため。仕様 §2 / §6.4 を Task 10 で直す |
| R2 | ECS × relay（未確認事項 #4）は、`up` / `cmd` でエラーにして mirrored を案内し、doctor でも ✗ にする | session-manager-plugin は 127.0.0.1 で待ち受けるため、仕様の保守的な側を選ぶ。`docs/acceptance.md` で実機確認する |
| R3 | ECS トンネルの forwards に 127.0.0.1 以外の待ち受けアドレスを書いたら validate エラー | SSM の `localPortNumber` は待ち受けアドレスを指定できない |
| R4 | スロットは自分の再接続のたびに `Prepare` を呼び、同じ位置の Spec を使う。起動直後もスロットごとに呼ぶ | 仕様 5.1 の「張り直すときは Prepare を呼び直す」を起動時にも適用し、初回だけの特別扱いをなくす。ECS で forward が N 本なら起動時の list-tasks も N 回になる |
| R5 | 子プロセスを起動できない（バイナリが無いなど）ときは、再接続せずに failed にする | 何度試しても直らないため |
| R6 | 状態ファイルの `started_at` には `platform.StartTime(os.Getpid())` を書き、残骸の判定では同じ関数で取った値と 1 秒以内かで比べる | Linux の起動時刻は tick 単位で、`time.Now()` と比べるとずれるため |
| R7 | `cmd` が実行しないのは接続と AWS API の呼び出し。表示用のパス変換（`cmd.exe`、`wslpath`、`wslinfo`）は実行する | MVP の `cmd` も Windows ホームの取得に `cmd.exe` を使っている |
| R8 | `status` の「状態ファイルなし」の行はトンネル単位（ポートをまとめて表示）、「状態ファイルあり」の行はスロット単位 | スロットの分け方は Prepare しないと決まらないため |
| R9 | Windows ネイティブの Job Object はスロットごとに1つ作る | 1スロットだけ止めるときに、ほかのスロットを巻き込まないため |
| R10 | ヘルスチェックは実時間の ticker で回し、`Clock` はバックオフと時刻にだけ使う | 偽の Clock で即時に発火させると、ヘルスチェックが空回りするため |

## ファイル構成

```
cmd/hashigo/main.go                       StdoutTTY、EnableVT を足す
internal/platform/host.go                 RunOptions、Host.Runner、IsWSL の見直し、AWSBinary、StateDir、ErrText
internal/platform/configpath.go           SSHConfigFor、AWSConfigFor、ToWindowsPath、ToWSLPath（新規）
internal/platform/interop.go              DefaultGateway、WindowsListeners、CleanupWindows、HostsFiles（新規）
internal/platform/interop_parse.go        上の出力を解釈する純粋関数（新規）
internal/platform/proc_unix.go            StartChild / Child（Setpgid）
internal/platform/proc_windows.go         StartChild / Child（Job Object）
internal/platform/starttime.go            IsAlive、/proc の解釈（新規）
internal/platform/starttime_linux.go      StartTime（新規）
internal/platform/starttime_darwin.go     StartTime（新規）
internal/platform/starttime_windows.go    StartTime（新規）
internal/platform/starttime_other.go      StartTime（未対応 OS、新規）
internal/platform/console_windows.go      EnableVT（新規）
internal/platform/console_other.go        EnableVT（何もしない、新規）
internal/driver/driver.go                 Exit、Classify、Rebinder
internal/driver/ssh/ssh.go                ConfigFile（-F）、PreviewAt / PrepareAt、Classify
internal/driver/ecs/ecs.go                ECS ドライバ（新規）
internal/driver/relay/relay.go            relay ドライバ（新規）
internal/state/state.go                   状態ファイル（新規）
internal/supervisor/supervisor.go         再接続、Clock、ヘルスチェック、状態ファイル
internal/doctor/doctor.go                 確認項目の枠組みと表示（新規）
internal/doctor/checks.go                 確認項目 1〜11（新規）
internal/config/validate.go               ECS の待ち受けアドレス
internal/cli/build.go                     ECS、config の切り替え、relay
internal/cli/cmd.go                       ECS の表示
internal/cli/up.go                        事前認証、衝突相手、状態ファイル、後始末、LAN 公開の警告
internal/cli/status.go                    status（新規）
internal/cli/doctor.go                    doctor（新規）
internal/cli/root.go                      errQuiet、StdoutTTY、status / doctor の登録
internal/testutil/fakebin/fakebin.go      偽 aws（localPortNumber で待ち受け）
test/e2e/*                                再接続と status のシナリオ
docs/acceptance.md                        手動の受け入れ手順（新規）
README.md, README.ja.md                   新しいコマンドと設定
docs/superpowers/specs/2026-10-23-hashigo-design.md  裁定の反映
.golangci.yml                             新パッケージの depguard、gosec の例外
```

---

### Task 1: platform — Runner の拡張、WSL 判定の見直し、aws の選択、状態ディレクトリ

**Files:**
- Modify: `internal/platform/host.go`
- Test: `internal/platform/host_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `type RunOptions struct { Env []string; Interactive bool; Combined bool }`
  - `type ExecRunner struct { Opts RunOptions }`（ゼロ値は MVP と同じ動き）
  - `Host.RunWith func(RunOptions) Runner`（`Detect` が設定する。nil なら `Host.Run` を使う）
  - `func (h *Host) Runner(o RunOptions) Runner`
  - `func (h *Host) CrossesToWindows(bin string) bool` — WSL 上で `.exe` を実行するか
  - `func (h *Host) AWSBinary(setting, wslMode string) (string, error)`
  - `func (h *Host) StateDir() (string, error)`
  - `func ErrText(err error) string` — `*exec.ExitError` なら stderr の中身、それ以外は `err.Error()`
  - `IsWSL` は「`WSL_DISTRO_NAME` か `WSL_INTEROP` がある」または「`/proc/version` に microsoft があり、かつ `/proc/sys/fs/binfmt_misc/WSLInterop`（または `WSLInterop-late`）がある」

- [ ] **Step 1: 失敗するテストを書く**

`internal/platform/host_test.go` の `TestIsWSL` を次に置き換える。

```go
func TestIsWSL(t *testing.T) {
	if !newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, nil).IsWSL() {
		t.Error("WSL_DISTRO_NAME があれば WSL")
	}
	if !newHost(map[string]string{"WSL_INTEROP": "/run/WSL/1_interop"}, nil, nil, nil).IsWSL() {
		t.Error("WSL_INTEROP があれば WSL")
	}
	wslKernel := "Linux version 6.18 (microsoft-standard-WSL2)"
	if !newHost(nil, map[string]string{"/proc/version": wslKernel, "/proc/sys/fs/binfmt_misc/WSLInterop": ""}, nil, nil).IsWSL() {
		t.Error("/proc/version に microsoft があり WSLInterop があれば WSL")
	}
	if newHost(nil, map[string]string{"/proc/version": wslKernel}, nil, nil).IsWSL() {
		t.Error("WSL 上の Docker コンテナ（/proc/version だけが microsoft）は WSL ではない")
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
```

同じファイルの末尾に追加する（`runtime` を import に足す）。

```go
func TestAWSBinary(t *testing.T) {
	wslEnv := map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}
	paths := map[string]string{"aws": "/usr/bin/aws", "aws.exe": "/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe"}
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
		{name: "非WSLのauto", paths: paths, setting: "auto", want: "/usr/bin/aws"},
		{name: "WSLのauto", env: wslEnv, paths: paths, setting: "auto", mode: "mirrored", want: "/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe"},
		{name: "WSLのnative", env: wslEnv, paths: paths, setting: "", mode: "native", want: "/usr/bin/aws"},
		{name: "PATHに無いaws.exe", env: wslEnv, files: map[string]string{windowsAWS: ""}, setting: "auto", mode: "mirrored", want: windowsAWS},
		{name: "パスの指定", files: map[string]string{"/opt/aws": ""}, setting: "/opt/aws", want: "/opt/aws"},
		{name: "存在しないパス", setting: "/nope/aws", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := newHost(c.env, c.files, c.paths, nil).AWSBinary(c.setting, c.mode)
			if (err != nil) != c.wantErr || got != c.want {
				t.Fatalf("AWSBinary = %q, %v; want %q (err=%v)", got, err, c.want, c.wantErr)
			}
		})
	}
	if _, err := newHost(nil, nil, nil, nil).AWSBinary("/nope/aws", ""); err == nil || !strings.Contains(err.Error(), "aws に指定したファイルがありません") {
		t.Errorf("メッセージ = %v", err)
	}
}

func TestCrossesToWindows(t *testing.T) {
	wsl := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, nil)
	if !wsl.CrossesToWindows("/mnt/c/Windows/System32/OpenSSH/ssh.EXE") {
		t.Error("WSL で .exe なら true（大文字小文字は区別しない）")
	}
	if wsl.CrossesToWindows("/usr/bin/ssh") {
		t.Error("Linux 版なら false")
	}
	win := newHost(nil, nil, nil, nil)
	win.GOOS = "windows"
	if win.CrossesToWindows(`C:\Windows\System32\OpenSSH\ssh.exe`) {
		t.Error("Windows ネイティブでは false")
	}
}

func TestStateDir(t *testing.T) {
	h := newHost(nil, nil, nil, nil)
	if got, _ := h.StateDir(); got != filepath.Join("/home/u", ".local", "state", "hashigo", "run") {
		t.Errorf("既定 → %q", got)
	}
	h = newHost(map[string]string{"XDG_STATE_HOME": "/x/state"}, nil, nil, nil)
	if got, _ := h.StateDir(); got != filepath.Join("/x/state", "hashigo", "run") {
		t.Errorf("XDG_STATE_HOME → %q", got)
	}
	h = newHost(map[string]string{"LOCALAPPDATA": `C:\Users\taro\AppData\Local`}, nil, nil, nil)
	h.GOOS = "windows"
	if got, _ := h.StateDir(); got != filepath.Join(`C:\Users\taro\AppData\Local`, "hashigo", "run") {
		t.Errorf("Windows → %q", got)
	}
	h.Getenv = func(string) string { return "" }
	if _, err := h.StateDir(); err == nil {
		t.Error("Windows で LOCALAPPDATA が無ければエラー")
	}
}

func TestHostRunner(t *testing.T) {
	run := fakeRunner{}
	h := newHost(nil, nil, nil, run)
	if got := h.Runner(RunOptions{Interactive: true}); got == nil {
		t.Fatal("RunWith が無ければ Run を返す")
	}
	var seen RunOptions
	h.RunWith = func(o RunOptions) Runner { seen = o; return run }
	h.Runner(RunOptions{Env: []string{"A=1"}, Combined: true})
	if len(seen.Env) != 1 || !seen.Combined {
		t.Errorf("RunWith に渡した値 = %+v", seen)
	}
}

func TestExecRunner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh を使うため Unix 系でのみ確認する")
	}
	ctx := context.Background()
	out, err := ExecRunner{Opts: RunOptions{Env: []string{"HASHIGO_X=42"}}}.Output(ctx, "sh", "-c", "echo $HASHIGO_X")
	if err != nil || strings.TrimSpace(string(out)) != "42" {
		t.Errorf("Env = %q, %v", out, err)
	}
	out, err = ExecRunner{Opts: RunOptions{Combined: true}}.Output(ctx, "sh", "-c", "echo out; echo err >&2")
	if err != nil || !strings.Contains(string(out), "err") {
		t.Errorf("Combined = %q, %v", out, err)
	}
	_, err = ExecRunner{}.Output(ctx, "sh", "-c", "echo bad >&2; exit 3")
	if got := ErrText(err); got != "bad" {
		t.Errorf("ErrText = %q", got)
	}
	if got := ErrText(errors.New("x")); got != "x" {
		t.Errorf("ErrText(普通のエラー) = %q", got)
	}
}
```

`TestDetect` に1行足す。

```go
	if h.RunWith == nil {
		t.Fatal("Detect は RunWith も埋める")
	}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/platform/`
Expected: FAIL（`windowsAWS`、`AWSBinary`、`CrossesToWindows`、`StateDir`、`RunOptions`、`ErrText` が未定義）

- [ ] **Step 3: 実装する**

`internal/platform/host.go` の `Runner` から `SSHBinary` までを、次のように書き換える（`WindowsHome`、`NetworkMode`、`ResolveWSLMode` は変えない）。import には `errors` と `strings` が既にある。

```go
// Runner は外部コマンドを実行して標準出力を返す。
type Runner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// RunOptions は外部コマンドの実行方法。
type RunOptions struct {
	Env         []string // 追加する環境変数（KEY=VALUE）
	Interactive bool     // 端末の stdin と stderr をつなぐ（MFA の入力など）
	Combined    bool     // stderr も出力に含める（ssh -V など）
}

// ExecRunner は os/exec で実際に実行する Runner。ゼロ値は stdout だけを返す。
type ExecRunner struct{ Opts RunOptions }

func (r ExecRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(r.Opts.Env) > 0 {
		cmd.Env = append(os.Environ(), r.Opts.Env...)
	}
	if r.Opts.Interactive {
		cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	}
	if r.Opts.Combined {
		return cmd.CombinedOutput()
	}
	return cmd.Output()
}

// ErrText は外部コマンドのエラーを表示用の文字列にする。stderr が取れていればその中身を返す。
func ErrText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(bytes.TrimSpace(ee.Stderr)) > 0 {
		return strings.TrimSpace(strings.ReplaceAll(string(ee.Stderr), "\r", ""))
	}
	return err.Error()
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
	RunWith  func(RunOptions) Runner // nil なら Run を使う

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
		RunWith:  func(o RunOptions) Runner { return ExecRunner{Opts: o} },
	}
}

// Runner は o の実行方法で動く Runner を返す。RunWith が無ければ Run を返す（テスト用）。
func (h *Host) Runner(o RunOptions) Runner {
	if h.RunWith != nil {
		return h.RunWith(o)
	}
	return h.Run
}

var wslInterop = []string{"/proc/sys/fs/binfmt_misc/WSLInterop", "/proc/sys/fs/binfmt_misc/WSLInterop-late"}

// IsWSL は WSL 上で動いているかを返す。
// WSL 上の Docker コンテナでも /proc/version に microsoft が入るため、/proc/version だけでは判定しない。
func (h *Host) IsWSL() bool {
	if h.GOOS != "linux" {
		return false
	}
	if h.Getenv("WSL_DISTRO_NAME") != "" || h.Getenv("WSL_INTEROP") != "" {
		return true
	}
	b, err := h.ReadFile("/proc/version")
	if err != nil || !strings.Contains(strings.ToLower(string(b)), "microsoft") {
		return false
	}
	for _, p := range wslInterop {
		if _, err := h.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// CrossesToWindows は、WSL 上で Windows のバイナリ（.exe）を実行するかを返す。
func (h *Host) CrossesToWindows(bin string) bool {
	return h.IsWSL() && strings.HasSuffix(strings.ToLower(bin), ".exe")
}
```

`windowsSSH` の定数から `SSHConfigPath` の手前までを次に置き換える。

```go
const (
	windowsSSH = "/mnt/c/Windows/System32/OpenSSH/ssh.exe"
	windowsAWS = "/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe"
)

// SSHBinary は ssh の設定値と WSL のモードから、実行する ssh のパスを決める。
func (h *Host) SSHBinary(setting, wslMode string) (string, error) {
	return h.binary("ssh", windowsSSH, setting, wslMode)
}

// AWSBinary は aws の設定値と WSL のモードから、実行する aws のパスを決める。
func (h *Host) AWSBinary(setting, wslMode string) (string, error) {
	return h.binary("aws", windowsAWS, setting, wslMode)
}

// binary は auto なら WSL（native 以外）で <kind>.exe、それ以外で <kind> を PATH から探す。
// PATH に無い .exe は、Windows の既定のインストール先 winDefault を使う。
func (h *Host) binary(kind, winDefault, setting, wslMode string) (string, error) {
	name := setting
	switch setting {
	case "", "auto":
		name = kind
		if h.IsWSL() && wslMode != "native" {
			name = kind + ".exe"
		}
	case kind, kind + ".exe":
	default:
		if _, err := h.Stat(setting); err != nil {
			return "", fmt.Errorf("%s に指定したファイルがありません: %s", kind, setting)
		}
		return setting, nil
	}
	if p, err := h.LookPath(name); err == nil {
		return p, nil
	}
	if name == kind+".exe" && h.IsWSL() {
		if _, err := h.Stat(winDefault); err == nil {
			return winDefault, nil
		}
	}
	return "", fmt.Errorf("%s が見つかりません", name)
}
```

`SSHConfigPath` の条件を `CrossesToWindows` に置き換える。

```go
// SSHConfigPath は、そのバイナリが既定で読む ssh config のパスを返す（表示用）。
func (h *Host) SSHConfigPath(ctx context.Context, bin string) (string, error) {
	if h.CrossesToWindows(bin) {
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

// StateDir は状態ファイルを置くディレクトリを返す。
func (h *Host) StateDir() (string, error) {
	if h.GOOS == "windows" {
		d := h.Getenv("LOCALAPPDATA")
		if d == "" {
			return "", errors.New("LOCALAPPDATA が設定されていないため、状態ファイルの場所を決められません")
		}
		return filepath.Join(d, "hashigo", "run"), nil
	}
	if d := h.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "hashigo", "run"), nil
	}
	home, err := h.HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "hashigo", "run"), nil
}
```

import に `bytes` を足す。

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/platform/ && go test ./... && make lint`
Expected: PASS。lint の指摘なし

- [ ] **Step 5: コミットする**

```bash
git add internal/platform/host.go internal/platform/host_test.go
git commit -m "feat(platform): Runner の実行方法、WSL 判定の見直し、aws の選択、状態ディレクトリを追加する

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: platform — 子プロセスの起動・停止と、プロセスの起動時刻

**Files:**
- Modify: `internal/platform/proc_unix.go`, `internal/platform/proc_windows.go`, `internal/supervisor/supervisor.go`, `go.mod`, `go.sum`, `.golangci.yml`
- Create: `internal/platform/starttime.go`, `internal/platform/starttime_linux.go`, `internal/platform/starttime_darwin.go`, `internal/platform/starttime_windows.go`, `internal/platform/starttime_other.go`
- Test: `internal/platform/proc_test.go`（新規）, `internal/platform/starttime_test.go`（新規）

**Interfaces:**
- Consumes: なし
- Produces:
  - `type Child struct { Process *os.Process; ... }`
  - `func StartChild(cmd *exec.Cmd) (*Child, error)` — 別のプロセスグループ（Windows は Job Object）で起動する
  - `func (c *Child) Terminate() error`、`func (c *Child) Kill() error`、`func (c *Child) Close()`
  - `func StartTime(pid int) (time.Time, error)`
  - `func IsAlive(pid int, startedAt time.Time) bool`
  - MVP の `PrepareCmd` / `Terminate` / `Kill` は削除する（呼び出し元は supervisor だけ）

- [ ] **Step 1: 失敗するテストを書く**

`internal/platform/starttime_test.go`:

```go
package platform

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestParseStatStartTicks(t *testing.T) {
	// comm に空白と ) を含む場合も、最後の ) の後ろから数える
	stat := "1234 (my (weird) cmd) S 1 1234 1234 0 -1 4194304 100 0 0 0 1 2 0 0 20 0 1 0 98765 1000 100 18446744073709551615"
	got, err := parseStatStartTicks([]byte(stat))
	if err != nil || got != 98765 {
		t.Fatalf("parseStatStartTicks = %d, %v", got, err)
	}
	if _, err := parseStatStartTicks([]byte("1234 (x) S 1")); err == nil {
		t.Error("項目が足りなければエラー")
	}
	if _, err := parseStatStartTicks([]byte("no paren")); err == nil {
		t.Error(") が無ければエラー")
	}
}

func TestParseBootTime(t *testing.T) {
	got, err := parseBootTime([]byte("cpu  1 2 3\nbtime 1700000000\nprocesses 10\n"))
	if err != nil || got != 1700000000 {
		t.Fatalf("parseBootTime = %d, %v", got, err)
	}
	if _, err := parseBootTime([]byte("cpu 1\n")); err == nil {
		t.Error("btime が無ければエラー")
	}
}

func TestStartTimeSelf(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
	default:
		t.Skip("未対応の OS")
	}
	st, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if st.After(time.Now().Add(time.Second)) || st.Before(time.Now().Add(-24*time.Hour)) {
		t.Errorf("自分の起動時刻が不自然: %s", st)
	}
	again, _ := StartTime(os.Getpid())
	if !again.Equal(st) {
		t.Errorf("同じプロセスの起動時刻は毎回同じ: %s と %s", st, again)
	}
	if !IsAlive(os.Getpid(), st) {
		t.Error("自分は生きている")
	}
	if IsAlive(os.Getpid(), st.Add(-time.Hour)) {
		t.Error("起動時刻が違えば pid の再利用とみなす")
	}
}
```

`internal/platform/proc_test.go`:

```go
package platform

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestMain は、テストバイナリ自身を「眠り続けるだけの子プロセス」として使えるようにする。
func TestMain(m *testing.M) {
	if os.Getenv("HASHIGO_PLATFORM_SLEEP") == "1" {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "HASHIGO_PLATFORM_SLEEP=1")
	return cmd
}

func waitExit(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("子プロセスが終了しない")
	}
}

func TestStartChildTerminate(t *testing.T) {
	cmd := sleeper(t)
	c, err := StartChild(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st, err := StartTime(c.Process.Pid)
	if err != nil {
		t.Fatalf("子の起動時刻: %v", err)
	}
	if err := c.Terminate(); err != nil {
		t.Fatal(err)
	}
	waitExit(t, cmd)
	if IsAlive(c.Process.Pid, st) {
		t.Error("終了したプロセスは生きていない")
	}
}

func TestStartChildKill(t *testing.T) {
	cmd := sleeper(t)
	c, err := StartChild(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Kill(); err != nil {
		t.Fatal(err)
	}
	waitExit(t, cmd)
}

func TestStartChildError(t *testing.T) {
	if _, err := StartChild(exec.Command("/nonexistent/hashigo-test")); err == nil {
		t.Error("存在しないバイナリはエラー")
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/platform/`
Expected: FAIL（`parseStatStartTicks`、`StartTime`、`StartChild` が未定義）

- [ ] **Step 3: 実装する**

`internal/platform/starttime.go`:

```go
package platform

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"time"
)

// IsAlive は pid のプロセスが生きていて、その起動時刻が startedAt と 1 秒以内で一致するかを返す。
// 一致しなければ、pid が別のプロセスに再利用されたとみなす。
// startedAt は同じ StartTime で取った値を渡す。
func IsAlive(pid int, startedAt time.Time) bool {
	st, err := StartTime(pid)
	if err != nil {
		return false
	}
	d := st.Sub(startedAt)
	return d > -time.Second && d < time.Second
}

// parseStatStartTicks は /proc/<pid>/stat から起動時刻（起動からの tick 数、22 番目の項目）を取り出す。
// 2 番目の項目（comm）は空白や ) を含みうるので、最後の ) より後ろを数える。
func parseStatStartTicks(stat []byte) (int64, error) {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("/proc/<pid>/stat の形式が想定と違います")
	}
	// ) の後ろの最初の項目が 3 番目（state）なので、22 番目は添字 19
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 20 {
		return 0, errors.New("/proc/<pid>/stat の項目が足りません")
	}
	return strconv.ParseInt(fields[19], 10, 64)
}

// parseBootTime は /proc/stat の btime（起動した時刻の UNIX 秒）を取り出す。
func parseBootTime(stat []byte) (int64, error) {
	for _, line := range strings.Split(string(stat), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	return 0, errors.New("/proc/stat に btime がありません")
}
```

`internal/platform/starttime_linux.go`:

```go
//go:build linux

package platform

import (
	"os"
	"strconv"
	"time"
)

// clockTicks は /proc の時刻の単位（USER_HZ）。Linux では事実上 100 に固定されている。
const clockTicks = 100

// StartTime は pid のプロセスが起動した時刻を返す。
func StartTime(pid int) (time.Time, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return time.Time{}, err
	}
	ticks, err := parseStatStartTicks(stat)
	if err != nil {
		return time.Time{}, err
	}
	sys, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	btime, err := parseBootTime(sys)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(btime, 0).Add(time.Duration(ticks) * (time.Second / clockTicks)), nil
}
```

`internal/platform/starttime_darwin.go`:

```go
//go:build darwin

package platform

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// StartTime は pid のプロセスが起動した時刻を返す（sysctl kern.proc.pid）。
func StartTime(pid int) (time.Time, error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, err
	}
	if int(kp.Proc.P_pid) != pid {
		return time.Time{}, fmt.Errorf("pid %d のプロセスがありません", pid)
	}
	tv := kp.Proc.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), nil
}
```

`internal/platform/starttime_windows.go`:

```go
//go:build windows

package platform

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows"
)

// stillActive は GetExitCodeProcess が「まだ動いている」ときに返す値（STILL_ACTIVE）。
const stillActive = 259

// StartTime は pid のプロセスが起動した時刻を返す（OpenProcess と GetProcessTimes）。
func StartTime(pid int) (time.Time, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err == nil && code != stillActive {
		return time.Time{}, fmt.Errorf("pid %d は終了しています", pid)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, created.Nanoseconds()), nil
}
```

`internal/platform/starttime_other.go`:

```go
//go:build !linux && !darwin && !windows

package platform

import (
	"errors"
	"time"
)

// StartTime はこの OS では未対応。状態ファイルはすべて残骸とみなされる。
func StartTime(int) (time.Time, error) {
	return time.Time{}, errors.New("この OS ではプロセスの起動時刻を取得できません")
}
```

`internal/platform/proc_unix.go` を置き換える。

```go
//go:build !windows

package platform

import (
	"os"
	"os/exec"
	"syscall"
)

// Child は別のプロセスグループで動かしている子プロセス。
type Child struct {
	Process *os.Process
}

// StartChild は cmd を別のプロセスグループで起動する。
// 端末の Ctrl-C が子に直接届かないようにし、停止はスーパーバイザが行う。
func StartChild(cmd *exec.Cmd) (*Child, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Child{Process: cmd.Process}, nil
}

// Terminate はプロセスグループに SIGTERM を送る。
func (c *Child) Terminate() error { return syscall.Kill(-c.Process.Pid, syscall.SIGTERM) }

// Kill はプロセスグループに SIGKILL を送る。
func (c *Child) Kill() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }

// Close は後片付けをする。Unix では何もしない。
func (c *Child) Close() {}
```

`internal/platform/proc_windows.go` を置き換える。

```go
//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Child は Job Object に入れて動かしている子プロセス。
// Job Object はスロットごとに1つ作るので、1スロットだけを止められる。
type Child struct {
	Process *os.Process
	job     windows.Handle
}

// StartChild は cmd を新しいプロセスグループで起動し、KILL_ON_JOB_CLOSE の Job Object に入れる。
// hashigo が異常終了してハンドルが閉じても、子（ssh.exe が起動する ProxyJump の ssh.exe も含む）は残らない。
func StartChild(cmd *exec.Cmd) (*Child, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	job, err := newKillOnCloseJob()
	if err != nil {
		return nil, fmt.Errorf("Job Object を作れません: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	if err := assignToJob(job, cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("子プロセスを Job Object に入れられません: %w", err)
	}
	return &Child{Process: cmd.Process, job: job}, nil
}

func newKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func assignToJob(job windows.Handle, pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	return windows.AssignProcessToJobObject(job, h)
}

// Terminate は Job Object ごと終了させる。Windows には SIGTERM に当たるものが無い。
func (c *Child) Terminate() error { return windows.TerminateJobObject(c.job, 1) }

// Kill は Job Object ごと終了させる。
func (c *Child) Kill() error { return windows.TerminateJobObject(c.job, 1) }

// Close は Job Object のハンドルを閉じる。プロセスが終わったあとに呼ぶ。
func (c *Child) Close() { _ = windows.CloseHandle(c.job) }
```

`internal/supervisor/supervisor.go` の `runSlot` と `stop` を、新しい API に合わせて書き換える（動きは変えない）。

```go
func (s *Supervisor) runSlot(ctx context.Context, out *syncWriter, sp driver.ProcessSpec) {
	label := "[" + sp.Tunnel + "] "
	lw := &lineWriter{out: out, label: label}
	cmd := exec.Command(sp.Path, sp.Args...)
	cmd.Env = append(os.Environ(), sp.Env...)
	cmd.Stdout, cmd.Stderr = lw, lw
	cmd.WaitDelay = 2 * time.Second

	child, err := platform.StartChild(cmd)
	if err != nil {
		out.printf("%s起動できませんでした: %v\n", label, err)
		return
	}
	defer child.Close()
	out.printf("%s起動しました (pid %d)\n", label, child.Process.Pid)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		lw.flush()
		out.printf("%s終了しました: %s\n", label, exitText(err))
	case <-ctx.Done():
		s.stop(child, done)
		lw.flush()
		out.printf("%s停止しました\n", label)
	}
}

func (s *Supervisor) stop(child *platform.Child, done <-chan error) {
	_ = child.Terminate()
	timeout := s.StopTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-done:
		return
	case <-time.After(timeout):
	}
	_ = child.Kill()
	<-done
}
```

`golang.org/x/sys` を直接依存にする。

Run: `go mod tidy`
Expected: `go.mod` の `golang.org/x/sys` から `// indirect` が外れる

`.golangci.yml` の `exclusions.rules` の G304 の例外の後ろに追加する。

```yaml
      # /proc/<pid>/stat の pid は状態ファイルに記録した数値で、パスの残りは固定である。
      - path: internal/platform/starttime_linux\.go
        linters:
          - gosec
        text: "G304"
      # 子プロセスの起動は supervisor から呼ばれる StartChild に集めている（host.go の例外と同じ理由）。
      - path: internal/platform/proc_(unix|windows)\.go
        linters:
          - gosec
        text: "G204"
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test -race ./... && GOOS=windows go vet ./... && GOOS=darwin go vet ./... && make lint && make tidy-check`
Expected: PASS。Windows と macOS でもコンパイルが通る

- [ ] **Step 5: コミットする**

```bash
git add internal/platform internal/supervisor/supervisor.go go.mod go.sum .golangci.yml
git commit -m "feat(platform): 子プロセスの起動を Child にまとめ、Windows は Job Object で管理する

プロセスの起動時刻（Linux は /proc、macOS は sysctl、Windows は GetProcessTimes）と
IsAlive も追加する。

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: platform — ssh / aws の config の参照先（仕様 6.3）

**Files:**
- Create: `internal/platform/configpath.go`
- Test: `internal/platform/configpath_test.go`

**Interfaces:**
- Consumes: Task 1 の `CrossesToWindows`、既存の `WindowsHome`、`SSHConfigPath`
- Produces:
  - `func (h *Host) ToWindowsPath(ctx context.Context, p string) (string, error)` — `wslpath -w`
  - `func (h *Host) ToWSLPath(ctx context.Context, p string) (string, error)` — `wslpath -u`
  - `func (h *Host) SSHConfigFor(ctx context.Context, bin, setting string) (arg, display string, err error)` — `arg` は `-F` に渡す値（空なら付けない）
  - `func (h *Host) AWSConfigFor(ctx context.Context, bin, setting string) (env []string, display string, err error)` — `env` は aws に渡す環境変数

仕様 6.3 の表との対応:

| bin | setting | SSHConfigFor の arg / display | AWSConfigFor の env / display |
|---|---|---|---|
| どれでも | auto | `""` / bin 側の既定 | なし / bin 側の既定（Linux 側で `AWS_CONFIG_FILE` があればそれ） |
| .exe（WSL） | windows | `""` / `<winhome>/.ssh/config` | なし / `<winhome>/.aws/config` |
| Linux 版（WSL） | windows | `<winhome>/.ssh/config` / 同じ | `AWS_CONFIG_FILE`、`AWS_SHARED_CREDENTIALS_FILE` を `<winhome>/.aws/...` に / `<winhome>/.aws/config` |
| どれでも（Windows ネイティブ） | windows | `""` / `<home>\.ssh\config` | なし / `<home>\.aws\config` |
| .exe（WSL） | wsl | `wslpath -w ~/.ssh/config`（UNC） / 同じ | `AWS_CONFIG_FILE`、`AWS_SHARED_CREDENTIALS_FILE` を Linux のパスに、`WSLENV` に `/p` 付きで追加 / Linux のパス |
| Linux 版（WSL） | wsl | `""` / `~/.ssh/config` | なし / `~/.aws/config` |
| .exe（WSL） | パス | `wslpath -w <パス>` / 同じ | `AWS_CONFIG_FILE=<wslpath -w パス>`、`WSLENV` に `AWS_CONFIG_FILE` を追加 / 変換後 |
| それ以外 | パス | `<パス>` / 同じ | `AWS_CONFIG_FILE=<パス>` / 同じ |
| WSL でも Windows でもない | windows / wsl | エラー | エラー |

- [ ] **Step 1: 失敗するテストを書く**

`internal/platform/configpath_test.go`:

```go
package platform

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

const (
	exeSSH = "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"
	exeAWS = "/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe"
)

func wslConfigHost(extra fakeRunner) *Host {
	run := fakeRunner{
		"cmd.exe /c echo %USERPROFILE%":          "C:\\Users\\taro\r\n",
		"wslpath -u C:\\Users\\taro":             "/mnt/c/Users/taro\n",
		"wslpath -w /home/u/.ssh/config":         "\\\\wsl.localhost\\Ubuntu\\home\\u\\.ssh\\config\n",
		"wslpath -w /home/u/work/ssh_config":     "\\\\wsl.localhost\\Ubuntu\\home\\u\\work\\ssh_config\n",
		"wslpath -w /home/u/work/aws_config":     "\\\\wsl.localhost\\Ubuntu\\home\\u\\work\\aws_config\n",
		"wslpath -u C:\\Users\\taro\\.ssh\\id_a": "/mnt/c/Users/taro/.ssh/id_a\n",
	}
	for k, v := range extra {
		run[k] = v
	}
	return newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu", "WSLENV": "USERPROFILE/p"}, nil, nil, run)
}

func TestSSHConfigFor(t *testing.T) {
	ctx := context.Background()
	wsl := wslConfigHost(nil)
	cases := []struct {
		name, bin, setting, arg, display string
	}{
		{"exeのauto", exeSSH, "auto", "", "/mnt/c/Users/taro/.ssh/config"},
		{"exeのwindows", exeSSH, "windows", "", "/mnt/c/Users/taro/.ssh/config"},
		{"exeのwsl", exeSSH, "wsl", `\\wsl.localhost\Ubuntu\home\u\.ssh\config`, `\\wsl.localhost\Ubuntu\home\u\.ssh\config`},
		{"exeのパス", exeSSH, "/home/u/work/ssh_config", `\\wsl.localhost\Ubuntu\home\u\work\ssh_config`, `\\wsl.localhost\Ubuntu\home\u\work\ssh_config`},
		{"linuxのwindows", "/usr/bin/ssh", "windows", "/mnt/c/Users/taro/.ssh/config", "/mnt/c/Users/taro/.ssh/config"},
		{"linuxのwsl", "/usr/bin/ssh", "wsl", "", "/home/u/.ssh/config"},
		{"linuxのパス", "/usr/bin/ssh", "/home/u/work/ssh_config", "/home/u/work/ssh_config", "/home/u/work/ssh_config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			arg, display, err := wsl.SSHConfigFor(ctx, c.bin, c.setting)
			if err != nil || arg != c.arg || display != c.display {
				t.Fatalf("SSHConfigFor = %q, %q, %v; want %q, %q", arg, display, err, c.arg, c.display)
			}
		})
	}

	plain := newHost(nil, nil, nil, nil)
	for _, s := range []string{"windows", "wsl"} {
		if _, _, err := plain.SSHConfigFor(ctx, "/usr/bin/ssh", s); err == nil {
			t.Errorf("WSL でも Windows でもなければ %s はエラー", s)
		}
	}
	win := newHost(nil, nil, nil, nil)
	win.GOOS = "windows"
	if arg, display, err := win.SSHConfigFor(ctx, `C:\ssh.exe`, "windows"); err != nil || arg != "" || display != filepath.Join("/home/u", ".ssh", "config") {
		t.Errorf("Windows ネイティブの windows = %q, %q, %v", arg, display, err)
	}
}

func TestAWSConfigFor(t *testing.T) {
	ctx := context.Background()
	wsl := wslConfigHost(nil)
	// WSL 側は Linux なので、テストを Windows で動かしてもパスは / 区切り
	linuxCfg := "/home/u/.aws/config"
	linuxCred := "/home/u/.aws/credentials"
	cases := []struct {
		name, bin, setting string
		env                []string
		display            string
	}{
		{"exeのauto", exeAWS, "auto", nil, "/mnt/c/Users/taro/.aws/config"},
		{"exeのwindows", exeAWS, "windows", nil, "/mnt/c/Users/taro/.aws/config"},
		{"exeのwsl", exeAWS, "wsl", []string{
			"AWS_CONFIG_FILE=" + linuxCfg,
			"AWS_SHARED_CREDENTIALS_FILE=" + linuxCred,
			"WSLENV=USERPROFILE/p:AWS_CONFIG_FILE/p:AWS_SHARED_CREDENTIALS_FILE/p",
		}, linuxCfg},
		{"exeのパス", exeAWS, "/home/u/work/aws_config", []string{
			`AWS_CONFIG_FILE=\\wsl.localhost\Ubuntu\home\u\work\aws_config`,
			"WSLENV=USERPROFILE/p:AWS_CONFIG_FILE",
		}, `\\wsl.localhost\Ubuntu\home\u\work\aws_config`},
		{"linuxのwindows", "/usr/bin/aws", "windows", []string{
			"AWS_CONFIG_FILE=/mnt/c/Users/taro/.aws/config",
			"AWS_SHARED_CREDENTIALS_FILE=/mnt/c/Users/taro/.aws/credentials",
		}, "/mnt/c/Users/taro/.aws/config"},
		{"linuxのwsl", "/usr/bin/aws", "wsl", nil, linuxCfg},
		{"linuxのパス", "/usr/bin/aws", "/home/u/work/aws_config", []string{"AWS_CONFIG_FILE=/home/u/work/aws_config"}, "/home/u/work/aws_config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env, display, err := wsl.AWSConfigFor(ctx, c.bin, c.setting)
			if err != nil || !reflect.DeepEqual(env, c.env) || display != c.display {
				t.Fatalf("AWSConfigFor = %q, %q, %v; want %q, %q", env, display, err, c.env, c.display)
			}
		})
	}

	withEnv := newHost(map[string]string{"AWS_CONFIG_FILE": "/etc/aws/config"}, nil, nil, nil)
	if _, display, _ := withEnv.AWSConfigFor(ctx, "/usr/bin/aws", "auto"); display != "/etc/aws/config" {
		t.Errorf("auto で AWS_CONFIG_FILE があればそれを表示する: %q", display)
	}
	if _, _, err := newHost(nil, nil, nil, nil).AWSConfigFor(ctx, "/usr/bin/aws", "wsl"); err == nil {
		t.Error("WSL でなければ wsl はエラー")
	}
}

func TestToWSLPath(t *testing.T) {
	got, err := wslConfigHost(nil).ToWSLPath(context.Background(), `C:\Users\taro\.ssh\id_a`)
	if err != nil || got != "/mnt/c/Users/taro/.ssh/id_a" {
		t.Fatalf("ToWSLPath = %q, %v", got, err)
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/platform/ -run 'ConfigFor|ToWSLPath'`
Expected: FAIL（`SSHConfigFor` などが未定義）

- [ ] **Step 3: 実装する**

`internal/platform/configpath.go`:

```go
package platform

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// ToWindowsPath は WSL のパスを Windows のパス（UNC を含む）に変換する。
func (h *Host) ToWindowsPath(ctx context.Context, p string) (string, error) {
	out, err := h.Run.Output(ctx, "wslpath", "-w", p)
	if err != nil {
		return "", fmt.Errorf("wslpath で %s を変換できません: %w", p, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ToWSLPath は Windows のパスを WSL のパスに変換する。
func (h *Host) ToWSLPath(ctx context.Context, p string) (string, error) {
	out, err := h.Run.Output(ctx, "wslpath", "-u", p)
	if err != nil {
		return "", fmt.Errorf("wslpath で %s を変換できません: %w", p, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// SSHConfigFor は ssh_config の設定値から、-F に渡す値（空なら付けない）と表示用のパスを返す。
// 対応は仕様 6.3 の表のとおり。auto は実行バイナリと同じ側の config を使う。
func (h *Host) SSHConfigFor(ctx context.Context, bin, setting string) (arg, display string, err error) {
	switch setting {
	case "", "auto":
		p, err := h.SSHConfigPath(ctx, bin)
		return "", p, err
	case "windows":
		if h.GOOS == "windows" {
			p, err := h.SSHConfigPath(ctx, bin)
			return "", p, err
		}
		if !h.IsWSL() {
			return "", "", errors.New("ssh_config: windows は WSL か Windows でだけ使えます")
		}
		home, err := h.WindowsHome(ctx)
		if err != nil {
			return "", "", err
		}
		p := path.Join(home, ".ssh", "config")
		if h.CrossesToWindows(bin) {
			return "", p, nil
		}
		return p, p, nil
	case "wsl":
		if !h.IsWSL() {
			return "", "", errors.New("ssh_config: wsl は WSL でだけ使えます")
		}
		home, err := h.HomeDir()
		if err != nil {
			return "", "", err
		}
		p := path.Join(home, ".ssh", "config") // WSL 側は Linux なので / 区切り
		if !h.CrossesToWindows(bin) {
			return "", p, nil
		}
		w, err := h.ToWindowsPath(ctx, p)
		return w, w, err
	}
	if h.CrossesToWindows(bin) {
		w, err := h.ToWindowsPath(ctx, setting)
		return w, w, err
	}
	return setting, setting, nil
}

// AWSConfigFor は aws_config の設定値から、aws に渡す環境変数と表示用のパスを返す。
// 対応は仕様 6.3 の表のとおり。aws.exe に WSL のパスを渡すときは WSLENV の /p で変換させる。
func (h *Host) AWSConfigFor(ctx context.Context, bin, setting string) (env []string, display string, err error) {
	switch setting {
	case "", "auto":
		if h.CrossesToWindows(bin) {
			home, err := h.WindowsHome(ctx)
			return nil, path.Join(home, ".aws", "config"), err
		}
		if p := h.Getenv("AWS_CONFIG_FILE"); p != "" {
			return nil, p, nil
		}
		home, err := h.HomeDir()
		return nil, filepath.Join(home, ".aws", "config"), err
	case "windows":
		if h.GOOS == "windows" {
			home, err := h.HomeDir()
			return nil, filepath.Join(home, ".aws", "config"), err
		}
		if !h.IsWSL() {
			return nil, "", errors.New("aws_config: windows は WSL か Windows でだけ使えます")
		}
		home, err := h.WindowsHome(ctx)
		if err != nil {
			return nil, "", err
		}
		cfg := path.Join(home, ".aws", "config")
		if h.CrossesToWindows(bin) {
			return nil, cfg, nil
		}
		return []string{
			"AWS_CONFIG_FILE=" + cfg,
			"AWS_SHARED_CREDENTIALS_FILE=" + path.Join(home, ".aws", "credentials"),
		}, cfg, nil
	case "wsl":
		if !h.IsWSL() {
			return nil, "", errors.New("aws_config: wsl は WSL でだけ使えます")
		}
		home, err := h.HomeDir()
		if err != nil {
			return nil, "", err
		}
		cfg := path.Join(home, ".aws", "config") // WSL 側は Linux なので / 区切り
		if !h.CrossesToWindows(bin) {
			return nil, cfg, nil
		}
		return []string{
			"AWS_CONFIG_FILE=" + cfg,
			"AWS_SHARED_CREDENTIALS_FILE=" + path.Join(home, ".aws", "credentials"),
			"WSLENV=" + appendWSLENV(h.Getenv("WSLENV"), "AWS_CONFIG_FILE/p", "AWS_SHARED_CREDENTIALS_FILE/p"),
		}, cfg, nil
	}
	if h.CrossesToWindows(bin) {
		w, err := h.ToWindowsPath(ctx, setting)
		if err != nil {
			return nil, "", err
		}
		return []string{"AWS_CONFIG_FILE=" + w, "WSLENV=" + appendWSLENV(h.Getenv("WSLENV"), "AWS_CONFIG_FILE")}, w, nil
	}
	return []string{"AWS_CONFIG_FILE=" + setting}, setting, nil
}

// appendWSLENV は既存の WSLENV の後ろに項目を足す。
func appendWSLENV(cur string, items ...string) string {
	if cur == "" {
		return strings.Join(items, ":")
	}
	return cur + ":" + strings.Join(items, ":")
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/platform/ && make lint`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/platform/configpath.go internal/platform/configpath_test.go
git commit -m "feat(platform): ssh_config / aws_config の参照先を実行バイナリに合わせて決める

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: platform — WSL と Windows の連携（ゲートウェイ、待ち受けの確認、後始末、hosts、VT）

**Files:**
- Create: `internal/platform/interop_parse.go`, `internal/platform/interop.go`, `internal/platform/console_windows.go`, `internal/platform/console_other.go`
- Test: `internal/platform/interop_test.go`

**Interfaces:**
- Consumes: 既存の `Host.Run`、`IsWSL`
- Produces:
  - `type Listener struct { Addr string; Port int }`、`func (l Listener) Exposed() bool`（`0.0.0.0` か `::`）
  - `type WinProc struct { PID int; Name, CommandLine string }`
  - `type HostsFile struct { Label, Path string }`
  - `func (h *Host) DefaultGateway(ctx context.Context) (string, error)` — `ip route show default`
  - `func (h *Host) WindowsListeners(ctx context.Context, ports []int) ([]Listener, error)` — `powershell.exe Get-NetTCPConnection`
  - `func (h *Host) CleanupWindows(ctx context.Context, ports []int) ([]WinProc, error)` — `Win32_Process` で探して `taskkill.exe /F /PID`
  - `func (h *Host) HostsFiles() []HostsFile`
  - `func HostsHas(data []byte, name string) bool` — `127.0.0.1 <name>` の行があるか
  - `func EnableVT(f *os.File) bool` — Windows で VT 処理を有効にする。ほかの OS では何もせず true

- [ ] **Step 1: 失敗するテストを書く**

`internal/platform/interop_test.go`:

```go
package platform

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// funcRunner は呼び出しを関数で受ける Runner。powershell のスクリプトのような長い引数の確認に使う。
type funcRunner func(name string, args []string) ([]byte, error)

func (f funcRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	return f(name, args)
}

func TestParseDefaultGateway(t *testing.T) {
	got, err := parseDefaultGateway([]byte("default via 172.29.16.1 dev eth0 proto kernel\n172.29.16.0/20 dev eth0 proto kernel scope link src 172.29.20.5\n"))
	if err != nil || got != "172.29.16.1" {
		t.Fatalf("parseDefaultGateway = %q, %v", got, err)
	}
	if _, err := parseDefaultGateway([]byte("172.29.16.0/20 dev eth0\n")); err == nil {
		t.Error("default が無ければエラー")
	}
}

func TestParseListeners(t *testing.T) {
	got := parseListeners([]byte("172.29.16.1 15432\r\n0.0.0.0 16379\r\n:: 8080\r\n\r\nbroken\r\n"))
	want := []Listener{{"172.29.16.1", 15432}, {"0.0.0.0", 16379}, {"::", 8080}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseListeners = %+v", got)
	}
	if got[0].Exposed() || !got[1].Exposed() || !got[2].Exposed() {
		t.Error("0.0.0.0 と :: だけが LAN に公開されている")
	}
}

func TestMatchForwardProcs(t *testing.T) {
	procs := parseWinProcs([]byte(strings.Join([]string{
		"100\tssh.exe\tC:\\Windows\\System32\\OpenSSH\\ssh.exe -N -o BatchMode=yes -L 127.0.0.1:15432:db:5432 -- admin@10.0.1.10",
		"101\tssh.exe\tssh.exe -N -L 127.0.0.1:25000:db:15432 -- other",
		"102\tssh.exe\tssh.exe -N -L [::1]:16379:redis:6379 -- x",
		"103\taws.exe\t\"C:\\Program Files\\Amazon\\AWSCLIV2\\aws.exe\" ssm start-session --parameters host=db,portNumber=5432,localPortNumber=35432",
		"104\tsession-manager-plugin.exe\tsession-manager-plugin.exe {\\\"localPortNumber\\\":[\\\"35432\\\"]} ap-northeast-1 StartSession",
		"105\taws.exe\taws.exe ssm start-session --parameters localPortNumber=354320",
		"106\tnotepad.exe\tnotepad.exe 15432",
		"bad line",
	}, "\r\n")))
	got := matchForwardProcs(procs, []int{15432, 16379, 35432})
	var pids []int
	for _, p := range got {
		pids = append(pids, p.PID)
	}
	if !reflect.DeepEqual(pids, []int{100, 102, 103, 104}) {
		t.Fatalf("一致した pid = %v", pids)
	}
}

func TestHostsHas(t *testing.T) {
	data := []byte("# comment 127.0.0.1 skip.local\r\n127.0.0.1 localhost app.local # app\r\n::1 v6.local\r\n")
	if !HostsHas(data, "app.local") || !HostsHas(data, "APP.local") {
		t.Error("127.0.0.1 の行にあれば true（大文字小文字は区別しない）")
	}
	if HostsHas(data, "skip.local") || HostsHas(data, "v6.local") || HostsHas(data, "none.local") {
		t.Error("コメントや 127.0.0.1 以外の行は数えない")
	}
}

func TestDefaultGateway(t *testing.T) {
	h := newHost(nil, nil, nil, fakeRunner{"ip route show default": "default via 172.29.16.1 dev eth0\n"})
	if got, err := h.DefaultGateway(context.Background()); err != nil || got != "172.29.16.1" {
		t.Fatalf("DefaultGateway = %q, %v", got, err)
	}
}

func TestWindowsListeners(t *testing.T) {
	var script string
	h := newHost(nil, nil, nil, nil)
	h.Run = funcRunner(func(_ string, args []string) ([]byte, error) {
		script = args[len(args)-1]
		return []byte("0.0.0.0 15432\r\n"), nil
	})
	got, err := h.WindowsListeners(context.Background(), []int{15432, 16379})
	if err != nil || len(got) != 1 || !got[0].Exposed() {
		t.Fatalf("WindowsListeners = %+v, %v", got, err)
	}
	if !strings.Contains(script, "Get-NetTCPConnection -State Listen -LocalPort 15432,16379") {
		t.Errorf("script = %s", script)
	}
	if got, _ := h.WindowsListeners(context.Background(), nil); got != nil {
		t.Error("ポートが無ければ呼ばない")
	}
}

func TestCleanupWindows(t *testing.T) {
	var killed []string
	h := newHost(nil, nil, nil, nil)
	h.Run = funcRunner(func(name string, args []string) ([]byte, error) {
		switch name {
		case "powershell.exe":
			if !strings.Contains(args[len(args)-1], "Win32_Process") {
				return nil, errors.New("想定外のスクリプト")
			}
			return []byte("100\tssh.exe\tssh.exe -L 127.0.0.1:15432:db:5432 -- x\r\n200\tssh.exe\tssh.exe -L 127.0.0.1:9999:db:5432 -- y\r\n300\taws.exe\taws.exe localPortNumber=35432\r\n"), nil
		case "taskkill.exe":
			killed = append(killed, strings.Join(args, " "))
			if args[2] == "300" {
				return nil, errors.New("access denied")
			}
			return nil, nil
		}
		return nil, errors.New("想定外: " + name)
	})
	got, err := h.CleanupWindows(context.Background(), []int{15432, 35432})
	if !reflect.DeepEqual(killed, []string{"/F /PID 100", "/F /PID 300"}) {
		t.Errorf("taskkill = %v", killed)
	}
	if len(got) != 1 || got[0].PID != 100 {
		t.Errorf("終了できたもの = %+v", got)
	}
	if err == nil || !strings.Contains(err.Error(), "aws.exe (pid 300)") {
		t.Errorf("失敗したものはエラーにまとめる: %v", err)
	}
}

func TestHostsFiles(t *testing.T) {
	if got := newHost(nil, nil, nil, nil).HostsFiles(); len(got) != 1 || got[0].Path != "/etc/hosts" {
		t.Errorf("Linux = %+v", got)
	}
	wsl := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, nil, nil).HostsFiles()
	if len(wsl) != 2 || wsl[0].Label != "WSL" || wsl[1].Path != "/mnt/c/Windows/System32/drivers/etc/hosts" {
		t.Errorf("WSL = %+v", wsl)
	}
	win := newHost(map[string]string{"SystemRoot": `D:\Windows`}, nil, nil, nil)
	win.GOOS = "windows"
	if got := win.HostsFiles(); len(got) != 1 || got[0].Label != "Windows" || !strings.HasPrefix(got[0].Path, `D:\Windows`) {
		t.Errorf("Windows = %+v", got)
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/platform/ -run 'Gateway|Listeners|Procs|Hosts|Cleanup'`
Expected: FAIL（未定義）

- [ ] **Step 3: 実装する**

`internal/platform/interop_parse.go`:

```go
package platform

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// Listener は TCP の待ち受け1件。
type Listener struct {
	Addr string
	Port int
}

// Exposed は LAN に公開されている（0.0.0.0 か :: で待ち受けている）かを返す。
func (l Listener) Exposed() bool { return l.Addr == "0.0.0.0" || l.Addr == "::" }

// WinProc は Windows 側のプロセス1件。
type WinProc struct {
	PID         int
	Name        string
	CommandLine string
}

func lines(out []byte) []string {
	return strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n")
}

var defaultRouteRe = regexp.MustCompile(`(?m)^default via (\S+)`)

// parseDefaultGateway は `ip route show default` の出力からゲートウェイの IP を取り出す。
func parseDefaultGateway(out []byte) (string, error) {
	m := defaultRouteRe.FindSubmatch(out)
	if m == nil {
		return "", errors.New("ip route の出力にデフォルトゲートウェイがありません")
	}
	return string(m[1]), nil
}

// parseListeners は「アドレス ポート」の行を読む。形式が違う行は無視する。
func parseListeners(out []byte) []Listener {
	var ls []Listener
	for _, line := range lines(out) {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		p, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		ls = append(ls, Listener{Addr: f[0], Port: p})
	}
	return ls
}

// parseWinProcs は「pid<TAB>名前<TAB>コマンドライン」の行を読む。形式が違う行は無視する。
func parseWinProcs(out []byte) []WinProc {
	var ps []WinProc
	for _, line := range lines(out) {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
		if err != nil {
			continue
		}
		ps = append(ps, WinProc{PID: pid, Name: parts[1], CommandLine: parts[2]})
	}
	return ps
}

// matchForwardProcs は ports のどれかを使っているプロセスを返す。
// ssh.exe は `-L <bind>:<port>:`、aws.exe と session-manager-plugin.exe は localPortNumber で照合する。
// ローカルポートは設定全体で一意なので、別のトンネルのプロセスと取り違えることはない。
func matchForwardProcs(procs []WinProc, ports []int) []WinProc {
	var out []WinProc
	for _, p := range procs {
		for _, port := range ports {
			if procUsesPort(p, port) {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

func procUsesPort(p WinProc, port int) bool {
	n := strconv.Itoa(port)
	switch strings.ToLower(p.Name) {
	case "ssh.exe":
		re := regexp.MustCompile(`(?:^|\s)-L\s*"?(?:\[[^\]]+\]:|[^\s":\[]+:)?` + n + `:`)
		return re.MatchString(p.CommandLine)
	case "aws.exe", "session-manager-plugin.exe":
		re := regexp.MustCompile(`localPortNumber[=":\[\s\\]*` + n + `\b`)
		return re.MatchString(p.CommandLine)
	}
	return false
}

// HostsHas は hosts ファイルに「127.0.0.1 <name>」の行があるかを返す。
func HostsHas(data []byte, name string) bool {
	for _, line := range lines(data) {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "127.0.0.1" {
			continue
		}
		for _, h := range f[1:] {
			if strings.EqualFold(h, name) {
				return true
			}
		}
	}
	return false
}
```

`internal/platform/interop.go`:

```go
package platform

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultGateway は WSL から見たデフォルトゲートウェイ（Windows 側の WSL 用仮想 NIC）の IP を返す。
// この IP は再起動で変わるので、接続のたびに呼ぶ。
func (h *Host) DefaultGateway(ctx context.Context) (string, error) {
	out, err := h.Run.Output(ctx, "ip", "route", "show", "default")
	if err != nil {
		return "", fmt.Errorf("ip route を実行できません: %w", err)
	}
	return parseDefaultGateway(out)
}

func (h *Host) powershell(ctx context.Context, script string) ([]byte, error) {
	return h.Run.Output(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
}

// WindowsListeners は、Windows 側で ports を待ち受けているアドレスを返す。
func (h *Host) WindowsListeners(ctx context.Context, ports []int) ([]Listener, error) {
	if len(ports) == 0 {
		return nil, nil
	}
	ps := make([]string, len(ports))
	for i, p := range ports {
		ps[i] = strconv.Itoa(p)
	}
	script := "Get-NetTCPConnection -State Listen -LocalPort " + strings.Join(ps, ",") +
		` -ErrorAction SilentlyContinue | ForEach-Object { "$($_.LocalAddress) $($_.LocalPort)" }`
	out, err := h.powershell(ctx, script)
	if err != nil {
		return nil, fmt.Errorf("Get-NetTCPConnection を実行できません: %w", err)
	}
	return parseListeners(out), nil
}

// procScript は対象のプロセスを「pid<TAB>名前<TAB>コマンドライン」で1行ずつ出す。
const procScript = "Get-CimInstance Win32_Process -Filter \"Name='ssh.exe' OR Name='aws.exe' OR Name='session-manager-plugin.exe'\"" +
	" | ForEach-Object { \"$($_.ProcessId)\t$($_.Name)\t$($_.CommandLine)\" }"

// CleanupWindows は、ports を使っている Windows 側の ssh.exe / aws.exe / session-manager-plugin.exe を
// taskkill で終了させる。WSL 側のプロセスを止めても Windows 側に残ることがあるため、up の停止後に呼ぶ（仕様 6.5）。
// 終了させたプロセスを返す。終了させられなかったものはエラーにまとめる。
func (h *Host) CleanupWindows(ctx context.Context, ports []int) ([]WinProc, error) {
	out, err := h.powershell(ctx, procScript)
	if err != nil {
		return nil, fmt.Errorf("Windows 側のプロセスを一覧できません: %w", err)
	}
	var killed []WinProc
	var errs []error
	for _, p := range matchForwardProcs(parseWinProcs(out), ports) {
		if _, err := h.Run.Output(ctx, "taskkill.exe", "/F", "/PID", strconv.Itoa(p.PID)); err != nil {
			errs = append(errs, fmt.Errorf("%s (pid %d) を終了できません: %w", p.Name, p.PID, err))
			continue
		}
		killed = append(killed, p)
	}
	return killed, errors.Join(errs...)
}

// HostsFile は確認する hosts ファイル1つ。Label は表示用（WSL / Windows）。
type HostsFile struct {
	Label string
	Path  string
}

// HostsFiles は、ブラウザなどが名前解決に使う hosts ファイルの一覧を返す。WSL では両側を返す。
func (h *Host) HostsFiles() []HostsFile {
	switch {
	case h.GOOS == "windows":
		root := h.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return []HostsFile{{Label: "Windows", Path: filepath.Join(root, "System32", "drivers", "etc", "hosts")}}
	case h.IsWSL():
		return []HostsFile{
			{Label: "WSL", Path: "/etc/hosts"},
			{Label: "Windows", Path: "/mnt/c/Windows/System32/drivers/etc/hosts"},
		}
	}
	return []HostsFile{{Path: "/etc/hosts"}}
}
```

`internal/platform/console_windows.go`:

```go
//go:build windows

package platform

import (
	"os"

	"golang.org/x/sys/windows"
)

// EnableVT はコンソールで ANSI エスケープを解釈させる。有効にできなければ false を返す。
func EnableVT(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
```

`internal/platform/console_other.go`:

```go
//go:build !windows

package platform

import "os"

// EnableVT は Windows 以外では何もしない。端末は ANSI エスケープを解釈できる。
func EnableVT(*os.File) bool { return true }
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/platform/ && GOOS=windows go vet ./internal/platform/ && make lint`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/platform
git commit -m "feat(platform): ゲートウェイの取得、Windows 側の待ち受けの確認と後始末、hosts、VT を追加する

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: driver — Classify、ssh の -F と待ち受けアドレスの差し替え

**Files:**
- Modify: `internal/driver/driver.go`, `internal/driver/ssh/ssh.go`, `internal/supervisor/supervisor_test.go`
- Test: `internal/driver/ssh/ssh_test.go`

**Interfaces:**
- Consumes: なし
- Produces:
  - `type Exit struct { Fatal bool; Message string }`
  - `Driver` に `Classify(err error, stderrTail []byte) Exit` を追加
  - `type Rebinder interface { Driver; PreviewAt(bind string) []ProcessSpec; PrepareAt(ctx context.Context, bind string) ([]ProcessSpec, error) }`
  - `ssh.Options.ConfigFile string`（空でなければ `-F <値>` を `BatchMode=yes` の直後、`-J` の前に付ける）
  - `*ssh.Driver` は `Rebinder` を満たす。`Classify` は常に `Exit{}`（Retry）

- [ ] **Step 1: 失敗するテストを書く**

`internal/driver/ssh/ssh_test.go` の末尾に追加する（import に `errors` を足す）。

```go
func TestConfigFileAndRebind(t *testing.T) {
	d := New(Options{
		Tunnel: "dev.db", Bin: "ssh.exe",
		Route:      config.Route{Dest: "admin@ap", Jumps: []string{"bastion"}},
		Forwards:   []config.Forward{{Bind: "127.0.0.1", LocalPort: 15432, RemoteHost: "db", RemotePort: 5432}},
		Keepalive:  30 * time.Second,
		ConfigFile: `\\wsl.localhost\Ubuntu\home\u\.ssh\config`,
	})
	got := strings.Join(d.Preview()[0].Args, " ")
	if !strings.Contains(got, `-o BatchMode=yes -F \\wsl.localhost\Ubuntu\home\u\.ssh\config -J bastion -L 127.0.0.1:15432:db:5432`) {
		t.Errorf("-F は BatchMode の直後、-J の前: %s", got)
	}

	specs, err := d.PrepareAt(context.Background(), "172.29.16.1")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(specs[0].Args, " "); !strings.Contains(got, "-L 172.29.16.1:15432:db:5432") {
		t.Errorf("待ち受けアドレスを差し替える: %s", got)
	}
	if !reflect.DeepEqual(specs[0].Ports, []driver.Listen{{Addr: "172.29.16.1", Port: 15432}}) {
		t.Errorf("ports も差し替える: %+v", specs[0].Ports)
	}
	if !reflect.DeepEqual(d.PreviewAt(""), d.Preview()) {
		t.Error("PreviewAt(\"\") は Preview と同じ")
	}
	var _ driver.Rebinder = d
}

func TestClassifyAlwaysRetry(t *testing.T) {
	d := New(Options{Tunnel: "x", Bin: "ssh", Route: config.Route{Dest: "h"}})
	if ex := d.Classify(errors.New("exit status 255"), []byte("Permission denied")); ex.Fatal {
		t.Error("ssh の終了はすべて再接続する")
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/driver/...`
Expected: FAIL（`ConfigFile`、`PrepareAt`、`Classify` が未定義）

- [ ] **Step 3: 実装する**

`internal/driver/driver.go` の `Driver` を次に置き換え、`Exit` と `Rebinder` を足す。

```go
// Exit はプロセスの終了（または Prepare の失敗）をどう扱うか。
type Exit struct {
	Fatal   bool   // true なら再接続せずに failed にする
	Message string // Fatal のときに表示する案内
}

// Driver はトンネルの種類ごとのコマンドの組み立て方。
type Driver interface {
	// Preview は外部呼び出しをせずに、起動するプロセスを返す（cmd 用）。
	Preview() []ProcessSpec
	// Prepare は接続のたびに呼ばれ、起動するプロセスを返す。並び順は毎回同じにする。
	Prepare(ctx context.Context) ([]ProcessSpec, error)
	// Classify は終了の理由（stderr の末尾を含む）から、再接続するかを決める。
	Classify(err error, stderrTail []byte) Exit
}

// Rebinder は待ち受けアドレスを差し替えてプロセスを組み立てられるドライバ。relay モードで使う。
type Rebinder interface {
	Driver
	PreviewAt(bind string) []ProcessSpec
	PrepareAt(ctx context.Context, bind string) ([]ProcessSpec, error)
}
```

`internal/driver/ssh/ssh.go` の `Options` を置き換える。

```go
// Options は SSH ドライバの入力。
type Options struct {
	Tunnel     string
	Bin        string
	Route      config.Route
	Forwards   []config.Forward
	Keepalive  time.Duration
	ConfigFile string // -F に渡す ssh config。空なら付けない
}
```

同じファイルの `args` から最後までを置き換える。

```go
// args は ssh の引数を組み立てる。bind が空でなければ、全 forward の待ち受けアドレスをそれにする。
func (d *Driver) args(bind string) []string {
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
	if d.o.ConfigFile != "" {
		args = append(args, "-F", d.o.ConfigFile)
	}
	if len(d.o.Route.Jumps) > 0 {
		args = append(args, "-J", strings.Join(d.o.Route.Jumps, ","))
	}
	for _, f := range d.o.Forwards {
		args = append(args, "-L", fmt.Sprintf("%s:%d:%s:%d", bracket(bindOf(f, bind)), f.LocalPort, bracket(f.RemoteHost), f.RemotePort))
	}
	return append(args, "--", d.o.Route.Dest)
}

func bindOf(f config.Forward, bind string) string {
	if bind != "" {
		return bind
	}
	return f.Bind
}

// PreviewAt は待ち受けアドレスを bind にした ssh プロセスを返す。bind が空なら設定のまま。
func (d *Driver) PreviewAt(bind string) []driver.ProcessSpec {
	ports := make([]driver.Listen, len(d.o.Forwards))
	for i, f := range d.o.Forwards {
		ports[i] = driver.Listen{Addr: bindOf(f, bind), Port: f.LocalPort}
	}
	return []driver.ProcessSpec{{Tunnel: d.o.Tunnel, Path: d.o.Bin, Args: d.args(bind), Ports: ports}}
}

// Preview は起動する ssh プロセスを返す。
func (d *Driver) Preview() []driver.ProcessSpec { return d.PreviewAt("") }

// PrepareAt は PreviewAt と同じものを返す。
func (d *Driver) PrepareAt(_ context.Context, bind string) ([]driver.ProcessSpec, error) {
	return d.PreviewAt(bind), nil
}

// Prepare は Preview と同じものを返す。
func (d *Driver) Prepare(ctx context.Context) ([]driver.ProcessSpec, error) {
	return d.PrepareAt(ctx, "")
}

// Classify は常に再接続とする。ssh の終了コードからは、直る問題かどうかを区別できない。
func (d *Driver) Classify(error, []byte) driver.Exit { return driver.Exit{} }
```

`internal/supervisor/supervisor_test.go` の `staticDriver` に `Classify` を足す。

```go
func (d staticDriver) Classify(error, []byte) driver.Exit { return driver.Exit{} }
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./... && make lint`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/driver internal/supervisor/supervisor_test.go
git commit -m "feat(driver): Classify を追加し、ssh ドライバに -F と待ち受けアドレスの差し替えを足す

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: state — 状態ファイル

**Files:**
- Create: `internal/state/state.go`
- Test: `internal/state/state_test.go`
- Modify: `.golangci.yml`

**Interfaces:**
- Consumes: なし（生存判定は関数で受け取る。cli から `platform.IsAlive` を渡す）
- Produces:
  - 状態の定数 `Starting = "starting"`、`Up = "up"`、`Unhealthy = "unhealthy"`、`Backoff = "backoff"`、`Failed = "failed"`、`Stopped = "stopped"`
  - `type Slot struct`（JSON のキーは仕様 7.1 のとおり: `tunnel`, `env`, `protected`, `ports`, `state`, `child_pid`, `reconnects`, `next_retry_at`, `last_error`, `healthy`, `checked_at`）
  - `type Run struct { PID int; StartedAt time.Time; Config string; Slots []Slot }`（`pid`, `started_at`, `config`, `slots`）
  - `type File struct { Path string }`、`func New(dir string, pid int) *File`、`func (f *File) Write(r Run) error`、`func (f *File) Remove() error`
  - `func List(dir string, alive func(pid int, startedAt time.Time) bool) ([]Run, error)` — 残骸は削除し、生きている up だけを pid 順に返す
  - `func FindPort(runs []Run, addr string) (Run, Slot, bool)` — `addr`（`127.0.0.1:15432` の形）を使っているスロット

- [ ] **Step 1: 失敗するテストを書く**

`internal/state/state_test.go`:

```go
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sample(pid int) Run {
	next := time.Date(2026, 10, 23, 10, 0, 12, 0, time.UTC)
	return Run{
		PID: pid, StartedAt: time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC), Config: "/path/hashigo.yaml",
		Slots: []Slot{{
			Tunnel: "dev.db", Env: "dev", Ports: []string{"127.0.0.1:15432"},
			State: Backoff, ChildPID: 5678, Reconnects: 3, NextRetryAt: &next, LastError: "exit status 255",
		}},
	}
}

func TestWriteAndList(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	f := New(dir, 1234)
	if err := f.Write(sample(1234)); err != nil {
		t.Fatal(err)
	}
	if f.Path != filepath.Join(dir, "1234.json") {
		t.Errorf("Path = %s", f.Path)
	}
	runs, err := List(dir, func(int, time.Time) bool { return true })
	if err != nil || len(runs) != 1 {
		t.Fatalf("List = %+v, %v", runs, err)
	}
	if got := runs[0].Slots[0]; got.State != Backoff || got.Reconnects != 3 || got.NextRetryAt == nil {
		t.Errorf("読み戻した値 = %+v", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("一時ファイルが残っている: %v", entries)
	}
}

func TestJSONKeys(t *testing.T) {
	b, err := json.Marshal(sample(1))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"pid"`, `"started_at"`, `"config"`, `"slots"`, `"tunnel"`, `"env"`, `"protected"`, `"ports"`,
		`"state"`, `"child_pid"`, `"reconnects"`, `"next_retry_at"`, `"last_error"`, `"healthy"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("キー %s が無い: %s", k, b)
		}
	}
}

func TestListRemovesStale(t *testing.T) {
	dir := t.TempDir()
	for _, pid := range []int{10, 20} {
		if err := New(dir, pid).Write(sample(pid)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "30.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "memo.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, err := List(dir, func(pid int, _ time.Time) bool { return pid == 20 })
	if err != nil || len(runs) != 1 || runs[0].PID != 20 {
		t.Fatalf("List = %+v, %v", runs, err)
	}
	for _, name := range []string{"10.json", "30.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s は残骸として削除する", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "memo.txt")); err != nil {
		t.Error("状態ファイル以外は触らない")
	}
}

func TestListMissingDir(t *testing.T) {
	runs, err := List(filepath.Join(t.TempDir(), "none"), func(int, time.Time) bool { return true })
	if err != nil || runs != nil {
		t.Fatalf("ディレクトリが無ければ空: %+v, %v", runs, err)
	}
}

func TestRemove(t *testing.T) {
	f := New(t.TempDir(), 1)
	if err := f.Write(sample(1)); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(); err != nil {
		t.Errorf("2回目の Remove もエラーにしない: %v", err)
	}
}

func TestFindPort(t *testing.T) {
	runs := []Run{sample(1234)}
	run, slot, ok := FindPort(runs, "127.0.0.1:15432")
	if !ok || run.PID != 1234 || slot.Tunnel != "dev.db" {
		t.Fatalf("FindPort = %+v %+v %v", run, slot, ok)
	}
	if _, _, ok := FindPort(runs, "127.0.0.1:9"); ok {
		t.Error("無いポートは false")
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/state/`
Expected: FAIL（パッケージが無い）

- [ ] **Step 3: 実装する**

`internal/state/state.go`:

```go
// Package state は up 1回ごとの状態ファイルを読み書きする。status と、起動前の衝突相手の特定に使う。
package state

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// スロットの状態。
const (
	Starting  = "starting"
	Up        = "up"
	Unhealthy = "unhealthy"
	Backoff   = "backoff"
	Failed    = "failed"
	Stopped   = "stopped"
)

// Slot はスロット1つの状態。
type Slot struct {
	Tunnel      string     `json:"tunnel"`
	Env         string     `json:"env"`
	Protected   bool       `json:"protected"`
	Ports       []string   `json:"ports"`
	State       string     `json:"state"`
	ChildPID    int        `json:"child_pid"`
	Reconnects  int        `json:"reconnects"`
	NextRetryAt *time.Time `json:"next_retry_at,omitempty"`
	LastError   string     `json:"last_error"`
	Healthy     bool       `json:"healthy"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
}

// Run は up 1回分の状態ファイルの中身。
type Run struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	Config    string    `json:"config"`
	Slots     []Slot    `json:"slots"`
}

// File は状態ファイル1つ。複数の goroutine から Write してよい。
type File struct {
	Path string
	mu   sync.Mutex
}

// New は dir/<pid>.json の状態ファイルを返す。
func New(dir string, pid int) *File {
	return &File{Path: filepath.Join(dir, strconv.Itoa(pid)+".json")}
}

// Write は r を書く。読み手が途中の内容を見ないよう、一時ファイルに書いてから rename する。
func (f *File) Write(r Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := filepath.Dir(f.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), f.Path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Remove は状態ファイルを削除する。無ければ何もしない。
func (f *File) Remove() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.Remove(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// List は dir の状態ファイルを読み、生きている up のものだけを pid 順に返す。
// 記録した pid が生きていない、起動時刻が違う（pid が再利用された）、JSON として壊れているファイルは残骸として削除する。
func List(dir string, alive func(pid int, startedAt time.Time) bool) ([]Run, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []Run
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".tmp-") {
			continue
		}
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p)
		if err != nil {
			continue // 書き換えの途中などで読めないものは、残骸とみなさない
		}
		var r Run
		if err := json.Unmarshal(b, &r); err != nil || !alive(r.PID, r.StartedAt) {
			_ = os.Remove(p)
			continue
		}
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].PID < runs[j].PID })
	return runs, nil
}

// FindPort は addr（127.0.0.1:15432 の形）を使っているスロットを探す。
func FindPort(runs []Run, addr string) (Run, Slot, bool) {
	for _, r := range runs {
		for _, s := range r.Slots {
			if slices.Contains(s.Ports, addr) {
				return r, s, true
			}
		}
	}
	return Run{}, Slot{}, false
}
```

`.golangci.yml` の depguard の `rules` に追加する（`testutil` の前）。

```yaml
        # state は状態ファイルの読み書きだけを行う葉である。
        state-layer:
          files:
            - "**/internal/state/**"
          deny:
            - pkg: github.com/kukv/hashigo/internal/config
              desc: state は設定ファイルを知らない
            - pkg: github.com/kukv/hashigo/internal/driver
              desc: state はドライバを知らない
            - pkg: github.com/kukv/hashigo/internal/supervisor
              desc: state はスーパーバイザを知らない
            - pkg: github.com/kukv/hashigo/internal/cli
              desc: state は CLI を知らない
```

`platform-layer` の `deny` に追加する。

```yaml
            - pkg: github.com/kukv/hashigo/internal/state
              desc: platform は状態ファイルを知らない
```

`exclusions.rules` に追加する。

```yaml
      # 状態ファイルのディレクトリは StateDir で決めた本人の場所で、読むのはそこにある *.json だけである。
      - path: internal/state/state\.go
        linters:
          - gosec
        text: "G304"
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test -race ./internal/state/ && make lint`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/state .golangci.yml
git commit -m "feat(state): up ごとの状態ファイルの読み書きと残骸の削除を追加する

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: supervisor — 状態遷移と再接続（バックオフ、Classify、Fatal）

**Files:**
- Modify: `internal/supervisor/supervisor.go`（全体を置き換える）, `internal/driver/driver.go`
- Test: `internal/supervisor/supervisor_test.go`

**Interfaces:**
- Consumes: Task 2 の `platform.StartChild`、Task 5 の `driver.Exit` / `Classify`、Task 6 の `state.Slot` と状態の定数
- Produces:
  - `type Unit struct { ID, Env string; Protected, Reconnect bool; Driver driver.Driver }`
  - `type Clock interface { Now() time.Time; After(d time.Duration) <-chan time.Time }`
  - `Supervisor` のフィールド `Out io.Writer`、`StopTimeout time.Duration`、`Clock Clock`（nil なら実時間）
  - `func (s *Supervisor) Run(ctx context.Context, units []Unit) error` — キャンセルで nil、全スロットが failed で `ErrAllExited`
  - `func (s *Supervisor) Slots() []state.Slot` — 各スロットの現在の状態（スロットの並びは `Preview()` の順）
  - `func (l driver.Listen) String() string`（`127.0.0.1:15432`）、`func (l driver.Listen) DialAddr() string`（`0.0.0.0` → `127.0.0.1`、`::` → `::1`）
  - 出力する行: `[id] 起動しました (pid N)`、`[id] 終了しました: <理由>`、`[id] 準備に失敗しました: <err>`、`[id] Ns 後に再接続します`、`[id] failed: <理由>`、`[id] 停止しました`、`[id] 起動できませんでした: <err>`

動き:
- スロットは `Driver.Preview()` の並びで作る（R4）。各スロットは接続のたびに `Prepare` を呼び、同じ位置の Spec を使う
- 終了したら `Classify(err, stderr の末尾 4KB)`。Fatal なら failed（理由は `Exit.Message`）。`Reconnect` が false なら failed（理由「reconnect: false のため再接続しません」）。それ以外は backoff
- バックオフは 1s から倍にし上限 60s。直前の接続が 60 秒以上続いていたら 1s に戻す
- `Prepare` の失敗も `Classify(err, nil)` で同じように扱う
- 子プロセスを起動できなければ failed（R5）
- ctx のキャンセルで、動いているものは停止、backoff 中のものはそのまま stopped にする

- [ ] **Step 1: 失敗するテストを書く**

`internal/supervisor/supervisor_test.go` の import に `bytes`、`sync`、`github.com/kukv/hashigo/internal/state` を足し、末尾に追加する。

```go
// fakeClock は After をすぐ発火させ、待った時間を記録する。Now は呼ぶたびに step だけ進む。
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	step  time.Duration
	waits []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.step)
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	c.waits = append(c.waits, d)
	c.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func (c *fakeClock) Waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}

// stuckClock の After は発火しない。backoff 中の停止を確かめるのに使う。
type stuckClock struct{}

func (stuckClock) Now() time.Time                       { return time.Now() }
func (stuckClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// firstState は最初のスロットの状態を返す。Run がスロットを作る前は ""。
func firstState(s *Supervisor) string {
	ss := s.Slots()
	if len(ss) == 0 {
		return ""
	}
	return ss[0].State
}

func reconnecting(u Unit) Unit {
	u.Reconnect = true
	return u
}

func TestBackoffDoubles(t *testing.T) {
	var out testutil.SyncBuffer
	clk := &fakeClock{}
	cancel, done := start(t, &Supervisor{Out: &out, Clock: clk},
		reconnecting(fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=10ms")))
	testutil.Eventually(t, 10*time.Second, func() bool { return len(clk.Waits()) >= 8 }, "再接続が 8 回起きない")
	cancel()
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		if got := clk.Waits()[i]; got != w*time.Second {
			t.Errorf("%d 回目の待ち = %s, want %s", i+1, got, w*time.Second)
		}
	}
	if s := out.String(); !strings.Contains(s, "[a] 1s 後に再接続します") || !strings.Contains(s, "[a] 60s 後に再接続します") {
		t.Errorf("出力 = %s", s)
	}
}

func TestBackoffResetsAfterStableRun(t *testing.T) {
	clk := &fakeClock{step: 61 * time.Second} // 毎回 60 秒以上動いたことにする
	cancel, done := start(t, &Supervisor{Out: io.Discard, Clock: clk},
		reconnecting(fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=10ms")))
	testutil.Eventually(t, 10*time.Second, func() bool { return len(clk.Waits()) >= 3 }, "再接続が 3 回起きない")
	cancel()
	_ = wait(t, done)
	for i, got := range clk.Waits()[:3] {
		if got != time.Second {
			t.Errorf("%d 回目の待ち = %s, 安定して動いたあとは 1s に戻す", i+1, got)
		}
	}
}

// classifyDriver は stderr に "token expired" があれば Fatal にする。
type classifyDriver struct{ staticDriver }

func (classifyDriver) Classify(_ error, tail []byte) driver.Exit {
	if bytes.Contains(tail, []byte("token expired")) {
		return driver.Exit{Fatal: true, Message: "sso login してください"}
	}
	return driver.Exit{}
}

func TestFatalStops(t *testing.T) {
	var out testutil.SyncBuffer
	u := fakeUnit("a", testutil.FreePort(t), "FAKE_STDERR=token expired", "FAKE_EXIT_AFTER=50ms")
	u.Driver = classifyDriver{u.Driver.(staticDriver)}
	s := &Supervisor{Out: &out, Clock: &fakeClock{}}
	_, done := start(t, s, reconnecting(u))
	if err := wait(t, done); !errors.Is(err, ErrAllExited) {
		t.Fatalf("Fatal なら再接続せずに終わる: %v", err)
	}
	if !strings.Contains(out.String(), "[a] failed: sso login してください") {
		t.Errorf("出力 = %s", out.String())
	}
	if got := s.Slots()[0]; got.State != state.Failed || got.LastError != "sso login してください" {
		t.Errorf("状態 = %+v", got)
	}
}

// flakyDriver は最初の fails 回だけ Prepare に失敗する。
type flakyDriver struct {
	staticDriver
	mu    sync.Mutex
	fails int
}

func (d *flakyDriver) Prepare(ctx context.Context) ([]driver.ProcessSpec, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fails > 0 {
		d.fails--
		return nil, errors.New("RUNNING のタスクがありません")
	}
	return d.staticDriver.Prepare(ctx)
}

func TestPrepareFailureRetries(t *testing.T) {
	var out testutil.SyncBuffer
	port := testutil.FreePort(t)
	u := fakeUnit("a", port)
	u.Driver = &flakyDriver{staticDriver: u.Driver.(staticDriver), fails: 2}
	clk := &fakeClock{}
	cancel, done := start(t, &Supervisor{Out: &out, Clock: clk}, reconnecting(u))
	testutil.WaitPort(t, port, 5*time.Second)
	cancel()
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if got := clk.Waits(); len(got) != 2 || got[0] != time.Second || got[1] != 2*time.Second {
		t.Errorf("待ち = %v", got)
	}
	if !strings.Contains(out.String(), "[a] 準備に失敗しました: RUNNING のタスクがありません") {
		t.Errorf("出力 = %s", out.String())
	}
}

func TestNoReconnectFails(t *testing.T) {
	var out testutil.SyncBuffer
	s := &Supervisor{Out: &out}
	_, done := start(t, s, fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=50ms"))
	if err := wait(t, done); !errors.Is(err, ErrAllExited) {
		t.Fatal(err)
	}
	if got := s.Slots()[0]; got.State != state.Failed || !strings.Contains(got.LastError, "reconnect: false") {
		t.Errorf("状態 = %+v", got)
	}
}

func TestStopDuringBackoff(t *testing.T) {
	var out testutil.SyncBuffer
	s := &Supervisor{Out: &out, Clock: stuckClock{}}
	cancel, done := start(t, s, reconnecting(fakeUnit("a", testutil.FreePort(t), "FAKE_EXIT_AFTER=10ms")))
	testutil.Eventually(t, 5*time.Second, func() bool { return firstState(s) == state.Backoff }, "backoff にならない")
	if got := s.Slots()[0]; got.NextRetryAt == nil || got.ChildPID != 0 {
		t.Errorf("backoff 中の状態 = %+v", got)
	}
	cancel()
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if got := s.Slots()[0].State; got != state.Stopped {
		t.Errorf("状態 = %s", got)
	}
}

func TestStderrTailIsBounded(t *testing.T) {
	lw := &lineWriter{out: &syncWriter{w: io.Discard}}
	_, _ = lw.Write(bytes.Repeat([]byte("x"), 10000))
	_, _ = lw.Write([]byte("end\n"))
	tail := lw.tail()
	if len(tail) != tailSize || !bytes.HasSuffix(tail, []byte("end\n")) {
		t.Errorf("末尾 %d バイトだけ残す: len=%d", tailSize, len(tail))
	}
}
```

import に `io` も足す。`TestStartFailure` の最後に状態の確認を足す。

```go
	if got := s.Slots()[0].State; got != state.Failed {
		t.Errorf("起動できなければ failed: %s", got)
	}
```

そのために `TestStartFailure` の `start(t, &Supervisor{Out: &out}, u)` を次に変える。

```go
	s := &Supervisor{Out: &out}
	_, done := start(t, s, u)
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/supervisor/`
Expected: FAIL（`Clock`、`Reconnect`、`Slots`、`tail`、`tailSize` が未定義）

- [ ] **Step 3: 実装する**

`internal/driver/driver.go` の `Listen` の後ろに追加する（import に `net`、`strconv` を足す）。

```go
// String は 127.0.0.1:15432 の形で返す。状態ファイルと表示に使う。
func (l Listen) String() string { return net.JoinHostPort(l.Addr, strconv.Itoa(l.Port)) }

// DialAddr は接続の確認に使うアドレスを返す。全アドレスで待ち受けていれば自分自身に接続する。
func (l Listen) DialAddr() string {
	addr := l.Addr
	switch addr {
	case "", "0.0.0.0":
		addr = "127.0.0.1"
	case "::":
		addr = "::1"
	}
	return net.JoinHostPort(addr, strconv.Itoa(l.Port))
}
```

`internal/supervisor/supervisor.go` を次の内容で置き換える。

```go
// Package supervisor は子プロセスを起動・監視し、終了したら張り直す。
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
	"github.com/kukv/hashigo/internal/state"
)

// ErrAllExited は、停止の指示が無いまま全スロットが failed になったことを表す。
var ErrAllExited = errors.New("すべてのトンネルが終了しました")

const (
	minBackoff  = time.Second
	maxBackoff  = 60 * time.Second
	stableAfter = 60 * time.Second // これだけ動いたら、次のバックオフを 1s に戻す
	tailSize    = 4096             // Classify に渡す stderr の末尾
)

// Unit は監視する1トンネル。
type Unit struct {
	ID        string
	Env       string // 状態ファイルと status の表示用
	Protected bool
	Reconnect bool // false なら、終了したスロットを再接続せずに failed にする
	Driver    driver.Driver
}

// Clock は時刻と待ち合わせ。テストで差し替える。
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Supervisor はトンネルのプロセスを起動し、停止の指示まで監視する。
// スロット（Driver.Preview の1要素）ごとに、終了したら指数バックオフで張り直す。
type Supervisor struct {
	Out         io.Writer     // 状態の変化と子プロセスの出力を書く先
	StopTimeout time.Duration // SIGTERM から SIGKILL までの猶予。0 なら 5 秒
	Clock       Clock         // nil なら実時間

	out   *syncWriter
	mu    sync.Mutex // slots の差し替えを守る（Run の途中で Slots が呼ばれてもよい）
	slots []*slot
}

type slot struct {
	unit  Unit
	index int // Prepare が返すスライスでの位置
	label string
	mu    sync.Mutex
	st    state.Slot
}

func (s *Supervisor) clock() Clock {
	if s.Clock == nil {
		return realClock{}
	}
	return s.Clock
}

// Run は全ユニットのスロットを起動する。ctx がキャンセルされたら全停止して nil を返す。
// 停止の指示が無いまま全スロットが failed になったら ErrAllExited を返す。
func (s *Supervisor) Run(ctx context.Context, units []Unit) error {
	s.out = &syncWriter{w: s.Out}
	var slots []*slot
	for _, u := range units {
		for i, sp := range u.Driver.Preview() {
			slots = append(slots, &slot{unit: u, index: i, label: "[" + u.ID + "] ", st: state.Slot{
				Tunnel: u.ID, Env: u.Env, Protected: u.Protected, Ports: portStrings(sp.Ports), State: state.Starting,
			}})
		}
	}
	s.mu.Lock()
	s.slots = slots
	s.mu.Unlock()
	s.changed()
	var wg sync.WaitGroup
	for _, sl := range slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runSlot(ctx, sl)
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil
	}
	return ErrAllExited
}

// Slots は各スロットの現在の状態を返す。Run がスロットを作る前は空。
func (s *Supervisor) Slots() []state.Slot {
	s.mu.Lock()
	slots := s.slots
	s.mu.Unlock()
	out := make([]state.Slot, len(slots))
	for i, sl := range slots {
		sl.mu.Lock()
		out[i] = sl.st
		out[i].Ports = append([]string(nil), sl.st.Ports...)
		sl.mu.Unlock()
	}
	return out
}

func (s *Supervisor) update(sl *slot, fn func(*state.Slot)) {
	sl.mu.Lock()
	fn(&sl.st)
	sl.mu.Unlock()
	s.changed()
}

// changed は状態が変わったときに呼ぶ。
func (s *Supervisor) changed() {}

func (s *Supervisor) runSlot(ctx context.Context, sl *slot) {
	clk := s.clock()
	delay := minBackoff
	for {
		var exit driver.Exit
		var ranFor time.Duration
		var lastErr string
		sp, err := s.prepare(ctx, sl)
		if err != nil {
			if ctx.Err() != nil {
				s.stopped(sl)
				return
			}
			s.out.printf("%s準備に失敗しました: %v\n", sl.label, err)
			lastErr = err.Error()
			exit = sl.unit.Driver.Classify(err, nil)
		} else {
			started := clk.Now()
			res := s.runOnce(ctx, sl, sp)
			ranFor = clk.Now().Sub(started)
			switch {
			case res.stopped:
				s.stopped(sl)
				return
			case res.startErr != nil:
				s.fail(sl, "起動できませんでした: "+res.startErr.Error())
				return
			}
			lastErr = exitText(res.err)
			exit = sl.unit.Driver.Classify(res.err, res.tail)
		}
		s.update(sl, func(st *state.Slot) { st.LastError = lastErr })

		if exit.Fatal {
			msg := exit.Message
			if msg == "" {
				msg = lastErr
			}
			s.fail(sl, msg)
			return
		}
		if !sl.unit.Reconnect {
			s.fail(sl, "reconnect: false のため再接続しません")
			return
		}
		if ranFor >= stableAfter {
			delay = minBackoff
		}
		next := clk.Now().Add(delay)
		s.update(sl, func(st *state.Slot) {
			st.State, st.NextRetryAt, st.ChildPID, st.Healthy = state.Backoff, &next, 0, false
		})
		s.out.printf("%s%ds 後に再接続します\n", sl.label, int(delay/time.Second))
		select {
		case <-ctx.Done():
			s.stopped(sl)
			return
		case <-clk.After(delay):
		}
		s.update(sl, func(st *state.Slot) {
			st.State, st.NextRetryAt = state.Starting, nil
			st.Reconnects++
		})
		delay = min(delay*2, maxBackoff)
	}
}

func (s *Supervisor) stopped(sl *slot) {
	s.update(sl, func(st *state.Slot) {
		st.State, st.ChildPID, st.NextRetryAt, st.Healthy = state.Stopped, 0, nil, false
	})
	s.out.printf("%s停止しました\n", sl.label)
}

func (s *Supervisor) fail(sl *slot, reason string) {
	s.update(sl, func(st *state.Slot) {
		st.State, st.LastError, st.ChildPID, st.NextRetryAt, st.Healthy = state.Failed, reason, 0, nil, false
	})
	s.out.printf("%sfailed: %s\n", sl.label, reason)
}

// prepare は Driver.Prepare を呼び、このスロットの位置の Spec を返す。
func (s *Supervisor) prepare(ctx context.Context, sl *slot) (driver.ProcessSpec, error) {
	specs, err := sl.unit.Driver.Prepare(ctx)
	if err != nil {
		return driver.ProcessSpec{}, err
	}
	if sl.index >= len(specs) {
		return driver.ProcessSpec{}, fmt.Errorf("起動するプロセスの数が変わりました（%d 個目がありません）", sl.index+1)
	}
	sp := specs[sl.index]
	s.update(sl, func(st *state.Slot) { st.Ports = portStrings(sp.Ports) })
	return sp, nil
}

type runResult struct {
	err      error  // プロセスの終了理由
	tail     []byte // stderr の末尾
	stopped  bool   // ctx のキャンセルで止めた
	startErr error  // 起動できなかった
}

// runOnce はプロセスを1回起動し、終了するか ctx がキャンセルされるまで待つ。
func (s *Supervisor) runOnce(ctx context.Context, sl *slot, sp driver.ProcessSpec) runResult {
	lw := &lineWriter{out: s.out, label: sl.label}
	cmd := exec.Command(sp.Path, sp.Args...)
	cmd.Env = append(os.Environ(), sp.Env...)
	cmd.Stdout, cmd.Stderr = lw, lw
	cmd.WaitDelay = 2 * time.Second

	child, err := platform.StartChild(cmd)
	if err != nil {
		s.out.printf("%s起動できませんでした: %v\n", sl.label, err)
		return runResult{startErr: err}
	}
	defer child.Close()
	s.out.printf("%s起動しました (pid %d)\n", sl.label, child.Process.Pid)
	s.update(sl, func(st *state.Slot) { st.ChildPID, st.State = child.Process.Pid, state.Starting })

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		lw.flush()
		s.out.printf("%s終了しました: %s\n", sl.label, exitText(err))
		return runResult{err: err, tail: lw.tail()}
	case <-ctx.Done():
		s.stop(child, done)
		lw.flush()
		return runResult{stopped: true}
	}
}

func (s *Supervisor) stop(child *platform.Child, done <-chan error) {
	_ = child.Terminate()
	timeout := s.StopTimeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	select {
	case <-done:
		return
	case <-time.After(timeout):
	}
	_ = child.Kill()
	<-done
}

func portStrings(ls []driver.Listen) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.String()
	}
	return out
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
	_, _ = fmt.Fprintf(s.w, format, args...)
}

// lineWriter は子プロセスの出力を行ごとに区切り、接頭辞を付けて書く。末尾 tailSize バイトを覚えておく。
type lineWriter struct {
	out   *syncWriter
	label string
	mu    sync.Mutex
	buf   []byte
	last  []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.last = append(l.last, p...)
	if len(l.last) > tailSize {
		l.last = l.last[len(l.last)-tailSize:]
	}
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

// tail は出力の末尾 tailSize バイトの写しを返す。
func (l *lineWriter) tail() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]byte(nil), l.last...)
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test -race -count=3 ./internal/supervisor/ && go test ./... && make lint`
Expected: PASS（3回とも）

- [ ] **Step 5: コミットする**

```bash
git add internal/supervisor internal/driver/driver.go
git commit -m "feat(supervisor): 終了したスロットを指数バックオフで張り直し、Fatal と reconnect: false で止める

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: supervisor — ヘルスチェックと状態ファイルの書き込み

**Files:**
- Modify: `internal/supervisor/supervisor.go`
- Test: `internal/supervisor/supervisor_test.go`

**Interfaces:**
- Consumes: Task 6 の `state.File`、Task 7 の `Supervisor`
- Produces:
  - `Supervisor` のフィールド `HealthInterval time.Duration`（0 なら 10 秒）、`HealthTimeout time.Duration`（0 なら 2 秒）、`State *state.File`（nil なら書かない）、`Base state.Run`（pid / started_at / config。Slots は Supervisor が埋める）
  - 状態が変わるたびと、ヘルスチェックのたびに `State.Write`。`Run` が戻る前に `State.Remove`
  - ヘルスチェックの出力: `[id] 接続を確認しました`（starting → up）、`[id] unhealthy: <addr> に接続できません`、`[id] 回復しました`（unhealthy → up）

動き:
- プロセスが動いている間、起動から `min(1s, HealthInterval)` 後に最初の確認、その後は `HealthInterval` ごと
- 全ポートに接続できれば up、1つでもできなければ unhealthy。プロセスは kill しない
- 状態の書き換えは、現在の状態が starting / up / unhealthy のときだけ（終了後に up へ戻さない）
- 状態ファイルを書けなければ、最初の1回だけ警告を出して続ける

- [ ] **Step 1: 失敗するテストを書く**

`internal/supervisor/supervisor_test.go` の import に `path/filepath` を足し、末尾に追加する。

```go
func TestHealthUp(t *testing.T) {
	var out testutil.SyncBuffer
	port := testutil.FreePort(t)
	s := &Supervisor{Out: &out, HealthInterval: 100 * time.Millisecond, HealthTimeout: 200 * time.Millisecond}
	cancel, done := start(t, s, fakeUnit("a", port))
	testutil.Eventually(t, 5*time.Second, func() bool { return firstState(s) == state.Up }, "up にならない")
	if got := s.Slots()[0]; !got.Healthy || got.CheckedAt == nil || got.ChildPID == 0 {
		t.Errorf("up の状態 = %+v", got)
	}
	cancel()
	_ = wait(t, done)
	if !strings.Contains(out.String(), "[a] 接続を確認しました") {
		t.Errorf("出力 = %s", out.String())
	}
}

func TestHealthUnhealthyDoesNotKill(t *testing.T) {
	var out testutil.SyncBuffer
	port, closed := testutil.FreePort(t), testutil.FreePort(t)
	u := fakeUnit("a", port)
	sd := u.Driver.(staticDriver)
	sd.specs[0].Ports = append(sd.specs[0].Ports, driver.Listen{Addr: "127.0.0.1", Port: closed})
	u.Driver = sd
	s := &Supervisor{Out: &out, HealthInterval: 100 * time.Millisecond, HealthTimeout: 200 * time.Millisecond}
	cancel, done := start(t, s, u)
	testutil.Eventually(t, 5*time.Second, func() bool { return firstState(s) == state.Unhealthy }, "unhealthy にならない")
	if !testutil.PortOpen(port) {
		t.Error("unhealthy でもプロセスは kill しない")
	}
	cancel()
	_ = wait(t, done)
	if want := fmt.Sprintf("[a] unhealthy: 127.0.0.1:%d に接続できません", closed); !strings.Contains(out.String(), want) {
		t.Errorf("出力 = %s", out.String())
	}
}

func TestStateFileLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	port := testutil.FreePort(t)
	f := state.New(dir, 4321)
	base := state.Run{PID: 4321, StartedAt: time.Now(), Config: "/x/hashigo.yaml"}
	s := &Supervisor{Out: io.Discard, HealthInterval: 100 * time.Millisecond, State: f, Base: base}
	u := fakeUnit("dev.db", port)
	u.Env, u.Protected = "dev", true
	cancel, done := start(t, s, u)
	alive := func(int, time.Time) bool { return true }
	testutil.Eventually(t, 5*time.Second, func() bool {
		runs, _ := state.List(dir, alive)
		return len(runs) == 1 && len(runs[0].Slots) == 1 && runs[0].Slots[0].State == state.Up
	}, "状態ファイルに up が書かれない")
	runs, _ := state.List(dir, alive)
	got := runs[0]
	if got.PID != 4321 || got.Config != "/x/hashigo.yaml" || got.Slots[0].Env != "dev" || !got.Slots[0].Protected {
		t.Errorf("状態ファイル = %+v", got)
	}
	if want := fmt.Sprintf("127.0.0.1:%d", port); got.Slots[0].Ports[0] != want {
		t.Errorf("ports = %v", got.Slots[0].Ports)
	}
	cancel()
	if err := wait(t, done); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
		t.Error("終了したら状態ファイルを削除する")
	}
}

func TestStateFileWriteErrorWarnsOnce(t *testing.T) {
	var out testutil.SyncBuffer
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// ファイルの下にディレクトリは作れないので、書き込みは必ず失敗する
	s := &Supervisor{Out: &out, HealthInterval: 50 * time.Millisecond, State: state.New(filepath.Join(blocker, "run"), 1)}
	cancel, done := start(t, s, fakeUnit("a", testutil.FreePort(t)))
	testutil.Eventually(t, 5*time.Second, func() bool { return firstState(s) == state.Up }, "up にならない")
	cancel()
	_ = wait(t, done)
	if n := strings.Count(out.String(), "状態ファイルを書けません"); n != 1 {
		t.Errorf("警告は1回だけ: %d 回\n%s", n, out.String())
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/supervisor/ -run 'Health|StateFile'`
Expected: FAIL（`HealthInterval`、`State`、`Base` が未定義）

- [ ] **Step 3: 実装する**

`internal/supervisor/supervisor.go` の import に `net` を足す。`Supervisor` の定義を次に置き換える。

```go
// Supervisor はトンネルのプロセスを起動し、停止の指示まで監視する。
// スロット（Driver.Preview の1要素）ごとに、終了したら指数バックオフで張り直す。
type Supervisor struct {
	Out            io.Writer     // 状態の変化と子プロセスの出力を書く先
	StopTimeout    time.Duration // SIGTERM から SIGKILL までの猶予。0 なら 5 秒
	Clock          Clock         // nil なら実時間
	HealthInterval time.Duration // ヘルスチェックの間隔。0 なら 10 秒
	HealthTimeout  time.Duration // ヘルスチェックの接続のタイムアウト。0 なら 2 秒
	State          *state.File   // nil なら状態ファイルを書かない
	Base           state.Run     // 状態ファイルの pid / started_at / config

	out      *syncWriter
	mu       sync.Mutex // slots の差し替えを守る（Run の途中で Slots が呼ばれてもよい）
	slots    []*slot
	writeMu  sync.Mutex
	warnOnce sync.Once
}
```

`Run` の `wg.Wait()` の直後に、状態ファイルの削除を足す。

```go
	wg.Wait()
	if s.State != nil {
		if err := s.State.Remove(); err != nil {
			s.out.printf("警告: 状態ファイルを削除できません: %v\n", err)
		}
	}
```

`changed` を次に置き換える。

```go
// changed は状態が変わったときに呼び、状態ファイルを書き直す。
// 古いスナップショットが新しいものを上書きしないよう、取得から書き込みまでを1つのロックで囲む。
func (s *Supervisor) changed() {
	if s.State == nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	r := s.Base
	r.Slots = s.Slots()
	if err := s.State.Write(r); err != nil {
		s.warnOnce.Do(func() { s.out.printf("警告: 状態ファイルを書けません: %v\n", err) })
	}
}
```

`runOnce` の `done := make(chan error, 1)` から `select` の前までを次に置き換える。

```go
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	hctx, stopHealth := context.WithCancel(ctx)
	defer stopHealth()
	go s.health(hctx, sl, sp)
```

ファイルの `stop` の後ろに `health` を足す。

```go
// health はプロセスが動いている間、ローカルポートへの接続を定期的に確かめる。失敗しても kill しない。
func (s *Supervisor) health(ctx context.Context, sl *slot, sp driver.ProcessSpec) {
	interval := s.HealthInterval
	if interval == 0 {
		interval = 10 * time.Second
	}
	timeout := s.HealthTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	wait := min(time.Second, interval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = interval
		bad := unreachable(sp.Ports, timeout)
		if ctx.Err() != nil {
			return
		}
		now := s.clock().Now()
		var prev, cur string
		s.update(sl, func(st *state.Slot) {
			prev = st.State
			switch st.State {
			case state.Starting, state.Up, state.Unhealthy:
			default:
				cur = st.State
				return
			}
			st.Healthy, st.CheckedAt = bad == "", &now
			st.State = state.Up
			if bad != "" {
				st.State = state.Unhealthy
			}
			cur = st.State
		})
		switch {
		case cur == state.Up && prev == state.Starting:
			s.out.printf("%s接続を確認しました\n", sl.label)
		case cur == state.Up && prev == state.Unhealthy:
			s.out.printf("%s回復しました\n", sl.label)
		case cur == state.Unhealthy && prev != state.Unhealthy:
			s.out.printf("%sunhealthy: %s に接続できません\n", sl.label, bad)
		}
	}
}

// unreachable は接続できなかった最初のアドレスを返す。全部つながれば "" を返す。
func unreachable(ports []driver.Listen, timeout time.Duration) string {
	for _, l := range ports {
		c, err := net.DialTimeout("tcp", l.DialAddr(), timeout)
		if err != nil {
			return l.String()
		}
		_ = c.Close()
	}
	return ""
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test -race -count=3 ./internal/supervisor/ && go test ./... && make lint`
Expected: PASS（3回とも）

- [ ] **Step 5: コミットする**

```bash
git add internal/supervisor
git commit -m "feat(supervisor): ヘルスチェックと状態ファイルの書き込みを追加する

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: driver/ecs — ECS（SSM）ドライバと、ECS の待ち受けアドレスの検証

**Files:**
- Create: `internal/driver/ecs/ecs.go`
- Test: `internal/driver/ecs/ecs_test.go`
- Modify: `internal/config/validate.go`, `internal/config/validate_test.go`

**Interfaces:**
- Consumes: Task 1 の `platform.Runner`、`platform.ErrText`、Task 5 の `driver.Exit`
- Produces:
  - `type ecs.Options struct { Tunnel, Bin string; Run platform.Runner; Env []string; Profile, Region, Cluster, Service, Container string; Forwards []config.Forward }`
  - `func ecs.New(o Options) *Driver`
  - `(*Driver).Preview()`（target は `ecs:<cluster>_<task-id>_<runtime-id>` のプレースホルダー）、`Prepare(ctx)`、`Classify(err, tail)`
  - `func (d *Driver) Resolvers() [][]string` — cmd で表示する list-tasks / describe-tasks のコマンド（先頭がバイナリ）
  - `func IsAuthExpired(text string) bool`、`func LoginHint(bin, profile string) string` — doctor でも使う
  - `func ResolveProfile(p string, getenv func(string) string) string` — `p`（トンネル / 環境の profile）→ `AWS_PROFILE` → `default`。cli と doctor で使う
  - validate: `type: ecs` の forwards に `127.0.0.1` 以外の待ち受けアドレスがあればエラー（R3）

コマンドの形（仕様 4.2）:

```
<aws> ecs list-tasks --cluster <c> --service-name <s> --desired-status RUNNING --query taskArns[0] --output text --profile <p> [--region <r>]
<aws> ecs describe-tasks --cluster <c> --tasks <task-arn> --query "tasks[0].containers[?name=='<container>'].runtimeId | [0]" --output text --profile <p> [--region <r>]
<aws> ssm start-session --profile <p> [--region <r>] --target ecs:<c>_<task-id>_<runtime-id> \
  --document-name AWS-StartPortForwardingSessionToRemoteHost --parameters host=<宛先>,portNumber=<宛先ポート>,localPortNumber=<ローカルポート>
```

- [ ] **Step 1: 失敗するテストを書く**

`internal/driver/ecs/ecs_test.go`:

```go
package ecs

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
)

type fakeRunner map[string]string

func (f fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	out, ok := f[key]
	if !ok {
		return nil, fmt.Errorf("想定外の呼び出し: %s", key)
	}
	if strings.HasPrefix(out, "ERR:") {
		return nil, errors.New(strings.TrimPrefix(out, "ERR:"))
	}
	return []byte(out), nil
}

const (
	listKey     = "aws ecs list-tasks --cluster stg-cluster --service-name api --desired-status RUNNING --query taskArns[0] --output text --profile stg --region ap-northeast-1"
	describeKey = "aws ecs describe-tasks --cluster stg-cluster --tasks arn:aws:ecs:ap-northeast-1:123:task/stg-cluster/abc123 --query tasks[0].containers[?name=='app'].runtimeId | [0] --output text --profile stg --region ap-northeast-1"
)

func newDriver(run fakeRunner) *Driver {
	return New(Options{
		Tunnel: "stg.db", Bin: "aws", Run: run, Env: []string{"AWS_CONFIG_FILE=/x"},
		Profile: "stg", Region: "ap-northeast-1",
		Cluster: "stg-cluster", Service: "api", Container: "app",
		Forwards: []config.Forward{
			{Bind: "127.0.0.1", LocalPort: 35432, RemoteHost: "stg-db.internal", RemotePort: 5432},
			{Bind: "127.0.0.1", LocalPort: 38080, RemoteHost: "localhost", RemotePort: 8080},
		},
	})
}

func TestPrepare(t *testing.T) {
	d := newDriver(fakeRunner{
		listKey:     "arn:aws:ecs:ap-northeast-1:123:task/stg-cluster/abc123\r\n",
		describeKey: "abc123-4567\r\n",
	})
	specs, err := d.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 2 {
		t.Fatalf("forward ごとに1プロセス: %d", len(specs))
	}
	want := "ssm start-session --profile stg --region ap-northeast-1 --target ecs:stg-cluster_abc123_abc123-4567 " +
		"--document-name AWS-StartPortForwardingSessionToRemoteHost --parameters host=stg-db.internal,portNumber=5432,localPortNumber=35432"
	if got := strings.Join(specs[0].Args, " "); got != want {
		t.Errorf("args =\n%s\nwant\n%s", got, want)
	}
	if specs[1].Ports[0] != (driver.Listen{Addr: "127.0.0.1", Port: 38080}) || specs[1].Path != "aws" || specs[1].Tunnel != "stg.db" {
		t.Errorf("2つ目 = %+v", specs[1])
	}
	if !reflect.DeepEqual(specs[0].Env, []string{"AWS_CONFIG_FILE=/x"}) {
		t.Errorf("env = %v", specs[0].Env)
	}
}

func TestPrepareNoTask(t *testing.T) {
	d := newDriver(fakeRunner{listKey: "None\n"})
	_, err := d.Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "RUNNING のタスクがありません") {
		t.Fatalf("err = %v", err)
	}
	if d.Classify(err, nil).Fatal {
		t.Error("タスクが無いのはデプロイ中とみなし、再接続する")
	}
}

func TestPrepareNoContainer(t *testing.T) {
	d := newDriver(fakeRunner{listKey: "arn:aws:ecs:ap-northeast-1:123:task/stg-cluster/abc123\n", describeKey: "None\n"})
	if _, err := d.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), `コンテナ "app"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestPrepareAuthExpired(t *testing.T) {
	d := newDriver(fakeRunner{listKey: "ERR:Error when retrieving token from sso: Token has expired and refresh failed"})
	_, err := d.Prepare(context.Background())
	ex := d.Classify(err, nil)
	if !ex.Fatal || !strings.Contains(ex.Message, "aws sso login --profile stg") {
		t.Fatalf("Prepare の認証切れは Fatal: %+v (%v)", ex, err)
	}
}

func TestClassify(t *testing.T) {
	d := newDriver(nil)
	for _, msg := range []string{
		"An error occurred (ExpiredTokenException) when calling the StartSession operation",
		"The SSO session associated with this profile has expired or is otherwise invalid.",
		"Token has expired and refresh failed",
		"An error occurred (UnrecognizedClientException): The security token included in the request is expired",
	} {
		if !d.Classify(errors.New("exit status 255"), []byte(msg)).Fatal {
			t.Errorf("認証切れは Fatal: %s", msg)
		}
	}
	if d.Classify(errors.New("exit status 0"), []byte("Exiting session with sessionId: x")).Fatal {
		t.Error("アイドルタイムアウトなどの終了は再接続する")
	}
}

func TestPreviewAndResolvers(t *testing.T) {
	d := New(Options{Tunnel: "x", Bin: "aws.exe", Profile: "default", Cluster: "c", Service: "s", Container: "app",
		Forwards: []config.Forward{{Bind: "127.0.0.1", LocalPort: 1, RemoteHost: "h", RemotePort: 2}}})
	got := strings.Join(d.Preview()[0].Args, " ")
	if !strings.Contains(got, "--target ecs:c_<task-id>_<runtime-id>") || strings.Contains(got, "--region") {
		t.Errorf("Preview = %s", got)
	}
	rs := d.Resolvers()
	if len(rs) != 2 || rs[0][0] != "aws.exe" || rs[0][2] != "list-tasks" || !strings.Contains(strings.Join(rs[1], " "), "--tasks <task-arn>") {
		t.Errorf("Resolvers = %v", rs)
	}
}

func TestResolveProfile(t *testing.T) {
	env := map[string]string{"AWS_PROFILE": "from-env"}
	getenv := func(k string) string { return env[k] }
	if got := ResolveProfile("stg", getenv); got != "stg" {
		t.Errorf("設定の profile が優先: %s", got)
	}
	if got := ResolveProfile("", getenv); got != "from-env" {
		t.Errorf("次に AWS_PROFILE: %s", got)
	}
	if got := ResolveProfile("", func(string) string { return "" }); got != "default" {
		t.Errorf("どちらも無ければ default: %s", got)
	}
}

func TestLoginHintQuotesPath(t *testing.T) {
	got := LoginHint("/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe", "stg")
	if !strings.Contains(got, "'/mnt/c/Program Files/Amazon/AWSCLIV2/aws.exe' sso login --profile stg") {
		t.Errorf("空白を含むパスは quote する: %s", got)
	}
}
```

`internal/config/validate_test.go` の末尾に追加する。

```go
func TestValidateECSBind(t *testing.T) {
	_, ps := loadString(t, `
tunnels:
  task:
    type: ecs
    cluster: c
    service: s
    container: app
    forwards:
      - { local: "0.0.0.0:35432", remote: "db:5432" }
`, "linux")
	wantProblem(t, ps, "type: ecs の forwards には待ち受けアドレスを書けません")
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/driver/ecs/ ./internal/config/`
Expected: FAIL（パッケージが無い、検証が無い）

- [ ] **Step 3: 実装する**

`internal/driver/ecs/ecs.go`:

```go
// Package ecs は SSM Session Manager（ECS Exec）のポートフォワードで、ECS タスク経由の転送を張るドライバ。
// ECS のコンテナには sshd が無いため、ssh とは組み合わせず「ローカル → ECS タスク → 宛先」の1段で完結させる。
package ecs

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/platform"
)

// Options は ECS ドライバの入力。
type Options struct {
	Tunnel    string
	Bin       string          // aws / aws.exe のパス
	Run       platform.Runner // list-tasks などの呼び出しに使う。aws_config の環境変数を設定済みのもの
	Env       []string        // 子プロセス（ssm start-session）に渡す環境変数
	Profile   string          // すべての呼び出しに --profile で付ける
	Region    string          // 空なら --region を付けず、プロファイルの値に任せる
	Cluster   string
	Service   string
	Container string
	Forwards  []config.Forward
}

// Driver は forward ごとに1つの aws ssm start-session を張る（SSM は1セッション1ポート）。
type Driver struct{ o Options }

// New は ECS ドライバを作る。
func New(o Options) *Driver { return &Driver{o: o} }

var errNoTask = errors.New("RUNNING のタスクがありません（デプロイ中の可能性があります）")

func (d *Driver) common() []string {
	a := []string{"--profile", d.o.Profile}
	if d.o.Region != "" {
		a = append(a, "--region", d.o.Region)
	}
	return a
}

func (d *Driver) listTasksArgs() []string {
	return append([]string{
		"ecs", "list-tasks", "--cluster", d.o.Cluster, "--service-name", d.o.Service,
		"--desired-status", "RUNNING", "--query", "taskArns[0]", "--output", "text",
	}, d.common()...)
}

func (d *Driver) describeArgs(task string) []string {
	return append([]string{
		"ecs", "describe-tasks", "--cluster", d.o.Cluster, "--tasks", task,
		"--query", fmt.Sprintf("tasks[0].containers[?name=='%s'].runtimeId | [0]", d.o.Container),
		"--output", "text",
	}, d.common()...)
}

func (d *Driver) specs(target string) []driver.ProcessSpec {
	specs := make([]driver.ProcessSpec, len(d.o.Forwards))
	for i, f := range d.o.Forwards {
		args := append([]string{"ssm", "start-session"}, d.common()...)
		args = append(args,
			"--target", target,
			"--document-name", "AWS-StartPortForwardingSessionToRemoteHost",
			"--parameters", fmt.Sprintf("host=%s,portNumber=%d,localPortNumber=%d", f.RemoteHost, f.RemotePort, f.LocalPort),
		)
		specs[i] = driver.ProcessSpec{
			Tunnel: d.o.Tunnel, Path: d.o.Bin, Args: args, Env: d.o.Env,
			Ports: []driver.Listen{{Addr: f.Bind, Port: f.LocalPort}},
		}
	}
	return specs
}

// Preview は、タスク ID とランタイム ID をプレースホルダーにしたコマンドを返す。外部呼び出しはしない。
func (d *Driver) Preview() []driver.ProcessSpec {
	return d.specs(fmt.Sprintf("ecs:%s_<task-id>_<runtime-id>", d.o.Cluster))
}

// Resolvers は、接続のたびにタスク ID とランタイム ID を解決するコマンドを返す（cmd の表示用）。
func (d *Driver) Resolvers() [][]string {
	return [][]string{
		append([]string{d.o.Bin}, d.listTasksArgs()...),
		append([]string{d.o.Bin}, d.describeArgs("<task-arn>")...),
	}
}

// Prepare は RUNNING のタスクを1つ選び、コンテナのランタイム ID を引いて、forward ごとのコマンドを返す。
func (d *Driver) Prepare(ctx context.Context) ([]driver.ProcessSpec, error) {
	out, err := d.o.Run.Output(ctx, d.o.Bin, d.listTasksArgs()...)
	if err != nil {
		return nil, fmt.Errorf("aws ecs list-tasks に失敗しました: %s", platform.ErrText(err))
	}
	arn := clean(out)
	if arn == "" || arn == "None" {
		return nil, errNoTask
	}
	taskID := arn[strings.LastIndex(arn, "/")+1:]
	out, err = d.o.Run.Output(ctx, d.o.Bin, d.describeArgs(arn)...)
	if err != nil {
		return nil, fmt.Errorf("aws ecs describe-tasks に失敗しました: %s", platform.ErrText(err))
	}
	rid := clean(out)
	if rid == "" || rid == "None" {
		return nil, fmt.Errorf("タスク %s にコンテナ %q がありません", taskID, d.o.Container)
	}
	return d.specs(fmt.Sprintf("ecs:%s_%s_%s", d.o.Cluster, taskID, rid)), nil
}

// clean は aws.exe の出力の CRLF と前後の空白を取り除く。
func clean(b []byte) string {
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\r", ""))
}

var authExpired = []string{
	"ExpiredToken",
	"Token has expired",
	"The SSO session associated with this profile has expired",
	"Error when retrieving token from sso",
	"The security token included in the request is expired",
}

// IsAuthExpired は aws の出力が、SSO トークンの期限切れや認証情報の失効を示しているかを返す。
func IsAuthExpired(text string) bool {
	for _, s := range authExpired {
		if strings.Contains(text, s) {
			return true
		}
	}
	return false
}

// LoginHint は SSO に再ログインする案内を返す。SSO のキャッシュは実行バイナリ側にあるので、同じバイナリで案内する。
func LoginHint(bin, profile string) string {
	return fmt.Sprintf("AWS の認証が切れています。%s sso login --profile %s を実行してから、もう一度 up してください", quote(bin), profile)
}

// ResolveProfile は使う AWS プロファイルを決める。トンネル / 環境の profile → AWS_PROFILE → default の順。
func ResolveProfile(p string, getenv func(string) string) string {
	if p != "" {
		return p
	}
	if p := getenv("AWS_PROFILE"); p != "" {
		return p
	}
	return "default"
}

func quote(s string) string {
	if strings.ContainsAny(s, " \t'\"") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

// Classify は認証切れなら Fatal にする。RUNNING のタスクが無い、アイドルタイムアウトで切れたなどは再接続する。
func (d *Driver) Classify(err error, tail []byte) driver.Exit {
	text := string(tail)
	if err != nil {
		text += "\n" + err.Error()
	}
	if IsAuthExpired(text) {
		return driver.Exit{Fatal: true, Message: LoginHint(d.o.Bin, d.o.Profile)}
	}
	return driver.Exit{}
}
```

`internal/config/validate.go` の `tunnels()` の `case "ecs":` の最後（`for _, x := range []kv{{"ssh", ...` のループの後ろ）に追加する。

```go
			for i, f := range t.Forwards {
				if f.Bind != defaultBind {
					v.add(t.Src.child("forwards", i), "type: ecs の forwards には待ち受けアドレスを書けません（SSM は 127.0.0.1 で待ち受けます）: %q", f.Bind)
				}
			}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./... && make lint`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/driver/ecs internal/config/validate.go internal/config/validate_test.go
git commit -m "feat(driver): SSM で ECS タスク経由のポートフォワードを張る ECS ドライバを追加する

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: driver/relay — relay モード（socat の補助スロット）

**Files:**
- Create: `internal/driver/relay/relay.go`
- Test: `internal/driver/relay/relay_test.go`
- Modify: `docs/superpowers/specs/2026-10-23-hashigo-design.md`（R1 の反映）

**Interfaces:**
- Consumes: Task 5 の `driver.Rebinder`（ssh ドライバ）、Task 4 の `Host.DefaultGateway`（cli から関数で渡す）
- Produces:
  - `type relay.Options struct { Socat string; Gateway func(ctx context.Context) (string, error) }`
  - `func relay.Wrap(inner driver.Rebinder, o Options) *Driver` — `driver.Driver` を満たす
  - 並び: inner の Spec（待ち受けをゲートウェイ IP にしたもの）のあとに、forward ごとの socat の Spec
  - socat の引数: `TCP-LISTEN:<port>,bind=<元の待ち受け>,reuseaddr,fork` と `TCP:<gatewayIP>:<port>`（待ち受けが IPv6 なら `TCP6-LISTEN:<port>,bind=[<addr>],reuseaddr,fork`）
  - Preview ではゲートウェイ IP を `<ゲートウェイIP>` と表示する

- [ ] **Step 1: 失敗するテストを書く**

`internal/driver/relay/relay_test.go`:

```go
package relay

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/driver/ssh"
)

func inner() *ssh.Driver {
	return ssh.New(ssh.Options{
		Tunnel: "dev.db", Bin: "ssh.exe", Route: config.Route{Dest: "ap"}, Keepalive: 30 * time.Second,
		Forwards: []config.Forward{
			{Bind: "127.0.0.1", LocalPort: 15432, RemoteHost: "db", RemotePort: 5432},
			{Bind: "127.0.0.1", LocalPort: 16379, RemoteHost: "redis", RemotePort: 6379},
		},
	})
}

func TestPrepare(t *testing.T) {
	calls := 0
	d := Wrap(inner(), Options{Socat: "/usr/bin/socat", Gateway: func(context.Context) (string, error) {
		calls++
		return "172.29.16.1", nil
	}})
	specs, err := d.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 {
		t.Fatalf("ssh 1つ + forward ごとの socat 2つ: %d", len(specs))
	}
	if got := strings.Join(specs[0].Args, " "); !strings.Contains(got, "-L 172.29.16.1:15432:db:5432 -L 172.29.16.1:16379:redis:6379") {
		t.Errorf("ssh.exe はゲートウェイ IP で待ち受ける: %s", got)
	}
	if specs[0].Ports[0] != (driver.Listen{Addr: "172.29.16.1", Port: 15432}) {
		t.Errorf("ssh のヘルスチェック先 = %+v", specs[0].Ports)
	}
	if got := strings.Join(specs[1].Args, " "); got != "TCP-LISTEN:15432,bind=127.0.0.1,reuseaddr,fork TCP:172.29.16.1:15432" {
		t.Errorf("socat = %s", got)
	}
	if specs[2].Path != "/usr/bin/socat" || specs[2].Tunnel != "dev.db" || specs[2].Ports[0] != (driver.Listen{Addr: "127.0.0.1", Port: 16379}) {
		t.Errorf("2つ目の socat = %+v", specs[2])
	}
	if _, err := d.Prepare(context.Background()); err != nil || calls != 2 {
		t.Errorf("ゲートウェイ IP は Prepare のたびに取り直す: calls=%d", calls)
	}
}

func TestPreview(t *testing.T) {
	d := Wrap(inner(), Options{Socat: "socat", Gateway: func(context.Context) (string, error) {
		t.Fatal("Preview は外部呼び出しをしない")
		return "", nil
	}})
	specs := d.Preview()
	if len(specs) != 3 || !strings.Contains(strings.Join(specs[1].Args, " "), "TCP:<ゲートウェイIP>:15432") {
		t.Errorf("Preview = %+v", specs)
	}
}

func TestIPv6Bind(t *testing.T) {
	in := ssh.New(ssh.Options{Tunnel: "x", Bin: "ssh.exe", Route: config.Route{Dest: "h"},
		Forwards: []config.Forward{{Bind: "::1", LocalPort: 8080, RemoteHost: "web", RemotePort: 80}}})
	specs, _ := Wrap(in, Options{Socat: "socat", Gateway: func(context.Context) (string, error) { return "172.29.16.1", nil }}).Prepare(context.Background())
	if got := specs[1].Args[0]; got != "TCP6-LISTEN:8080,bind=[::1],reuseaddr,fork" {
		t.Errorf("IPv6 の待ち受け = %s", got)
	}
}

func TestGatewayError(t *testing.T) {
	d := Wrap(inner(), Options{Socat: "socat", Gateway: func(context.Context) (string, error) { return "", errors.New("no route") }})
	if _, err := d.Prepare(context.Background()); err == nil || !strings.Contains(err.Error(), "no route") {
		t.Fatalf("err = %v", err)
	}
	if d.Classify(errors.New("x"), nil).Fatal {
		t.Error("Classify は中身のドライバに任せる（ssh は常に再接続）")
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/driver/relay/`
Expected: FAIL（パッケージが無い）

- [ ] **Step 3: 実装する**

`internal/driver/relay/relay.go`:

```go
// Package relay は、NAT モードの WSL で ssh.exe の待ち受けを WSL 側へ中継するドライバ。
// ssh.exe を Windows 側の WSL 用仮想 NIC の IP で待ち受けさせ（0.0.0.0 にはしないので LAN に公開されない）、
// WSL 側の待ち受けアドレスから socat で中継する。
package relay

import (
	"context"
	"fmt"
	"strings"

	"github.com/kukv/hashigo/internal/driver"
)

// Options は relay ドライバの入力。
type Options struct {
	Socat   string                                    // socat のパス
	Gateway func(ctx context.Context) (string, error) // WSL から見た Windows 側の IP。接続のたびに呼ぶ
}

// Driver は中身のドライバの Spec に、forward ごとの socat の補助スロットを足す。
type Driver struct {
	inner driver.Rebinder
	o     Options
}

// Wrap は inner を relay モードにする。
func Wrap(inner driver.Rebinder, o Options) *Driver { return &Driver{inner: inner, o: o} }

const previewGateway = "<ゲートウェイIP>"

// Preview はゲートウェイ IP をプレースホルダーにして返す。外部呼び出しはしない。
func (d *Driver) Preview() []driver.ProcessSpec {
	return d.wrap(d.inner.PreviewAt(previewGateway), previewGateway)
}

// Prepare はゲートウェイ IP を取り直し、ssh の待ち受けをその IP にして、socat を足す。
// この IP は Windows の再起動で変わるため、接続のたびに取り直す。
func (d *Driver) Prepare(ctx context.Context) ([]driver.ProcessSpec, error) {
	gw, err := d.o.Gateway(ctx)
	if err != nil {
		return nil, fmt.Errorf("relay の中継先（WSL から見た Windows の IP）を取得できません: %w", err)
	}
	specs, err := d.inner.PrepareAt(ctx, gw)
	if err != nil {
		return nil, err
	}
	return d.wrap(specs, gw), nil
}

// Classify は中身のドライバに任せる。
func (d *Driver) Classify(err error, tail []byte) driver.Exit { return d.inner.Classify(err, tail) }

// wrap は specs の後ろに、ローカルポートごとの socat を足す。並びは毎回同じになる。
func (d *Driver) wrap(specs []driver.ProcessSpec, gw string) []driver.ProcessSpec {
	orig := d.inner.Preview() // 待ち受けを差し替える前の、WSL 側の待ち受けアドレス
	out := append([]driver.ProcessSpec(nil), specs...)
	for i, sp := range specs {
		for j, p := range sp.Ports {
			local := orig[i].Ports[j]
			out = append(out, driver.ProcessSpec{
				Tunnel: sp.Tunnel,
				Path:   d.o.Socat,
				Args:   []string{listenArg(local), fmt.Sprintf("TCP:%s:%d", gw, p.Port)},
				Ports:  []driver.Listen{local},
			})
		}
	}
	return out
}

func listenArg(l driver.Listen) string {
	if strings.Contains(l.Addr, ":") {
		return fmt.Sprintf("TCP6-LISTEN:%d,bind=[%s],reuseaddr,fork", l.Port, l.Addr)
	}
	return fmt.Sprintf("TCP-LISTEN:%d,bind=%s,reuseaddr,fork", l.Port, l.Addr)
}
```

仕様書（`docs/superpowers/specs/2026-10-23-hashigo-design.md`）を2か所直す。

パッケージ構成の `internal/driver/` の行:

```
internal/driver/        Driver インターフェース、ssh/、ecs/
```

を次にする。

```
internal/driver/        Driver インターフェース、ssh/、ecs/、relay/（relay モードの書き換え）
```

6.4 の relay の1行目:

```
- **relay**: platform 層がドライバの出力を書き換える（`Wrap(specs) → specs + 補助スロット`）
```

を次にする。

```
- **relay**: `internal/driver/relay` がドライバの出力を書き換える（`Wrap(specs) → specs + 補助スロット`）。ゲートウェイ IP の取得など OS に依存する部分は platform 層が受け持つ（depguard で platform から driver を参照できないため）
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./internal/driver/... && make lint`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/driver/relay docs/superpowers/specs/2026-10-23-hashigo-design.md
git commit -m "feat(driver): NAT モードの WSL で ssh.exe の待ち受けを socat で中継する relay ドライバを追加する

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: cli — ドライバの組み立て（ECS、config の切り替え、relay）と `cmd` の表示

**Files:**
- Modify: `internal/cli/build.go`（全体を置き換える）, `internal/cli/cmd.go`, `internal/cli/up.go`
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Consumes: Task 1（`AWSBinary`、`Runner`、`CrossesToWindows`）、Task 3（`SSHConfigFor`、`AWSConfigFor`）、Task 4（`DefaultGateway`）、Task 5（`ssh.Options.ConfigFile`）、Task 7（`supervisor.Unit` の新しいフィールド）、Task 9（`ecs.New`、`Resolvers`）、Task 10（`relay.Wrap`）
- Produces:
  - `type builtUnit struct { ID, Env string; Protected, Reconnect bool; Type, Bin, Config, Profile, Region string; AWSEnv []string; Ports []int; Driver driver.Driver }`
  - `func (a *app) buildUnits(ctx, cfg, tunnels) ([]builtUnit, string, error)` — 2つ目の戻り値は WSL のモード（WSL でなければ ""）
  - `func (b builtUnit) unit() supervisor.Unit`
  - MVP の「安定化フェーズで対応します」のエラー3つを削除する
  - ECS × relay はエラー（R2）。relay で socat が無ければエラー
  - ECS のプロファイルは、トンネル / 環境の `profile` → `AWS_PROFILE` → `default`

`cmd` の ECS の表示:

```
# stg.db  aws=<bin>  aws_config=<path>  profile=<p>
# 環境変数: AWS_CONFIG_FILE=... WSLENV=...          ← 環境変数を渡すときだけ
# <task-id> と <runtime-id> は、接続のたびに次のコマンドで解決します
<list-tasks のコマンド>
<describe-tasks のコマンド>
<forward ごとの ssm start-session>
```

- [ ] **Step 1: 失敗するテストを書く**

`internal/cli/cli_test.go` の `testHost` の `LookPath` に aws を足す。

```go
		LookPath: func(name string) (string, error) {
			switch name {
			case "ssh":
				return "/usr/bin/ssh", nil
			case "aws":
				return "/usr/bin/aws", nil
			}
			return "", exec.ErrNotFound
		},
```

`TestCmdNotYet` を削除し、次を追加する（import に `fmt` を足す）。

```go
// mapRunner は「コマンド 引数...」をキーに出力を返す Runner。
type mapRunner map[string]string

func (m mapRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	out, ok := m[key]
	if !ok {
		return nil, fmt.Errorf("想定外の呼び出し: %s", key)
	}
	return []byte(out), nil
}

// wslHost は NAT モードの WSL として振る舞う Host。
func wslHost(paths map[string]string) *platform.Host {
	h := testHost()
	h.Getenv = func(k string) string {
		if k == "WSL_DISTRO_NAME" {
			return "Ubuntu"
		}
		return ""
	}
	h.LookPath = func(name string) (string, error) {
		if p, ok := paths[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	h.Run = mapRunner{
		"wslinfo --networking-mode":     "nat\n",
		"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
		"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
	}
	return h
}

func runWith(t *testing.T, h *platform.Host, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	d := Deps{Host: h, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut, IsTTY: func() bool { return false }}
	code := Run(context.Background(), d, args)
	return code, out.String(), errOut.String()
}

func TestCmdECS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("パス区切りが異なるため Unix 系でのみ確認する")
	}
	p := writeConfig(t, cmdYAML)
	code, out, errOut := run(t, "cmd", "--config", p, "task")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	want := `# 実行環境: linux
# task  aws=/usr/bin/aws  aws_config=/home/u/.aws/config  profile=default
# <task-id> と <runtime-id> は、接続のたびに次のコマンドで解決します
/usr/bin/aws ecs list-tasks --cluster c --service-name s --desired-status RUNNING --query 'taskArns[0]' --output text --profile default
/usr/bin/aws ecs describe-tasks --cluster c --tasks '<task-arn>' --query 'tasks[0].containers[?name=='\''app'\''].runtimeId | [0]' --output text --profile default
/usr/bin/aws ssm start-session --profile default --target 'ecs:c_<task-id>_<runtime-id>' --document-name AWS-StartPortForwardingSessionToRemoteHost --parameters host=db,portNumber=5432,localPortNumber=35432
`
	if out != want {
		t.Errorf("cmd の出力 =\n%s\nwant\n%s", out, want)
	}
}

func TestCmdSSHConfig(t *testing.T) {
	p := writeConfig(t, "defaults:\n  ssh_config: /home/u/work/ssh_config\ntunnels:\n  t: { host: x, forwards: [\"1:a:1\"] }\n")
	code, out, errOut := run(t, "cmd", "--config", p, "t")
	if code != 0 || !strings.Contains(out, "ssh_config=/home/u/work/ssh_config") || !strings.Contains(out, "-o BatchMode=yes -F /home/u/work/ssh_config -L") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, errOut)
	}
	p = writeConfig(t, "defaults:\n  ssh_config: wsl\ntunnels:\n  t: { host: x, forwards: [\"1:a:1\"] }\n")
	code, _, errOut = run(t, "cmd", "--config", p, "t")
	if code != 1 || !strings.Contains(errOut, "ssh_config: wsl は WSL でだけ使えます") {
		t.Fatalf("WSL でなければ wsl はエラー: code=%d stderr=%s", code, errOut)
	}
}

func TestCmdRelay(t *testing.T) {
	p := writeConfig(t, "tunnels:\n  db: { host: ap, forwards: [\"15432:db:5432\"] }\n")
	h := wslHost(map[string]string{"ssh.exe": "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe", "socat": "/usr/bin/socat"})
	code, out, errOut := runWith(t, h, "cmd", "--config", p, "db")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	for _, want := range []string{
		"# 実行環境: linux (WSL, relay)",
		"ssh_config=/mnt/c/Users/taro/.ssh/config",
		"-L '<ゲートウェイIP>:15432:db:5432'",
		"/usr/bin/socat TCP-LISTEN:15432,bind=127.0.0.1,reuseaddr,fork 'TCP:<ゲートウェイIP>:15432'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("出力に %q が無い:\n%s", want, out)
		}
	}
}

func TestCmdRelayErrors(t *testing.T) {
	p := writeConfig(t, "tunnels:\n  db: { host: ap, forwards: [\"15432:db:5432\"] }\n")
	h := wslHost(map[string]string{"ssh.exe": "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"})
	if code, _, errOut := runWith(t, h, "cmd", "--config", p, "db"); code != 1 || !strings.Contains(errOut, "socat が必要です") {
		t.Errorf("socat が無ければエラー: code=%d %s", code, errOut)
	}

	p = writeConfig(t, cmdYAML)
	h = wslHost(map[string]string{"aws.exe": "/mnt/c/aws.exe", "socat": "/usr/bin/socat"})
	if code, _, errOut := runWith(t, h, "cmd", "--config", p, "task"); code != 1 || !strings.Contains(errOut, "relay モードでは使えません") {
		t.Errorf("ECS × relay はエラー: code=%d %s", code, errOut)
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/cli/`
Expected: FAIL（ECS がまだ「安定化フェーズで対応します」になる、`-F` が付かない、relay がエラーになる）

- [ ] **Step 3: 実装する**

`internal/cli/build.go` を次の内容で置き換える。

```go
package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/driver/ecs"
	"github.com/kukv/hashigo/internal/driver/relay"
	"github.com/kukv/hashigo/internal/driver/ssh"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/supervisor"
)

// builtUnit は1トンネル分のドライバと、表示用の情報。
type builtUnit struct {
	ID        string
	Env       string
	Protected bool
	Reconnect bool
	Type      string   // ssh / ecs
	Bin       string   // 実行するバイナリ
	Config    string   // 実際に読む ssh / aws config のパス（表示用）
	Profile   string   // ECS の AWS プロファイル
	Region    string   // ECS のリージョン（空ならプロファイルの値）
	AWSEnv    []string // ECS の aws に渡す環境変数
	Ports     []int    // ローカルポート
	Driver    driver.Driver
}

func (b builtUnit) unit() supervisor.Unit {
	return supervisor.Unit{ID: b.ID, Env: b.Env, Protected: b.Protected, Reconnect: b.Reconnect, Driver: b.Driver}
}

// buildUnits はトンネルごとにドライバを組み立てる。2つ目の戻り値は WSL のモード（WSL でなければ ""）。
func (a *app) buildUnits(ctx context.Context, cfg *config.Config, tunnels []*config.Tunnel) ([]builtUnit, string, error) {
	mode, err := a.d.Host.ResolveWSLMode(ctx, cfg.WSLMode)
	if err != nil {
		return nil, "", err
	}
	b := &builder{a: a, cfg: cfg, mode: mode}
	units := make([]builtUnit, 0, len(tunnels))
	for _, t := range tunnels {
		eff := cfg.Effective(t)
		u := builtUnit{ID: t.ID(), Env: t.Env, Protected: eff.Protected, Reconnect: eff.Reconnect, Type: t.Type}
		for _, f := range t.Forwards {
			u.Ports = append(u.Ports, f.LocalPort)
		}
		if t.Type == "ecs" {
			err = b.ecs(ctx, &u, t, eff)
		} else {
			err = b.ssh(ctx, &u, t, eff)
		}
		if err != nil {
			return nil, "", err
		}
		units = append(units, u)
	}
	return units, mode, nil
}

type builder struct {
	a     *app
	cfg   *config.Config
	mode  string
	socat string // relay で使う socat のパス（最初に必要になったときに探す）
}

func (b *builder) ssh(ctx context.Context, u *builtUnit, t *config.Tunnel, eff config.Effective) error {
	h := b.a.d.Host
	bin, err := h.SSHBinary(eff.SSH, b.mode)
	if err != nil {
		return fmt.Errorf("%s: %w", t.ID(), err)
	}
	route, err := b.cfg.Route(t)
	if err != nil {
		return err
	}
	arg, display, err := h.SSHConfigFor(ctx, bin, eff.SSHConfig)
	if err != nil {
		if eff.SSHConfig != "auto" {
			return fmt.Errorf("%s: %w", t.ID(), err)
		}
		arg, display = "", "(不明)"
	}
	d := ssh.New(ssh.Options{
		Tunnel: t.ID(), Bin: bin, Route: route, Forwards: t.Forwards, Keepalive: eff.Keepalive, ConfigFile: arg,
	})
	u.Bin, u.Config, u.Driver = bin, display, d
	if b.mode != "relay" || !h.CrossesToWindows(bin) {
		return nil
	}
	if b.socat == "" {
		p, err := h.LookPath("socat")
		if err != nil {
			return errors.New("relay モードには socat が必要です（例: sudo apt install socat）。または .wslconfig で networkingMode=mirrored を有効にしてください")
		}
		b.socat = p
	}
	u.Driver = relay.Wrap(d, relay.Options{Socat: b.socat, Gateway: h.DefaultGateway})
	return nil
}

func (b *builder) ecs(ctx context.Context, u *builtUnit, t *config.Tunnel, eff config.Effective) error {
	h := b.a.d.Host
	if b.mode == "relay" {
		return fmt.Errorf("%s: ECS トンネルは relay モードでは使えません（session-manager-plugin は Windows の 127.0.0.1 でしか待ち受けないため）。.wslconfig で networkingMode=mirrored を有効にしてください", t.ID())
	}
	bin, err := h.AWSBinary(eff.AWS, b.mode)
	if err != nil {
		return fmt.Errorf("%s: %w", t.ID(), err)
	}
	env, display, err := h.AWSConfigFor(ctx, bin, eff.AWSConfig)
	if err != nil {
		if eff.AWSConfig != "auto" {
			return fmt.Errorf("%s: %w", t.ID(), err)
		}
		env, display = nil, "(不明)"
	}
	profile := ecs.ResolveProfile(eff.Profile, h.Getenv)
	u.Bin, u.Config, u.Profile, u.Region, u.AWSEnv = bin, display, profile, eff.Region, env
	u.Driver = ecs.New(ecs.Options{
		Tunnel: t.ID(), Bin: bin, Run: h.Runner(platform.RunOptions{Env: env}), Env: env,
		Profile: profile, Region: eff.Region,
		Cluster: t.Cluster, Service: t.Service, Container: t.Container,
		Forwards: t.Forwards,
	})
	return nil
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

`internal/cli/cmd.go` の `RunE` の `units, err := a.buildUnits(ctx, cfg, tunnels)` から `return nil` の前までを次に置き換える（`strings` は既に import 済み）。

```go
			units, _, err := a.buildUnits(ctx, cfg, tunnels)
			if err != nil {
				return err
			}
			w := c.OutOrStdout()
			_, _ = fmt.Fprintf(w, "# 実行環境: %s\n", a.describeEnv(ctx, cfg))
			for _, u := range units {
				if u.Type == "ecs" {
					_, _ = fmt.Fprintf(w, "# %s  aws=%s  aws_config=%s  profile=%s\n", u.ID, u.Bin, u.Config, u.Profile)
					if len(u.AWSEnv) > 0 {
						_, _ = fmt.Fprintf(w, "# 環境変数: %s\n", strings.Join(u.AWSEnv, " "))
					}
					if r, ok := u.Driver.(interface{ Resolvers() [][]string }); ok {
						_, _ = fmt.Fprintln(w, "# <task-id> と <runtime-id> は、接続のたびに次のコマンドで解決します")
						for _, args := range r.Resolvers() {
							_, _ = fmt.Fprintln(w, shellJoin(args))
						}
					}
				} else {
					_, _ = fmt.Fprintf(w, "# %s  ssh=%s  ssh_config=%s\n", u.ID, u.Bin, u.Config)
				}
				for _, sp := range u.Driver.Preview() {
					_, _ = fmt.Fprintln(w, shellJoin(append([]string{sp.Path}, sp.Args...)))
				}
			}
```

`cmd.go` の `Short` を `"生成される ssh / aws コマンドを表示する（接続はしない）"` にする。

`internal/cli/up.go` の `up` で、`buildUnits` の呼び出しと `supervisor.Unit` の組み立てを直す。

```go
	built, _, err := a.buildUnits(ctx, cfg, tunnels)
	if err != nil {
		return err
	}
```

```go
	units := make([]supervisor.Unit, len(built))
	for i, b := range built {
		units[i] = b.unit()
	}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./... && make lint`
Expected: PASS。`grep -rn 安定化フェーズで対応 internal/` が何も出さない

- [ ] **Step 5: コミットする**

```bash
git add internal/cli
git commit -m "feat(cli): ECS、ssh_config / aws_config の切り替え、relay をドライバの組み立てに入れ、cmd で表示する

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 12: cli — `up` の安定化（状態ファイル、衝突相手、事前認証、後始末、LAN 公開の警告）

**Files:**
- Modify: `internal/cli/up.go`（全体を置き換える）, `internal/testutil/fakebin/fakebin.go`, `internal/cli/cli_test.go`
- Test: `internal/cli/up_test.go`

**Interfaces:**
- Consumes: Task 1（`StateDir`、`Runner`、`ErrText`、`CrossesToWindows`）、Task 2（`StartTime`、`IsAlive`）、Task 4（`WindowsListeners`、`CleanupWindows`）、Task 6（`state.New`、`List`、`FindPort`）、Task 8（`Supervisor.State` / `Base`）、Task 9（`ecs.IsAuthExpired`、`LoginHint`）、Task 11（`buildUnits`、`builtUnit`）
- Produces:
  - `func (a *app) liveRuns() []state.Run` — 動いている up の状態ファイル（status / doctor でも使う）
  - `func dialOK(addr string) bool` — 300ms で TCP 接続できるか（status / doctor でも使う）
  - `func checkPorts(tunnels []*config.Tunnel, runs []state.Run) error` — 使用中なら `<id> の <addr> → <相手>`。相手は `dev.db（pid 1234 の up）` か `hashigo 外のプロセス`
  - `func (a *app) preauth(ctx, built []builtUnit) error` — ECS のプロファイルごとに1回 `sts get-caller-identity --profile <p> [--region <r>]` を端末につないで実行
  - `func (a *app) warnExposed(ctx, w io.Writer, built []builtUnit, delay time.Duration)` — relay で起動して delay 後に、Windows 側の待ち受けが `0.0.0.0` / `::` なら警告
  - `func (a *app) cleanupWindows(w io.Writer, built []builtUnit)` — WSL で .exe を使ったトンネルのポートについて `CleanupWindows`
  - テスト用: `testHost(t testing.TB)`（`XDG_STATE_HOME` を一時ディレクトリにする）、`wslHost(t, paths)`、`funcRunner`
  - fakebin: `--parameters` の `localPortNumber=<port>` でも待ち受ける（偽 aws）

`up` の流れ（仕様 5.3）: 検証 → protected の確認 → ポートの確認（衝突相手は状態ファイルから）→ ECS の事前認証 → 状態ファイルを用意して起動 → （relay なら 5 秒後に LAN 公開を確認）→ 停止後に Windows 側の後始末。

- [ ] **Step 1: テストの準備を直し、失敗するテストを書く**

`internal/cli/cli_test.go` の `testHost` を、`t` を受け取って状態ファイルの場所を一時ディレクトリにする形に置き換える。

```go
// testHost は WSL ではない Linux として振る舞う Host。ファイルの存在確認だけは本物を使う。
// 状態ファイルはテストごとの一時ディレクトリに書く。
func testHost(t testing.TB) *platform.Host {
	stateHome := t.TempDir()
	return &platform.Host{
		GOOS: "linux",
		Getenv: func(k string) string {
			if k == "XDG_STATE_HOME" {
				return stateHome
			}
			return ""
		},
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		LookPath: func(name string) (string, error) {
			switch name {
			case "ssh":
				return "/usr/bin/ssh", nil
			case "aws":
				return "/usr/bin/aws", nil
			}
			return "", exec.ErrNotFound
		},
		Stat:    os.Stat,
		HomeDir: func() (string, error) { return "/home/u", nil },
		Run:     platform.ExecRunner{},
	}
}
```

`run` の `testHost()` を `testHost(t)` にする。`wslHost` を `t` を受け取る形に置き換える。

```go
// wslHost は NAT モードの WSL として振る舞う Host。
func wslHost(t testing.TB, paths map[string]string) *platform.Host {
	h := testHost(t)
	base := h.Getenv
	h.Getenv = func(k string) string {
		if k == "WSL_DISTRO_NAME" {
			return "Ubuntu"
		}
		return base(k)
	}
	h.LookPath = func(name string) (string, error) {
		if p, ok := paths[name]; ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	h.Run = mapRunner{
		"wslinfo --networking-mode":     "nat\n",
		"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
		"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
	}
	return h
}
```

`TestCmdRelay` と `TestCmdRelayErrors` の `wslHost(map...)` を `wslHost(t, map...)` にする。`mapRunner` の後ろに `funcRunner` を足す。

```go
// funcRunner は呼び出しを関数で受ける Runner。
type funcRunner func(name string, args []string) ([]byte, error)

func (f funcRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	return f(name, args)
}
```

`internal/cli/up_test.go` の `startUp` を、Host を受け取る `startUpHost` に分ける。

```go
func startUp(t *testing.T, stdin string, tty bool, args ...string) (context.CancelFunc, <-chan int, *testutil.SyncBuffer) {
	t.Helper()
	return startUpHost(t, testHost(t), stdin, tty, args...)
}

func startUpHost(t *testing.T, h *platform.Host, stdin string, tty bool, args ...string) (context.CancelFunc, <-chan int, *testutil.SyncBuffer) {
	t.Helper()
	t.Setenv("HASHIGO_FAKE", "1")
	var stderr testutil.SyncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := Deps{Host: h, Stdin: strings.NewReader(stdin), Stdout: io.Discard, Stderr: &stderr, IsTTY: func() bool { return tty }}
	done := make(chan int, 1)
	go func() { done <- Run(ctx, d, args) }()
	return cancel, done, &stderr
}
```

`TestUpPortBusy` の最後に追加する。

```go
	if !strings.Contains(stderr.String(), "hashigo 外のプロセス") {
		t.Errorf("状態ファイルに無ければ hashigo 外のプロセス: %s", stderr.String())
	}
```

`up_test.go` の import に `bytes`、`errors`、`sync`、`github.com/kukv/hashigo/internal/platform`、`github.com/kukv/hashigo/internal/state` を足し、末尾に追加する。

```go
func TestUpWritesStateFile(t *testing.T) {
	port := testutil.FreePort(t)
	p := writeConfig(t, upYAML(port, false))
	h := testHost(t)
	dir, _ := h.StateDir()
	cancel, done, stderr := startUpHost(t, h, "", false, "up", "--config", p, "prod")
	testutil.WaitPort(t, port, 5*time.Second)
	testutil.Eventually(t, 5*time.Second, func() bool {
		runs, _ := state.List(dir, platform.IsAlive)
		return len(runs) == 1 && runs[0].PID == os.Getpid() && len(runs[0].Slots) == 1 && runs[0].Slots[0].Tunnel == "prod.db"
	}, "状態ファイルが書かれない")
	cancel()
	if code := waitCode(t, done); code != 0 {
		t.Fatalf("code=%d %s", code, stderr.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("終了後に状態ファイルが残っている: %v", entries)
	}
}

func TestUpPortOwner(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	h := testHost(t)
	dir, _ := h.StateDir()
	st, err := platform.StartTime(os.Getpid())
	if err != nil {
		t.Skip("この OS ではプロセスの起動時刻を取れない")
	}
	run := state.Run{PID: os.Getpid(), StartedAt: st, Slots: []state.Slot{{Tunnel: "other.db", Ports: []string{fmt.Sprintf("127.0.0.1:%d", port)}}}}
	if err := state.New(dir, os.Getpid()).Write(run); err != nil {
		t.Fatal(err)
	}
	_, done, stderr := startUpHost(t, h, "", false, "up", "--config", writeConfig(t, upYAML(port, false)), "prod")
	if code := waitCode(t, done); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if want := fmt.Sprintf("other.db（pid %d の up）", os.Getpid()); !strings.Contains(stderr.String(), want) {
		t.Errorf("衝突相手を状態ファイルから特定する: %s", stderr.String())
	}
}

func ecsUpYAML(p1, p2 int) string {
	return fmt.Sprintf(`
defaults:
  aws: %q
tunnels:
  a: { type: ecs, cluster: c, service: s, container: app, profile: p1, forwards: ["%d:db:5432"] }
  b: { type: ecs, cluster: c, service: s, container: app, profile: p1, forwards: ["%d:db:5432"] }
groups:
  both: [a, b]
`, os.Args[0], p1, p2)
}

// ecsHost は aws の呼び出しを偽の応答にする Host。sts の呼び出しを「Interactive 引数...」の形で記録する。
func ecsHost(t *testing.T, stsErr error) (*platform.Host, func() []string) {
	h := testHost(t)
	var mu sync.Mutex
	var sts []string
	h.RunWith = func(o platform.RunOptions) platform.Runner {
		return funcRunner(func(_ string, args []string) ([]byte, error) {
			switch args[1] {
			case "get-caller-identity":
				mu.Lock()
				sts = append(sts, fmt.Sprintf("%v %s", o.Interactive, strings.Join(args, " ")))
				mu.Unlock()
				return []byte("{}"), stsErr
			case "list-tasks":
				return []byte("arn:aws:ecs:ap-northeast-1:1:task/c/t1\n"), nil
			case "describe-tasks":
				return []byte("t1-1\n"), nil
			}
			return nil, fmt.Errorf("想定外の呼び出し: %v", args)
		})
	}
	return h, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), sts...)
	}
}

func TestUpECS(t *testing.T) {
	p1, p2 := testutil.FreePort(t), testutil.FreePort(t)
	h, sts := ecsHost(t, nil)
	cancel, done, stderr := startUpHost(t, h, "", false, "up", "--config", writeConfig(t, ecsUpYAML(p1, p2)), "both")
	testutil.WaitPort(t, p1, 5*time.Second)
	testutil.WaitPort(t, p2, 5*time.Second)
	cancel()
	if code := waitCode(t, done); code != 0 {
		t.Fatalf("code=%d %s", code, stderr.String())
	}
	got := sts()
	if len(got) != 1 || got[0] != "true sts get-caller-identity --profile p1" {
		t.Errorf("同じプロファイルの事前認証は1回だけ、端末につないで行う: %v", got)
	}
	if !strings.Contains(stderr.String(), "AWS の認証を確認しています（profile: p1）") {
		t.Errorf("stderr = %s", stderr.String())
	}
}

func TestUpECSPreauthFails(t *testing.T) {
	h, _ := ecsHost(t, errors.New("MFA の入力に失敗しました"))
	_, done, stderr := startUpHost(t, h, "", false, "up", "--config",
		writeConfig(t, ecsUpYAML(testutil.FreePort(t), testutil.FreePort(t))), "both")
	if code := waitCode(t, done); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if s := stderr.String(); !strings.Contains(s, "profile p1 の認証を確認できませんでした") || strings.Contains(s, "起動しました") {
		t.Errorf("事前認証に失敗したら何も起動しない: %s", s)
	}
}

func TestWarnExposed(t *testing.T) {
	h := testHost(t)
	h.Run = funcRunner(func(string, []string) ([]byte, error) {
		return []byte("0.0.0.0 15432\r\n172.29.16.1 16379\r\n"), nil
	})
	a := &app{d: Deps{Host: h}}
	var out bytes.Buffer
	a.warnExposed(context.Background(), &out, []builtUnit{{Ports: []int{15432, 16379}}}, 0)
	if s := out.String(); !strings.Contains(s, "ポート 15432 が 0.0.0.0 で待ち受けています") || strings.Contains(s, "16379") {
		t.Errorf("出力 = %s", s)
	}
}

func TestCleanupWindowsAfterUp(t *testing.T) {
	var killed []string
	h := wslHost(t, nil)
	h.Run = funcRunner(func(name string, args []string) ([]byte, error) {
		switch name {
		case "powershell.exe":
			return []byte("100\tssh.exe\tssh.exe -L 127.0.0.1:15432:db:5432 -- x\r\n200\tssh.exe\tssh.exe -L 127.0.0.1:9:db:5432 -- y\r\n"), nil
		case "taskkill.exe":
			killed = append(killed, args[2])
			return nil, nil
		}
		return nil, fmt.Errorf("想定外: %s", name)
	})
	a := &app{d: Deps{Host: h}}
	var out bytes.Buffer
	a.cleanupWindows(&out, []builtUnit{
		{Bin: "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe", Ports: []int{15432}},
		{Bin: "/usr/bin/ssh", Ports: []int{9}},
	})
	if len(killed) != 1 || killed[0] != "100" {
		t.Errorf(".exe を使ったトンネルのポートだけを片付ける: %v", killed)
	}
	if !strings.Contains(out.String(), "Windows 側に残っていた ssh.exe (pid 100) を終了しました") {
		t.Errorf("出力 = %s", out.String())
	}

	plain := testHost(t)
	plain.Run = funcRunner(func(name string, _ []string) ([]byte, error) {
		t.Errorf("WSL でなければ何も実行しない: %s", name)
		return nil, nil
	})
	(&app{d: Deps{Host: plain}}).cleanupWindows(&out, []builtUnit{{Bin: "/usr/bin/ssh", Ports: []int{1}}})
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/cli/`
Expected: FAIL（状態ファイルが書かれない、衝突相手が出ない、ECS が起動しない、`warnExposed` / `cleanupWindows` が未定義）

- [ ] **Step 3: 実装する**

`internal/testutil/fakebin/fakebin.go` の `Main` を次に置き換え、`localPortNumber` を足す。

```go
// Main は偽 ssh / 偽 aws として動き、戻らない。
// ssh の -L と、aws ssm start-session の --parameters にある localPortNumber で待ち受ける。
func Main() {
	if os.Getenv("FAKE_IGNORE_TERM") == "1" {
		signal.Ignore(syscall.SIGTERM)
	}
	if s := os.Getenv("FAKE_STDERR"); s != "" {
		_, _ = fmt.Fprintln(os.Stderr, s)
	}
	args := os.Args[1:]
	for i := 0; i < len(args)-1; i++ {
		var addr string
		switch args[i] {
		case "-L":
			addr = listenAddr(args[i+1])
		case "--parameters":
			p := localPortNumber(args[i+1])
			if p == "" {
				continue
			}
			addr = net.JoinHostPort("127.0.0.1", p)
		default:
			continue
		}
		i++
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "bind に失敗しました:", err)
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

// localPortNumber は host=db,portNumber=5432,localPortNumber=15432 から 15432 を取り出す。
func localPortNumber(params string) string {
	for _, kv := range strings.Split(params, ",") {
		if v, ok := strings.CutPrefix(kv, "localPortNumber="); ok {
			return v
		}
	}
	return ""
}
```

パッケージのコメントの1行目を `// Package fakebin はテストバイナリ自身を偽の ssh / aws として振る舞わせる。` にする。

`internal/cli/up.go` を次の内容で置き換える。

```go
package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/driver/ecs"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/state"
	"github.com/kukv/hashigo/internal/supervisor"
)

func (a *app) upCmd() *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "up <対象...>",
		Short: "トンネルを起動する（Ctrl-C で全停止）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()
			return a.up(ctx, c, args, yes)
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "protected 環境の確認を省略する")
	return c
}

func (a *app) up(ctx context.Context, c *cobra.Command, args []string, yes bool) error {
	cfg, path, err := a.load(ctx)
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
	built, mode, err := a.buildUnits(ctx, cfg, tunnels)
	if err != nil {
		return err
	}
	if err := a.confirmProtected(cfg, tunnels, yes); err != nil {
		return err
	}
	if err := checkPorts(tunnels, a.liveRuns()); err != nil {
		return err
	}
	if err := a.preauth(ctx, built); err != nil {
		return err
	}
	units := make([]supervisor.Unit, len(built))
	for i, b := range built {
		units[i] = b.unit()
	}
	w := c.ErrOrStderr()
	sup := &supervisor.Supervisor{Out: w}
	a.attachState(w, sup, path)
	if mode == "relay" {
		go a.warnExposed(ctx, w, built, 5*time.Second)
	}
	runErr := sup.Run(ctx, units)
	a.cleanupWindows(w, built)
	return runErr
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
	_, _ = fmt.Fprintf(a.d.Stderr, "%s は protected です。起動しますか? [y/N] ", names)
	line, _ := bufio.NewReader(a.d.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return errors.New("起動を取りやめました")
}

// liveRuns は動いている up の状態ファイルを読む。残骸は削除される。読めなければ空として扱う。
func (a *app) liveRuns() []state.Run {
	dir, err := a.d.Host.StateDir()
	if err != nil {
		return nil
	}
	runs, err := state.List(dir, platform.IsAlive)
	if err != nil {
		return nil
	}
	return runs
}

// dialOK は addr に TCP 接続できるかを返す。
func dialOK(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// checkPorts はローカルポートに接続を試し、使用中のものがあればエラーにする。
// 使っている相手は状態ファイルから特定し、特定できなければ hashigo 外のプロセスとする。
func checkPorts(tunnels []*config.Tunnel, runs []state.Run) error {
	var busy []string
	for _, t := range tunnels {
		for _, f := range t.Forwards {
			l := driver.Listen{Addr: f.Bind, Port: f.LocalPort}
			if !dialOK(l.DialAddr()) {
				continue
			}
			owner := "hashigo 外のプロセス"
			if r, s, ok := state.FindPort(runs, l.String()); ok {
				owner = fmt.Sprintf("%s（pid %d の up）", s.Tunnel, r.PID)
			}
			busy = append(busy, fmt.Sprintf("%s の %s → %s", t.ID(), l.String(), owner))
		}
	}
	if len(busy) == 0 {
		return nil
	}
	return fmt.Errorf("ローカルポートが使用中のため起動しません:\n  %s", strings.Join(busy, "\n  "))
}

// preauth は、ECS トンネルが使うプロファイルごとに一度だけ sts get-caller-identity を実行する。
// 端末をつなぐので、AssumeRole の MFA コードをここで入力できる。失敗したら起動しない。
func (a *app) preauth(ctx context.Context, built []builtUnit) error {
	seen := map[string]bool{}
	for _, u := range built {
		if u.Type != "ecs" {
			continue
		}
		key := strings.Join(append([]string{u.Bin, u.Profile, u.Region}, u.AWSEnv...), "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		_, _ = fmt.Fprintf(a.d.Stderr, "AWS の認証を確認しています（profile: %s）\n", u.Profile)
		args := []string{"sts", "get-caller-identity", "--profile", u.Profile}
		if u.Region != "" {
			args = append(args, "--region", u.Region)
		}
		r := a.d.Host.Runner(platform.RunOptions{Env: u.AWSEnv, Interactive: true})
		if _, err := r.Output(ctx, u.Bin, args...); err != nil {
			msg := platform.ErrText(err)
			if ecs.IsAuthExpired(msg) {
				return errors.New(ecs.LoginHint(u.Bin, u.Profile))
			}
			return fmt.Errorf("profile %s の認証を確認できませんでした: %s", u.Profile, msg)
		}
	}
	return nil
}

// attachState は状態ファイルを用意する。started_at には、残骸の判定と同じ StartTime の値を書く（R6）。
func (a *app) attachState(w io.Writer, sup *supervisor.Supervisor, configPath string) {
	dir, err := a.d.Host.StateDir()
	if err != nil {
		_, _ = fmt.Fprintf(w, "警告: 状態ファイルを使えません: %v\n", err)
		return
	}
	pid := os.Getpid()
	started, err := platform.StartTime(pid)
	if err != nil {
		started = time.Now()
	}
	if abs, err := filepath.Abs(configPath); err == nil {
		configPath = abs
	}
	sup.State = state.New(dir, pid)
	sup.Base = state.Run{PID: pid, StartedAt: started, Config: configPath}
}

// warnExposed は relay で起動したあと、Windows 側の待ち受けが LAN に公開されていないかを確かめる。
func (a *app) warnExposed(ctx context.Context, w io.Writer, built []builtUnit, delay time.Duration) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}
	var ports []int
	for _, u := range built {
		ports = append(ports, u.Ports...)
	}
	ls, err := a.d.Host.WindowsListeners(ctx, ports)
	if err != nil {
		_, _ = fmt.Fprintf(w, "警告: Windows 側の待ち受けアドレスを確認できません: %v\n", err)
		return
	}
	for _, l := range ls {
		if l.Exposed() {
			_, _ = fmt.Fprintf(w, "警告: Windows 側でポート %d が %s で待ち受けています（LAN に公開されています）\n", l.Port, l.Addr)
		}
	}
}

// cleanupWindows は、WSL から Windows のバイナリを使ったトンネルについて、
// Windows 側に残ったプロセスを片付ける（仕様 6.5）。
func (a *app) cleanupWindows(w io.Writer, built []builtUnit) {
	var ports []int
	for _, u := range built {
		if a.d.Host.CrossesToWindows(u.Bin) {
			ports = append(ports, u.Ports...)
		}
	}
	if len(ports) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	killed, err := a.d.Host.CleanupWindows(ctx, ports)
	for _, p := range killed {
		_, _ = fmt.Fprintf(w, "Windows 側に残っていた %s (pid %d) を終了しました\n", p.Name, p.PID)
	}
	if err != nil {
		_, _ = fmt.Fprintf(w, "警告: Windows 側の後始末に失敗しました: %v\n", err)
	}
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test -race ./... && make lint`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/cli internal/testutil/fakebin/fakebin.go
git commit -m "feat(cli): up に状態ファイル、衝突相手の特定、ECS の事前認証、Windows 側の後始末、LAN 公開の警告を足す

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 13: cli — `status`

**Files:**
- Create: `internal/cli/status.go`
- Modify: `internal/cli/root.go`, `cmd/hashigo/main.go`
- Test: `internal/cli/status_test.go`

**Interfaces:**
- Consumes: Task 6（`state.Run` / `Slot`）、Task 7（`driver.Listen.DialAddr`）、Task 12（`liveRuns`、`dialOK`）
- Produces:
  - `Deps.StdoutTTY func() bool`（nil なら TTY ではない）
  - `var errQuiet` — 出力済みで、終了コード 1 だけを返すときのエラー（`printError` しない）
  - `type statusRow struct { Env string; Protected bool; Tunnel, Ports, State string; Reconnects int; LastError string }`
  - `func statusRows(cfg *config.Config, tunnels []*config.Tunnel, runs []state.Run, dial func(string) bool, now time.Time) []statusRow`
  - `func renderTable(w io.Writer, header []string, rows [][]cell)`、`type cell struct { Text, Color string }`、`func dispWidth(s string) int`、`func envColor(name string, protected bool) string`

表示（仕様 7.2）:

| 状態ファイル | TCP 接続 | 状態の列 |
|---|---|---|
| あり | 全ポート成功 | `up` |
| あり | 失敗 | `backoff (次回 Ns 後)` / `failed: <最終エラー>` / `starting` / `停止`（stopped）/ `unhealthy`（up・unhealthy） |
| なし | どれか成功 | `hashigo 外のプロセスが使用中` |
| なし | 全部失敗 | `停止` |

- 列: `環境 トンネル ポート 状態 再接続 最終エラー`。ルート環境の環境名は `-`
- 環境名の色は名前から決める（10色）。protected の環境は赤の太字（`1;31`）。`StdoutTTY` が false、または `NO_COLOR` が設定されていれば色なし
- 終了コード: 全行が `up` なら 0、それ以外は 1（エラーの文言は出さない）

- [ ] **Step 1: 失敗するテストを書く**

`internal/cli/status_test.go`:

```go
package cli

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/state"
)

const statusYAML = `
tunnels:
  web: { host: app1, forwards: ["18080:localhost:80"] }
environments:
  dev:
    tunnels:
      db: { host: ap, forwards: ["15432:db:5432", "16379:redis:6379"] }
  prod:
    protected: true
    tunnels:
      db: { host: ap, forwards: ["25432:db:5432"] }
`

func loadCfg(t *testing.T, yaml string) *config.Config {
	t.Helper()
	cfg, err := config.Load(writeConfig(t, yaml), "linux")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestStatusRows(t *testing.T) {
	cfg := loadCfg(t, statusYAML)
	now := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	next := now.Add(11500 * time.Millisecond)
	runs := []state.Run{{PID: 1, Slots: []state.Slot{
		{Tunnel: "dev.db", Env: "dev", Ports: []string{"127.0.0.1:15432", "127.0.0.1:16379"}, State: state.Up},
		{Tunnel: "prod.db", Env: "prod", Ports: []string{"127.0.0.1:25432"}, State: state.Backoff, NextRetryAt: &next, Reconnects: 3, LastError: "exit status 255"},
	}}}
	open := map[string]bool{"127.0.0.1:15432": true, "127.0.0.1:16379": true, "127.0.0.1:18080": true}
	rows := statusRows(cfg, cfg.Tunnels(), runs, func(a string) bool { return open[a] }, now)
	got := map[string]statusRow{}
	for _, r := range rows {
		got[r.Env+"/"+r.Tunnel] = r
	}
	if r := got["dev/db"]; r.State != "up" || r.Ports != "127.0.0.1:15432,127.0.0.1:16379" {
		t.Errorf("dev.db = %+v", r)
	}
	if r := got["prod/db"]; r.State != "backoff (次回 12s 後)" || r.Reconnects != 3 || !r.Protected || r.LastError != "exit status 255" {
		t.Errorf("prod.db = %+v", r)
	}
	if r := got["/web"]; r.State != "hashigo 外のプロセスが使用中" {
		t.Errorf("web = %+v", r)
	}

	failed := []state.Run{{PID: 1, Slots: []state.Slot{{Tunnel: "dev.db", Ports: []string{"127.0.0.1:15432"}, State: state.Failed, LastError: "AWS の認証が切れています"}}}}
	none := func(string) bool { return false }
	rows = statusRows(cfg, cfg.Tunnels(), failed, none, now)
	for _, r := range rows {
		switch r.Env + "/" + r.Tunnel {
		case "dev/db":
			if r.State != "failed: AWS の認証が切れています" {
				t.Errorf("failed = %+v", r)
			}
		case "/web":
			if r.State != "停止" {
				t.Errorf("状態ファイルも接続も無ければ停止: %+v", r)
			}
		}
	}

	up := []state.Run{{PID: 1, Slots: []state.Slot{{Tunnel: "dev.db", Ports: []string{"127.0.0.1:15432"}, State: state.Up}}}}
	if r := statusRows(cfg, []*config.Tunnel{cfg.Envs["dev"].Tunnels["db"]}, up, none, now)[0]; r.State != "unhealthy" {
		t.Errorf("状態ファイルが up でも接続できなければ unhealthy: %+v", r)
	}
}

func TestRenderTable(t *testing.T) {
	var out bytes.Buffer
	renderTable(&out, []string{"環境", "トンネル"}, [][]cell{
		{{Text: "prod", Color: "1;31"}, {Text: "db"}},
		{{Text: "-"}, {Text: "web"}},
	})
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if lines[0] != "環境  トンネル" || lines[1] != "\x1b[1;31mprod\x1b[0m  db" || lines[2] != "-     web" {
		t.Errorf("表 =\n%q", lines)
	}
	if dispWidth("環境ab") != 6 {
		t.Errorf("全角は幅 2: %d", dispWidth("環境ab"))
	}
	if envColor("dev", false) != envColor("dev", false) || envColor("x", true) != "1;31" {
		t.Error("色は名前で決まり、protected は赤の太字")
	}
}

func TestStatusCmd(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := ln.Addr().(*net.TCPAddr).Port
	p := writeConfig(t, fmt.Sprintf("environments:\n  dev:\n    tunnels:\n      db: { host: ap, forwards: [\"%d:db:5432\"] }\n", port))
	h := testHost(t)

	code, out, _ := runWith(t, h, "status", "--config", p)
	if code != 1 || !strings.Contains(out, "hashigo 外のプロセスが使用中") || !strings.Contains(out, "環境") {
		t.Fatalf("状態ファイルが無ければ hashigo 外: code=%d\n%s", code, out)
	}

	st, err := platform.StartTime(os.Getpid())
	if err != nil {
		t.Skip("この OS ではプロセスの起動時刻を取れない")
	}
	dir, _ := h.StateDir()
	run := state.Run{PID: os.Getpid(), StartedAt: st, Slots: []state.Slot{{Tunnel: "dev.db", Env: "dev", Ports: []string{fmt.Sprintf("127.0.0.1:%d", port)}, State: state.Up}}}
	if err := state.New(dir, os.Getpid()).Write(run); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runWith(t, h, "status", "--config", p, "dev")
	if code != 0 || !strings.Contains(out, "up") || errOut != "" {
		t.Fatalf("全部 up なら 0: code=%d\n%s\n%s", code, out, errOut)
	}
	if strings.Contains(out, "\x1b[") {
		t.Error("TTY でなければ色を付けない")
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/cli/ -run 'Status|Render'`
Expected: FAIL（`statusRows` などが未定義）

- [ ] **Step 3: 実装する**

`internal/cli/root.go` の `Deps` に `StdoutTTY` を足す。

```go
// Deps はコマンドが使う外部とのつなぎ目。テストで差し替える。
type Deps struct {
	Host      *platform.Host
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	IsTTY     func() bool // stdin が端末か（protected の確認に使う）
	StdoutTTY func() bool // stdout が色を出せる端末か。nil なら端末ではない
}
```

`Run` のエラー処理と `AddCommand` を次にする。

```go
	root.AddCommand(newVersionCmd(), a.validateCmd(), a.cmdCmd(), a.upCmd(), a.statusCmd())

	if err := root.ExecuteContext(ctx); err != nil {
		if !errors.Is(err, errQuiet) {
			printError(d.Stderr, err)
		}
		return 1
	}
	return 0
}

// errQuiet は、結果を出力済みで、終了コード 1 だけを返すときのエラー。
var errQuiet = errors.New("終了コード 1")
```

`internal/cli/status.go`:

```go
package cli

import (
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/state"
)

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status [対象...]",
		Short: "トンネルの状態を表示する（全部 up なら終了コード 0）",
		RunE: func(c *cobra.Command, args []string) error {
			cfg, _, err := a.load(c.Context())
			if err != nil {
				return err
			}
			tunnels := cfg.Tunnels()
			if len(args) > 0 {
				if tunnels, err = cfg.Select(args); err != nil {
					return err
				}
			}
			rows := statusRows(cfg, tunnels, a.liveRuns(), dialOK, time.Now())
			color := a.d.StdoutTTY != nil && a.d.StdoutTTY() && a.d.Host.Getenv("NO_COLOR") == ""
			cells := make([][]cell, len(rows))
			allUp := true
			for i, r := range rows {
				env := r.Env
				if env == "" {
					env = "-"
				}
				envCell := cell{Text: env}
				if color && r.Env != "" {
					envCell.Color = envColor(r.Env, r.Protected)
				}
				cells[i] = []cell{envCell, {Text: r.Tunnel}, {Text: r.Ports}, {Text: r.State}, {Text: strconv.Itoa(r.Reconnects)}, {Text: r.LastError}}
				allUp = allUp && r.State == "up"
			}
			renderTable(c.OutOrStdout(), []string{"環境", "トンネル", "ポート", "状態", "再接続", "最終エラー"}, cells)
			if !allUp {
				return errQuiet
			}
			return nil
		},
	}
}

// statusRow は status の1行。
type statusRow struct {
	Env        string
	Protected  bool
	Tunnel     string
	Ports      string
	State      string
	Reconnects int
	LastError  string
}

// statusRows は、状態ファイルがあるトンネルはスロットごと、無いトンネルはトンネルごとに1行を作る（R8）。
func statusRows(cfg *config.Config, tunnels []*config.Tunnel, runs []state.Run, dial func(string) bool, now time.Time) []statusRow {
	var rows []statusRow
	for _, t := range tunnels {
		e := cfg.Envs[t.Env]
		base := statusRow{Env: t.Env, Protected: e != nil && e.Protected, Tunnel: t.Name}
		slots := slotsOf(runs, t.ID())
		if len(slots) == 0 {
			var ports []string
			open := false
			for _, f := range t.Forwards {
				l := driver.Listen{Addr: f.Bind, Port: f.LocalPort}
				ports = append(ports, l.String())
				open = open || dial(l.DialAddr())
			}
			r := base
			r.Ports, r.State = strings.Join(ports, ","), "停止"
			if open {
				r.State = "hashigo 外のプロセスが使用中"
			}
			rows = append(rows, r)
			continue
		}
		for _, s := range slots {
			r := base
			r.Ports, r.State, r.Reconnects, r.LastError = strings.Join(s.Ports, ","), slotState(s, dial, now), s.Reconnects, s.LastError
			rows = append(rows, r)
		}
	}
	return rows
}

// slotsOf は id のトンネルのスロットを返す。ローカルポートは一意なので、見つかる up は1つだけ。
func slotsOf(runs []state.Run, id string) []state.Slot {
	for _, r := range runs {
		var out []state.Slot
		for _, s := range r.Slots {
			if s.Tunnel == id {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func slotState(s state.Slot, dial func(string) bool, now time.Time) string {
	ok := len(s.Ports) > 0
	for _, p := range s.Ports {
		ok = ok && dial(dialAddrOf(p))
	}
	if ok {
		return "up"
	}
	switch s.State {
	case state.Backoff:
		if s.NextRetryAt == nil {
			return "backoff"
		}
		secs := max(0, int(math.Ceil(s.NextRetryAt.Sub(now).Seconds())))
		return fmt.Sprintf("backoff (次回 %ds 後)", secs)
	case state.Failed:
		return "failed: " + s.LastError
	case state.Starting:
		return "starting"
	case state.Stopped:
		return "停止"
	}
	return "unhealthy"
}

// dialAddrOf は状態ファイルのポート（127.0.0.1:15432 の形）を、接続の確認に使うアドレスにする。
func dialAddrOf(p string) string {
	host, port, err := net.SplitHostPort(p)
	if err != nil {
		return p
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return p
	}
	return driver.Listen{Addr: host, Port: n}.DialAddr()
}

// cell は表の1マス。Color は ANSI の SGR（"1;31" など）。空なら色なし。
type cell struct {
	Text  string
	Color string
}

// renderTable は列を表示幅でそろえて書く。最後の列は詰めない。
func renderTable(w io.Writer, header []string, rows [][]cell) {
	all := make([][]cell, 0, len(rows)+1)
	hc := make([]cell, len(header))
	for i, h := range header {
		hc[i] = cell{Text: h}
	}
	all = append(all, hc)
	all = append(all, rows...)
	widths := make([]int, len(header))
	for _, r := range all {
		for i, c := range r {
			widths[i] = max(widths[i], dispWidth(c.Text))
		}
	}
	for _, r := range all {
		var b strings.Builder
		for i, c := range r {
			text := c.Text
			if c.Color != "" {
				text = "\x1b[" + c.Color + "m" + text + "\x1b[0m"
			}
			b.WriteString(text)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-dispWidth(c.Text)+2))
			}
		}
		_, _ = fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// dispWidth は端末での表示幅を返す。全角の文字は 2 と数える。
func dispWidth(s string) int {
	n := 0
	for _, r := range s {
		if isWide(r) {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func isWide(r rune) bool {
	return (r >= 0x1100 && r <= 0x115f) || (r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe30 && r <= 0xfe4f) || (r >= 0xff00 && r <= 0xff60) ||
		(r >= 0xffe0 && r <= 0xffe6) || r >= 0x20000
}

var envPalette = []string{"32", "33", "34", "35", "36", "92", "93", "94", "95", "96"}

// envColor は環境名から色を決める。protected の環境は赤の太字にする。
func envColor(name string, protected bool) string {
	if protected {
		return "1;31"
	}
	sum := 0
	for _, b := range []byte(name) {
		sum = (sum*31 + int(b)) % 1000003
	}
	return envPalette[sum%len(envPalette)]
}
```

`cmd/hashigo/main.go` の `Deps` に `StdoutTTY` を足す（Windows では VT 処理を有効にできたときだけ色を出す）。

```go
	d := cli.Deps{
		Host:   platform.Detect(),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		IsTTY:  func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		StdoutTTY: func() bool {
			return term.IsTerminal(int(os.Stdout.Fd())) && platform.EnableVT(os.Stdout)
		},
	}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./... && make lint`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/cli cmd/hashigo/main.go
git commit -m "feat(cli): 状態ファイルと TCP 接続でトンネルの状態を表示する status を追加する

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 14: doctor — 枠組みと確認項目（実行環境、ssh、aws、socat、ポート、config、ECS × relay）と `doctor` コマンド

**Files:**
- Create: `internal/doctor/doctor.go`, `internal/doctor/checks.go`, `internal/cli/doctor.go`
- Test: `internal/doctor/doctor_test.go`, `internal/cli/doctor_test.go`
- Modify: `internal/cli/root.go`, `.golangci.yml`

**Interfaces:**
- Consumes: Task 1（`AWSBinary`、`Runner`、`ErrText`、`CrossesToWindows`）、Task 3（`SSHConfigFor`、`AWSConfigFor`）、Task 6（`state.FindPort`）、Task 7（`driver.Listen`）、Task 12（`liveRuns`、`dialOK`）、Task 13（`errQuiet`）
- Produces:
  - `type Level int`（`NA`、`OK`、`Warn`、`Fail`）、`type Result struct { Level Level; Title string; Lines []string }`
  - `type Env struct { Host *platform.Host; Config *config.Config; Tunnels []*config.Tunnel; Runs []state.Run; Dial func(addr string) bool }`
  - `func Run(ctx context.Context, e Env) []Result`、`func Print(w io.Writer, rs []Result)`、`func HasFail(rs []Result) bool`
  - この Task の `Run` は仕様 9 の項目 1, 2, 3, 5, 6, 7, 11 を返す。4, 8, 9, 10 は Task 15 で差し込む
  - 表示: `✓ <項目>` / `⚠ <項目>` / `✗ <項目>`、その下に4字下げで詳細。該当しない項目は `- <項目>: 対象外`
  - `hashigo doctor [対象...]` — 対象を省略したら全トンネル。✗ が1つでもあれば終了コード 1

項目のタイトルと判定:

| # | タイトル | 対象外になる条件 | ✗ / ⚠ |
|---|---|---|---|
| 1 | 実行環境 | なし（WSL でなければ「WSL: 対象外」と書く） | wsl.mode が解決できない ✗、Windows ホームが取れない ⚠ |
| 2 | ssh | ssh トンネルが無い | バイナリが無い・`-V` が失敗 ✗ |
| 3 | aws と session-manager-plugin | ECS トンネルが無い | バイナリが無い・`--version` が失敗・plugin が無い ✗ |
| 5 | socat（relay モード） | relay でない | socat が無い ✗ |
| 6 | ローカルポートの衝突 | なし | hashigo 外のプロセスが使用中 ⚠（hashigo の up が使っているのは ✓ で相手を表示） |
| 7 | ssh / aws の config | トンネルが無い | 参照先を決められない ✗ |
| 11 | ECS × relay | ECS トンネルが無い、または relay でない | 両方あれば ✗（R2） |

- [ ] **Step 1: 失敗するテストを書く**

`internal/doctor/doctor_test.go`:

```go
package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/state"
)

// fakeRunner は「コマンド 引数...」をキーに出力を返す。値が ERR: で始まればその文言のエラーにする。
type fakeRunner map[string]string

func (f fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	key := strings.Join(append([]string{name}, args...), " ")
	out, ok := f[key]
	if !ok {
		return nil, fmt.Errorf("想定外の呼び出し: %s", key)
	}
	if msg, isErr := strings.CutPrefix(out, "ERR:"); isErr {
		return nil, errors.New(msg)
	}
	return []byte(out), nil
}

func newHost(env, files, paths map[string]string, run fakeRunner) *platform.Host {
	return &platform.Host{
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

func loadConfig(t *testing.T, yaml string) *config.Config {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hashigo.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(p, "linux")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func runDoctor(h *platform.Host, cfg *config.Config, runs []state.Run, dial func(string) bool) []Result {
	return Run(context.Background(), Env{Host: h, Config: cfg, Tunnels: cfg.Tunnels(), Runs: runs, Dial: dial})
}

func find(t *testing.T, rs []Result, title string) Result {
	t.Helper()
	for _, r := range rs {
		if r.Title == title {
			return r
		}
	}
	t.Fatalf("項目 %q が無い", title)
	return Result{}
}

func hasLine(r Result, sub string) bool {
	for _, l := range r.Lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func closed(string) bool { return false }

const sshYAML = "tunnels:\n  db: { host: ap, forwards: [\"15432:db:5432\"] }\n"

const wslRunBase = "wslinfo --networking-mode"

func TestPrint(t *testing.T) {
	var out bytes.Buffer
	rs := []Result{
		{Level: OK, Title: "a", Lines: []string{"x"}},
		{Level: Warn, Title: "b"},
		{Level: Fail, Title: "c"},
		{Level: NA, Title: "d"},
	}
	Print(&out, rs)
	want := "✓ a\n    x\n⚠ b\n✗ c\n- d: 対象外\n"
	if out.String() != want {
		t.Errorf("Print =\n%s\nwant\n%s", out.String(), want)
	}
	if !HasFail(rs) || HasFail(rs[:2]) {
		t.Error("HasFail は ✗ があるときだけ true")
	}
}

func TestLinuxSSHOnly(t *testing.T) {
	h := newHost(nil, nil, map[string]string{"ssh": "/usr/bin/ssh"}, fakeRunner{
		"/usr/bin/ssh -V": "OpenSSH_9.6p1, OpenSSL 3.0.13\n",
	})
	rs := runDoctor(h, loadConfig(t, sshYAML), nil, closed)
	if r := find(t, rs, "実行環境"); r.Level != OK || !hasLine(r, "WSL: 対象外") {
		t.Errorf("実行環境 = %+v", r)
	}
	if r := find(t, rs, "ssh"); r.Level != OK || !hasLine(r, "/usr/bin/ssh: OpenSSH_9.6p1") {
		t.Errorf("ssh = %+v", r)
	}
	for _, title := range []string{"aws と session-manager-plugin", "socat（relay モード）", "ECS × relay"} {
		if r := find(t, rs, title); r.Level != NA {
			t.Errorf("%s は対象外: %+v", title, r)
		}
	}
	if r := find(t, rs, "ローカルポートの衝突"); r.Level != OK || !hasLine(r, "衝突なし") {
		t.Errorf("ポート = %+v", r)
	}
	if r := find(t, rs, "ssh / aws の config"); r.Level != OK || !hasLine(r, "ssh (/usr/bin/ssh, ssh_config: auto)") {
		t.Errorf("config = %+v", r)
	}
	if HasFail(rs) {
		t.Errorf("✗ は無い: %+v", rs)
	}
}

func TestWSLRelayWithECS(t *testing.T) {
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"},
		map[string]string{"/mnt/c/Users/taro/.wslconfig": "[wsl2]\r\nnetworkingMode=nat\r\n"},
		map[string]string{"aws.exe": "/mnt/c/aws.exe"},
		fakeRunner{
			wslRunBase:                      "nat\n",
			"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
			"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
			"/mnt/c/aws.exe --version":      "aws-cli/2.17.0 Python/3.11 Windows/10\r\n",
		})
	cfg := loadConfig(t, "tunnels:\n  task: { type: ecs, cluster: c, service: s, container: app, forwards: [\"35432:db:5432\"] }\n")
	rs := runDoctor(h, cfg, nil, closed)
	if r := find(t, rs, "実行環境"); !hasLine(r, "hashigo のモード relay") || !hasLine(r, "networkingMode=nat") {
		t.Errorf("実行環境 = %+v", r)
	}
	if r := find(t, rs, "aws と session-manager-plugin"); r.Level != Fail || !hasLine(r, "aws-cli/2.17.0") || !hasLine(r, "session-manager-plugin が見つかりません") {
		t.Errorf("aws = %+v", r)
	}
	if r := find(t, rs, "socat（relay モード）"); r.Level != Fail {
		t.Errorf("socat = %+v", r)
	}
	if r := find(t, rs, "ECS × relay"); r.Level != Fail || !hasLine(r, "mirrored") {
		t.Errorf("ECS × relay = %+v", r)
	}
	if r := find(t, rs, "ssh"); r.Level != NA {
		t.Errorf("ssh トンネルが無ければ対象外: %+v", r)
	}
}

func TestPortsOwner(t *testing.T) {
	h := newHost(nil, nil, map[string]string{"ssh": "/usr/bin/ssh"}, fakeRunner{"/usr/bin/ssh -V": "OpenSSH_9\n"})
	cfg := loadConfig(t, "tunnels:\n  a: { host: x, forwards: [\"15432:db:5432\"] }\n  b: { host: y, forwards: [\"16379:r:6379\"] }\n")
	runs := []state.Run{{PID: 7, Slots: []state.Slot{{Tunnel: "dev.db", Ports: []string{"127.0.0.1:15432"}}}}}
	r := find(t, runDoctor(h, cfg, runs, func(string) bool { return true }), "ローカルポートの衝突")
	if r.Level != Warn || !hasLine(r, "dev.db（pid 7 の up）が使用中") || !hasLine(r, "127.0.0.1:16379（b）: hashigo 外のプロセスが使用中") {
		t.Errorf("ポート = %+v", r)
	}
}
```

`internal/cli/doctor_test.go`:

```go
package cli

import (
	"strings"
	"testing"
)

func TestDoctorCmd(t *testing.T) {
	h := testHost(t)
	h.Run = mapRunner{"/usr/bin/ssh -V": "OpenSSH_9.6p1\n"}
	p := writeConfig(t, "tunnels:\n  db: { host: ap, forwards: [\"1:db:5432\"] }\n")
	code, out, errOut := runWith(t, h, "doctor", "--config", p)
	if code != 0 || !strings.Contains(out, "✓ ssh") || !strings.Contains(out, "- socat（relay モード）: 対象外") {
		t.Fatalf("code=%d\n%s\n%s", code, out, errOut)
	}

	h.LookPath = func(string) (string, error) { return "", errNotFound }
	code, out, _ = runWith(t, h, "doctor", "--config", p, "db")
	if code != 1 || !strings.Contains(out, "✗ ssh") {
		t.Fatalf("✗ があれば終了コード 1: code=%d\n%s", code, out)
	}
}
```

`errNotFound` は同じファイルに `var errNotFound = exec.ErrNotFound` として置き、import に `os/exec` を足す。

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/doctor/ ./internal/cli/`
Expected: FAIL（パッケージと `doctor` コマンドが無い）

- [ ] **Step 3: 実装する**

`internal/doctor/doctor.go`:

```go
// Package doctor は、実行環境と設定が hashigo の前提を満たしているかを確かめる（仕様 9 の doctor）。
package doctor

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/kukv/hashigo/internal/config"
	"github.com/kukv/hashigo/internal/platform"
	"github.com/kukv/hashigo/internal/state"
)

// Level は確認の結果。
type Level int

// 結果の種類。NA は「対象外」。
const (
	NA Level = iota
	OK
	Warn
	Fail
)

// Result は確認項目1つの結果。
type Result struct {
	Level Level
	Title string
	Lines []string
}

// line は詳細を1行足し、l が今の結果より悪ければ結果を l にする。
func (r *Result) line(l Level, format string, args ...any) {
	r.Lines = append(r.Lines, fmt.Sprintf(format, args...))
	if l > r.Level {
		r.Level = l
	}
}

func ok(title string) Result { return Result{Level: OK, Title: title} }

func na(title string, lines ...string) Result { return Result{Level: NA, Title: title, Lines: lines} }

// Env は確認に使う入力。
type Env struct {
	Host    *platform.Host
	Config  *config.Config
	Tunnels []*config.Tunnel // 確認の対象
	Runs    []state.Run      // 動いている up
	Dial    func(addr string) bool
}

type checker struct {
	ctx     context.Context
	e       Env
	mode    string // WSL のモード（WSL でなければ ""）
	modeErr error
}

// Run は確認項目を仕様の番号順に実行する。
func Run(ctx context.Context, e Env) []Result {
	c := &checker{ctx: ctx, e: e}
	c.mode, c.modeErr = e.Host.ResolveWSLMode(ctx, e.Config.WSLMode)
	return []Result{
		c.platform(),    // 1
		c.ssh(),         // 2
		c.aws(),         // 3
		c.socat(),       // 5
		c.ports(),       // 6
		c.configPaths(), // 7
		c.ecsRelay(),    // 11
	}
}

var marks = map[Level]string{OK: "✓", Warn: "⚠", Fail: "✗"}

// Print は結果を1項目ずつ表示する。詳細は4字下げで続ける。
func Print(w io.Writer, rs []Result) {
	for _, r := range rs {
		if r.Level == NA {
			_, _ = fmt.Fprintf(w, "- %s: 対象外\n", r.Title)
		} else {
			_, _ = fmt.Fprintf(w, "%s %s\n", marks[r.Level], r.Title)
		}
		for _, l := range r.Lines {
			_, _ = fmt.Fprintf(w, "    %s\n", l)
		}
	}
}

// HasFail は ✗ が1つでもあるかを返す。
func HasFail(rs []Result) bool {
	for _, r := range rs {
		if r.Level == Fail {
			return true
		}
	}
	return false
}

func (c *checker) tunnels(typ string) []*config.Tunnel {
	var out []*config.Tunnel
	for _, t := range c.e.Tunnels {
		if t.Type == typ {
			out = append(out, t)
		}
	}
	return out
}

// version は bin arg を実行し、出力の1行目を返す。ssh -V は stderr に出すので stderr も取り込む。
func (c *checker) version(bin, arg string) (Level, string) {
	out, err := c.e.Host.Runner(platform.RunOptions{Combined: true}).Output(c.ctx, bin, arg)
	if err != nil {
		return Fail, fmt.Sprintf("%s: %s を実行できません: %s", bin, arg, platform.ErrText(err))
	}
	return OK, fmt.Sprintf("%s: %s", bin, firstLine(out))
}

func firstLine(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r", "")
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
```

`internal/doctor/checks.go`:

```go
package doctor

import (
	"errors"
	"path"
	"strings"

	"github.com/kukv/hashigo/internal/driver"
	"github.com/kukv/hashigo/internal/state"
)

// 1. OS と WSL の判定、ネットワークモード、.wslconfig の内容
func (c *checker) platform() Result {
	h := c.e.Host
	r := ok("実行環境")
	r.line(OK, "OS: %s", h.GOOS)
	if !h.IsWSL() {
		r.line(OK, "WSL: 対象外")
		return r
	}
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	r.line(OK, "WSL: ネットワークモード %s、hashigo のモード %s", h.NetworkMode(c.ctx), c.mode)
	home, err := h.WindowsHome(c.ctx)
	if err != nil {
		r.line(Warn, "%v", err)
		return r
	}
	b, err := h.ReadFile(path.Join(home, ".wslconfig"))
	if err != nil {
		r.line(OK, ".wslconfig: なし")
		return r
	}
	r.line(OK, ".wslconfig:")
	for _, l := range strings.Split(strings.TrimSpace(strings.ReplaceAll(string(b), "\r", "")), "\n") {
		r.line(OK, "  %s", l)
	}
	return r
}

// 2. ssh バイナリの場所と -V
func (c *checker) ssh() Result {
	const title = "ssh"
	ts := c.tunnels("ssh")
	if len(ts) == 0 {
		return na(title)
	}
	r := ok(title)
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	seen := map[string]bool{}
	for _, t := range ts {
		bin, err := c.e.Host.SSHBinary(c.e.Config.Effective(t).SSH, c.mode)
		if err != nil {
			r.line(Fail, "%s: %v", t.ID(), err)
			continue
		}
		if seen[bin] {
			continue
		}
		seen[bin] = true
		l, s := c.version(bin, "-V")
		r.line(l, "%s", s)
	}
	return r
}

const windowsPlugin = "/mnt/c/Program Files/Amazon/SessionManagerPlugin/bin/session-manager-plugin.exe"

// 3. aws バイナリの場所とバージョン、session-manager-plugin の有無
func (c *checker) aws() Result {
	const title = "aws と session-manager-plugin"
	ts := c.tunnels("ecs")
	if len(ts) == 0 {
		return na(title)
	}
	r := ok(title)
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	seen := map[string]bool{}
	for _, t := range ts {
		bin, err := c.e.Host.AWSBinary(c.e.Config.Effective(t).AWS, c.mode)
		if err != nil {
			r.line(Fail, "%s: %v", t.ID(), err)
			continue
		}
		if seen[bin] {
			continue
		}
		seen[bin] = true
		l, s := c.version(bin, "--version")
		r.line(l, "%s", s)
		if p, err := c.plugin(bin); err != nil {
			r.line(Fail, "session-manager-plugin が見つかりません（%s 用）。AWS のドキュメントに従って入れてください", bin)
		} else {
			r.line(OK, "session-manager-plugin: %s", p)
		}
	}
	return r
}

// plugin は bin と同じ側の session-manager-plugin を探す。
func (c *checker) plugin(bin string) (string, error) {
	h := c.e.Host
	if !h.CrossesToWindows(bin) {
		return h.LookPath("session-manager-plugin")
	}
	if p, err := h.LookPath("session-manager-plugin.exe"); err == nil {
		return p, nil
	}
	if _, err := h.Stat(windowsPlugin); err == nil {
		return windowsPlugin, nil
	}
	return "", errors.New("session-manager-plugin.exe が見つかりません")
}

// 5. socat の有無（relay モードの場合）
func (c *checker) socat() Result {
	const title = "socat（relay モード）"
	if c.mode != "relay" {
		return na(title)
	}
	r := ok(title)
	if p, err := c.e.Host.LookPath("socat"); err != nil {
		r.line(Fail, "socat が見つかりません。relay モードに必要です（例: sudo apt install socat）")
	} else {
		r.line(OK, "%s", p)
	}
	return r
}

// 6. ローカルポートの衝突（TCP 接続と状態ファイルで確認）
func (c *checker) ports() Result {
	r := ok("ローカルポートの衝突")
	for _, t := range c.e.Tunnels {
		for _, f := range t.Forwards {
			l := driver.Listen{Addr: f.Bind, Port: f.LocalPort}
			if !c.e.Dial(l.DialAddr()) {
				continue
			}
			if run, s, found := state.FindPort(c.e.Runs, l.String()); found {
				r.line(OK, "%s: %s（pid %d の up）が使用中", l, s.Tunnel, run.PID)
			} else {
				r.line(Warn, "%s（%s）: hashigo 外のプロセスが使用中", l, t.ID())
			}
		}
	}
	if len(r.Lines) == 0 {
		r.line(OK, "衝突なし")
	}
	return r
}

// 7. 実際に読む ssh / aws config のパス
func (c *checker) configPaths() Result {
	const title = "ssh / aws の config"
	if len(c.e.Tunnels) == 0 {
		return na(title)
	}
	r := ok(title)
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	h := c.e.Host
	seen := map[string]bool{}
	for _, t := range c.e.Tunnels {
		eff := c.e.Config.Effective(t)
		if t.Type == "ecs" {
			bin, err := h.AWSBinary(eff.AWS, c.mode)
			if err != nil || seen["aws\x00"+bin+"\x00"+eff.AWSConfig] {
				continue // バイナリが無いことは 3 で報告する
			}
			seen["aws\x00"+bin+"\x00"+eff.AWSConfig] = true
			if _, display, err := h.AWSConfigFor(c.ctx, bin, eff.AWSConfig); err != nil {
				r.line(Fail, "aws (%s, aws_config: %s): %v", bin, eff.AWSConfig, err)
			} else {
				r.line(OK, "aws (%s, aws_config: %s): %s", bin, eff.AWSConfig, display)
			}
			continue
		}
		bin, err := h.SSHBinary(eff.SSH, c.mode)
		if err != nil || seen["ssh\x00"+bin+"\x00"+eff.SSHConfig] {
			continue // バイナリが無いことは 2 で報告する
		}
		seen["ssh\x00"+bin+"\x00"+eff.SSHConfig] = true
		if _, display, err := h.SSHConfigFor(c.ctx, bin, eff.SSHConfig); err != nil {
			r.line(Fail, "ssh (%s, ssh_config: %s): %v", bin, eff.SSHConfig, err)
		} else {
			r.line(OK, "ssh (%s, ssh_config: %s): %s", bin, eff.SSHConfig, display)
		}
	}
	return r
}

// 11. ECS × relay の組み合わせ（R2: session-manager-plugin は 127.0.0.1 で待ち受けるため使えない）
func (c *checker) ecsRelay() Result {
	const title = "ECS × relay"
	if len(c.tunnels("ecs")) == 0 || c.mode != "relay" {
		return na(title)
	}
	r := ok(title)
	r.line(Fail, "ECS トンネルは relay モードでは使えません（session-manager-plugin は Windows の 127.0.0.1 でしか待ち受けないため）。.wslconfig で networkingMode=mirrored を有効にしてください")
	return r
}
```

`internal/cli/doctor.go`:

```go
package cli

import (
	"github.com/spf13/cobra"

	"github.com/kukv/hashigo/internal/doctor"
)

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor [対象...]",
		Short: "実行環境と設定の前提条件を確かめる（✗ があれば終了コード 1）",
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			cfg, _, err := a.load(ctx)
			if err != nil {
				return err
			}
			tunnels := cfg.Tunnels()
			if len(args) > 0 {
				if tunnels, err = cfg.Select(args); err != nil {
					return err
				}
			}
			rs := doctor.Run(ctx, doctor.Env{Host: a.d.Host, Config: cfg, Tunnels: tunnels, Runs: a.liveRuns(), Dial: dialOK})
			doctor.Print(c.OutOrStdout(), rs)
			if doctor.HasFail(rs) {
				return errQuiet
			}
			return nil
		},
	}
}
```

`internal/cli/root.go` の `AddCommand` に `a.doctorCmd()` を足す。

```go
	root.AddCommand(newVersionCmd(), a.validateCmd(), a.cmdCmd(), a.upCmd(), a.statusCmd(), a.doctorCmd())
```

`.golangci.yml` の depguard の `rules` に追加する（`testutil` の前）。

```yaml
        # doctor は確認だけを行い、プロセスの起動や CLI を知らない。
        doctor-layer:
          files:
            - "**/internal/doctor/**"
          deny:
            - pkg: github.com/kukv/hashigo/internal/supervisor
              desc: doctor はプロセスを起動しない
            - pkg: github.com/kukv/hashigo/internal/cli
              desc: doctor は CLI を知らない
```

`platform-layer`、`state-layer`、`config-layer`、`driver-layer`、`supervisor-layer`、`testutil` の `deny` に、それぞれ次を足す（`state-layer` には `desc: state は確認項目を知らない` のように層の名前に合わせる）。

```yaml
            - pkg: github.com/kukv/hashigo/internal/doctor
              desc: platform は確認項目を知らない
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./... && make lint`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/doctor internal/cli .golangci.yml
git commit -m "feat(doctor): 実行環境・バイナリ・ポート・config・ECS × relay を確かめる doctor を追加する

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 15: doctor — AWS の認証、IdentityFile、hosts、relay の LAN 公開

**Files:**
- Create: `internal/doctor/checks_more.go`
- Modify: `internal/doctor/doctor.go`
- Test: `internal/doctor/checks_more_test.go`

**Interfaces:**
- Consumes: Task 3（`SSHConfigFor`、`AWSConfigFor`、`ToWSLPath`）、Task 4（`HostsFiles`、`HostsHas`、`WindowsListeners`）、Task 9（`ecs.ResolveProfile`、`IsAuthExpired`、`LoginHint`）、Task 14 の枠組み
- Produces: `Run` が仕様 9 の 1〜11 をすべて番号順に返す

| # | タイトル | 対象外になる条件 | 判定 |
|---|---|---|---|
| 4 | AWS の認証 | ECS トンネルが無い | プロファイルごとに非対話で `sts get-caller-identity --profile <p> [--region <r>]`。成功 ✓、出力に `MFA` があれば ⚠「要認証（MFA）。up のときに入力します」、認証切れは ✗（`LoginHint`）、それ以外の失敗は ✗ |
| 8 | 鍵ファイル（IdentityFile） | ssh トンネルが無い | 接続先ごとに `ssh -G [-F <config>] [-J <jumps>] -- <dest>` の `identityfile` を、実行バイナリ側のパスに直して確認。1つも無ければ ⚠。`-G` が失敗したら ⚠ |
| 9 | hosts の設定漏れ | `hostname` を書いたトンネルが無い | `HostsFiles` の各ファイルに `127.0.0.1 <hostname>` があるか。無い側を ⚠ で表示 |
| 10 | LAN への公開（relay） | relay でない。relay で起動中のトンネルが無い | 状態ファイルのポートについて Windows 側の待ち受けを確認。`0.0.0.0` / `::` なら ⚠ |

IdentityFile のパスの直し方: `~` で始まれば実行バイナリ側のホーム（WSL で .exe なら Windows ホーム、それ以外は `HomeDir`）に置き換える。WSL で .exe のとき、`C:\...` の形は `wslpath -u` で WSL のパスにする。

- [ ] **Step 1: 失敗するテストを書く**

`internal/doctor/checks_more_test.go`:

```go
package doctor

import (
	"runtime"
	"testing"

	"github.com/kukv/hashigo/internal/state"
)

const ecsYAML = "tunnels:\n  task: { type: ecs, cluster: c, service: s, container: app, profile: stg, forwards: [\"35432:db:5432\"] }\n"

func TestAllElevenItems(t *testing.T) {
	h := newHost(nil, nil, map[string]string{"ssh": "/usr/bin/ssh"}, fakeRunner{
		"/usr/bin/ssh -V":         "OpenSSH_9\n",
		"/usr/bin/ssh -G -- ap":   "user u\nidentityfile ~/.ssh/id_ed25519\n",
	})
	rs := runDoctor(h, loadConfig(t, sshYAML), nil, closed)
	want := []string{"実行環境", "ssh", "aws と session-manager-plugin", "AWS の認証", "socat（relay モード）",
		"ローカルポートの衝突", "ssh / aws の config", "鍵ファイル（IdentityFile）", "hosts の設定漏れ", "LAN への公開（relay）", "ECS × relay"}
	if len(rs) != len(want) {
		t.Fatalf("項目の数 = %d", len(rs))
	}
	for i, w := range want {
		if rs[i].Title != w {
			t.Errorf("%d 番目 = %q, want %q", i+1, rs[i].Title, w)
		}
	}
}

func TestProfiles(t *testing.T) {
	cases := []struct {
		name  string
		out   string
		level Level
		line  string
	}{
		{"認証済み", `{"Account":"1"}`, OK, "stg: 認証済み"},
		{"MFA", "ERR:Enter MFA code for arn:aws:iam::1:mfa/u: EOF", Warn, "要認証（MFA）"},
		{"期限切れ", "ERR:The SSO session associated with this profile has expired", Fail, "/usr/bin/aws sso login --profile stg"},
		{"その他", "ERR:could not connect", Fail, "stg: could not connect"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHost(nil, nil, map[string]string{"aws": "/usr/bin/aws"}, fakeRunner{
				"/usr/bin/aws sts get-caller-identity --profile stg": c.out,
				"/usr/bin/aws --version":                             "aws-cli/2\n",
			})
			r := find(t, runDoctor(h, loadConfig(t, ecsYAML), nil, closed), "AWS の認証")
			if r.Level != c.level || !hasLine(r, c.line) {
				t.Errorf("AWS の認証 = %+v", r)
			}
		})
	}
}

func TestIdentityFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("パス区切りが異なるため Unix 系でのみ確認する")
	}
	cfg := loadConfig(t, "hosts:\n  b: { host: bastion, user: ec2-user }\ntunnels:\n  db: { via: b, forwards: [\"15432:db:5432\"] }\n  web: { host: app1, forwards: [\"18080:l:80\"] }\n")
	h := newHost(nil, map[string]string{"/home/u/.ssh/id_ed25519": ""}, map[string]string{"ssh": "/usr/bin/ssh"}, fakeRunner{
		"/usr/bin/ssh -V":                 "OpenSSH_9\n",
		"/usr/bin/ssh -G -- ec2-user@bastion": "identityfile ~/.ssh/id_rsa\nidentityfile ~/.ssh/id_ed25519\n",
		"/usr/bin/ssh -G -- app1":         "identityfile ~/.ssh/id_rsa\n",
	})
	r := find(t, runDoctor(h, cfg, nil, closed), "鍵ファイル（IdentityFile）")
	if r.Level != Warn || !hasLine(r, "db: /home/u/.ssh/id_ed25519") || !hasLine(r, "web: 鍵ファイルが1つも見つかりません") {
		t.Errorf("IdentityFile = %+v", r)
	}
}

func TestIdentityFilesWindowsSide(t *testing.T) {
	cfg := loadConfig(t, "wsl:\n  mode: mirrored\ntunnels:\n  db: { host: ap, forwards: [\"15432:db:5432\"] }\n")
	exe := "/mnt/c/WINDOWS/System32/OpenSSH/ssh.exe"
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"},
		map[string]string{"/mnt/c/Users/taro/.ssh/id_ed25519": "", "/mnt/c/keys/work": ""},
		map[string]string{"ssh.exe": exe},
		fakeRunner{
			"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
			"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
			"wslpath -u C:\\keys\\work":     "/mnt/c/keys/work\n",
			exe + " -V":                     "OpenSSH_for_Windows_9.5p1\r\n",
			exe + " -G -- ap":               "identityfile ~/.ssh/id_ed25519\r\nidentityfile C:\\keys\\work\r\n",
		})
	r := find(t, runDoctor(h, cfg, nil, closed), "鍵ファイル（IdentityFile）")
	if r.Level != OK || !hasLine(r, "/mnt/c/Users/taro/.ssh/id_ed25519") || !hasLine(r, "/mnt/c/keys/work") {
		t.Errorf("ssh.exe は Windows 側のパスで確認する: %+v", r)
	}
}

func TestHostsCheck(t *testing.T) {
	cfg := loadConfig(t, "tunnels:\n  web: { host: app1, hostname: app.local, forwards: [\"18080:l:80\"] }\n")
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"},
		map[string]string{
			"/etc/hosts": "127.0.0.1 localhost app.local\n",
			"/mnt/c/Windows/System32/drivers/etc/hosts": "127.0.0.1 localhost\r\n",
		},
		map[string]string{"ssh.exe": "/mnt/c/ssh.exe"},
		fakeRunner{
			"wslinfo --networking-mode":     "mirrored\n",
			"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
			"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
			"/mnt/c/ssh.exe -V":             "OpenSSH\n",
			"/mnt/c/ssh.exe -G -- app1":     "",
		})
	r := find(t, runDoctor(h, cfg, nil, closed), "hosts の設定漏れ")
	if r.Level != Warn || !hasLine(r, "「127.0.0.1 app.local」が Windows 側（/mnt/c/Windows/System32/drivers/etc/hosts）にありません") {
		t.Errorf("hosts = %+v", r)
	}
	if r := find(t, runDoctor(h, loadConfig(t, sshYAML), nil, closed), "hosts の設定漏れ"); r.Level != NA {
		t.Errorf("hostname が無ければ対象外: %+v", r)
	}
}

func TestLANExposure(t *testing.T) {
	cfg := loadConfig(t, sshYAML)
	run := fakeRunner{
		"wslinfo --networking-mode":     "nat\n",
		"cmd.exe /c echo %USERPROFILE%": "C:\\Users\\taro\r\n",
		"wslpath -u C:\\Users\\taro":    "/mnt/c/Users/taro\n",
		"/mnt/c/ssh.exe -V":             "OpenSSH\n",
		"/mnt/c/ssh.exe -G -- ap":       "",
		"powershell.exe -NoProfile -NonInteractive -Command Get-NetTCPConnection -State Listen -LocalPort 15432 -ErrorAction SilentlyContinue | ForEach-Object { \"$($_.LocalAddress) $($_.LocalPort)\" }": "0.0.0.0 15432\r\n",
	}
	h := newHost(map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, nil, map[string]string{"ssh.exe": "/mnt/c/ssh.exe", "socat": "/usr/bin/socat"}, run)
	runs := []state.Run{{PID: 1, Slots: []state.Slot{
		{Tunnel: "db", Ports: []string{"172.29.16.1:15432"}},
		{Tunnel: "db", Ports: []string{"127.0.0.1:15432"}},
	}}}
	r := find(t, runDoctor(h, cfg, runs, closed), "LAN への公開（relay）")
	if r.Level != Warn || !hasLine(r, "ポート 15432 が 0.0.0.0 で待ち受けています") {
		t.Errorf("LAN = %+v", r)
	}
	if r := find(t, runDoctor(h, cfg, nil, closed), "LAN への公開（relay）"); r.Level != NA || !hasLine(r, "relay で起動中のトンネルがありません") {
		t.Errorf("起動中が無ければ対象外: %+v", r)
	}
}
```

- [ ] **Step 2: テストが失敗することを確認する**

Run: `go test ./internal/doctor/`
Expected: FAIL（項目が 7 つしかない、`AWS の認証` などが無い）

- [ ] **Step 3: 実装する**

`internal/doctor/doctor.go` の `Run` の戻り値を次にする。

```go
	return []Result{
		c.platform(),      // 1
		c.ssh(),           // 2
		c.aws(),           // 3
		c.profiles(),      // 4
		c.socat(),         // 5
		c.ports(),         // 6
		c.configPaths(),   // 7
		c.identityFiles(), // 8
		c.hosts(),         // 9
		c.lanExposure(),   // 10
		c.ecsRelay(),      // 11
	}
```

`internal/doctor/checks_more.go`:

```go
package doctor

import (
	"net"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kukv/hashigo/internal/driver/ecs"
	"github.com/kukv/hashigo/internal/platform"
)

// 4. 使用するプロファイルごとの認証状態。非対話で実行するので、MFA が要るときは失敗する
func (c *checker) profiles() Result {
	const title = "AWS の認証"
	ts := c.tunnels("ecs")
	if len(ts) == 0 {
		return na(title)
	}
	r := ok(title)
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	h := c.e.Host
	seen := map[string]bool{}
	for _, t := range ts {
		eff := c.e.Config.Effective(t)
		bin, err := h.AWSBinary(eff.AWS, c.mode)
		if err != nil {
			continue // 3 で報告する
		}
		env, _, err := h.AWSConfigFor(c.ctx, bin, eff.AWSConfig)
		if err != nil {
			continue // 7 で報告する
		}
		profile := ecs.ResolveProfile(eff.Profile, h.Getenv)
		key := strings.Join(append([]string{bin, profile, eff.Region}, env...), "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		args := []string{"sts", "get-caller-identity", "--profile", profile}
		if eff.Region != "" {
			args = append(args, "--region", eff.Region)
		}
		if _, err := h.Runner(platform.RunOptions{Env: env}).Output(c.ctx, bin, args...); err != nil {
			msg := platform.ErrText(err)
			switch {
			case ecs.IsAuthExpired(msg):
				r.line(Fail, "%s: %s", profile, ecs.LoginHint(bin, profile))
			case strings.Contains(msg, "MFA"):
				r.line(Warn, "%s: 要認証（MFA）。up のときに入力します", profile)
			default:
				r.line(Fail, "%s: %s", profile, msg)
			}
			continue
		}
		r.line(OK, "%s: 認証済み", profile)
	}
	return r
}

// 8. IdentityFile の存在。ssh -G は既定の鍵も列挙するので、1つも存在しないときだけ警告する
func (c *checker) identityFiles() Result {
	const title = "鍵ファイル（IdentityFile）"
	ts := c.tunnels("ssh")
	if len(ts) == 0 {
		return na(title)
	}
	r := ok(title)
	if c.modeErr != nil {
		r.line(Fail, "%v", c.modeErr)
		return r
	}
	h := c.e.Host
	for _, t := range ts {
		eff := c.e.Config.Effective(t)
		bin, err := h.SSHBinary(eff.SSH, c.mode)
		if err != nil {
			continue // 2 で報告する
		}
		route, err := c.e.Config.Route(t)
		if err != nil {
			continue
		}
		arg, _, err := h.SSHConfigFor(c.ctx, bin, eff.SSHConfig)
		if err != nil {
			continue // 7 で報告する
		}
		args := []string{"-G"}
		if arg != "" {
			args = append(args, "-F", arg)
		}
		if len(route.Jumps) > 0 {
			args = append(args, "-J", strings.Join(route.Jumps, ","))
		}
		args = append(args, "--", route.Dest)
		out, err := h.Run.Output(c.ctx, bin, args...)
		if err != nil {
			r.line(Warn, "%s: ssh -G に失敗しました: %s", t.ID(), platform.ErrText(err))
			continue
		}
		var found, missing []string
		for _, p := range parseIdentityFiles(out) {
			lp := c.localPath(bin, p)
			if _, err := h.Stat(lp); err == nil {
				found = append(found, lp)
			} else {
				missing = append(missing, lp)
			}
		}
		if len(found) == 0 {
			r.line(Warn, "%s: 鍵ファイルが1つも見つかりません（%s）", t.ID(), strings.Join(missing, ", "))
		} else {
			r.line(OK, "%s: %s", t.ID(), strings.Join(found, ", "))
		}
	}
	return r
}

// parseIdentityFiles は ssh -G の出力から identityfile の値を取り出す。
func parseIdentityFiles(out []byte) []string {
	var ps []string
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r", ""), "\n") {
		if v, ok := strings.CutPrefix(line, "identityfile "); ok {
			ps = append(ps, strings.TrimSpace(v))
		}
	}
	return ps
}

// localPath は、bin が読む鍵のパスを、hashigo から確認できるパスにする。
func (c *checker) localPath(bin, p string) string {
	h := c.e.Host
	cross := h.CrossesToWindows(bin)
	if rest, ok := strings.CutPrefix(p, "~"); ok {
		if cross {
			if home, err := h.WindowsHome(c.ctx); err == nil {
				return path.Join(home, strings.ReplaceAll(rest, `\`, "/"))
			}
			return p
		}
		if home, err := h.HomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
		return p
	}
	if cross && isWindowsAbs(p) {
		if w, err := h.ToWSLPath(c.ctx, p); err == nil {
			return w
		}
	}
	return p
}

func isWindowsAbs(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

// 9. hosts の設定漏れ。WSL では Windows 側と WSL 側の両方を確認する
func (c *checker) hosts() Result {
	const title = "hosts の設定漏れ"
	var ts []string
	ids := map[string]string{}
	for _, t := range c.e.Tunnels {
		if t.Hostname != "" {
			ts = append(ts, t.Hostname)
			ids[t.Hostname] = t.ID()
		}
	}
	if len(ts) == 0 {
		return na(title)
	}
	r := ok(title)
	h := c.e.Host
	files := h.HostsFiles()
	for _, name := range ts {
		var missing []string
		for _, f := range files {
			data, err := h.ReadFile(f.Path)
			if err != nil {
				r.line(Warn, "%s を読めません: %v", f.Path, err)
				continue
			}
			if !platform.HostsHas(data, name) {
				missing = append(missing, hostsLabel(f))
			}
		}
		if len(missing) > 0 {
			r.line(Warn, "「127.0.0.1 %s」が %s にありません（%s）", name, strings.Join(missing, "と"), ids[name])
		} else {
			r.line(OK, "%s: 設定済み", name)
		}
	}
	return r
}

func hostsLabel(f platform.HostsFile) string {
	if f.Label == "" {
		return f.Path
	}
	return f.Label + " 側（" + f.Path + "）"
}

// 10. relay で動いている場合の LAN 公開（Windows 側の待ち受けアドレスの確認）
func (c *checker) lanExposure() Result {
	const title = "LAN への公開（relay）"
	if c.mode != "relay" {
		return na(title)
	}
	seen := map[int]bool{}
	var ports []int
	for _, run := range c.e.Runs {
		for _, s := range run.Slots {
			for _, p := range s.Ports {
				_, port, err := net.SplitHostPort(p)
				if err != nil {
					continue
				}
				n, err := strconv.Atoi(port)
				if err != nil || seen[n] {
					continue
				}
				seen[n] = true
				ports = append(ports, n)
			}
		}
	}
	if len(ports) == 0 {
		return na(title, "relay で起動中のトンネルがありません")
	}
	sort.Ints(ports)
	r := ok(title)
	ls, err := c.e.Host.WindowsListeners(c.ctx, ports)
	if err != nil {
		r.line(Warn, "%v", err)
		return r
	}
	for _, l := range ls {
		if l.Exposed() {
			r.line(Warn, "Windows 側でポート %d が %s で待ち受けています（LAN に公開されています）", l.Port, l.Addr)
		}
	}
	if len(r.Lines) == 0 {
		r.line(OK, "LAN には公開されていません")
	}
	return r
}
```

- [ ] **Step 4: テストが通ることを確認する**

Run: `go test ./... && make lint`
Expected: PASS

- [ ] **Step 5: コミットする**

```bash
git add internal/doctor
git commit -m "feat(doctor): AWS の認証、IdentityFile、hosts の設定漏れ、relay の LAN 公開の確認を足す

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 16: Docker E2E — ap の再起動後の再接続と `status`

**Files:**
- Modify: `test/e2e/scenario.sh`（全体を置き換える）, `test/e2e/e2e_test.go`, `test/e2e/compose.yaml`, `test/e2e/client/Dockerfile`, `test/e2e/hashigo.yaml`, `.github/workflows/ci.yaml`（コメントだけ）

**Interfaces:**
- Consumes: Task 7〜8（再接続、状態ファイル）、Task 12（`up`）、Task 13（`status`）
- Produces: `make e2e` が、2段・3段のフォワード、`status` の終了コード 0、`ap` の再起動後に three-hop だけが張り直されること、Ctrl-C 後に ssh と状態ファイルが残らないことを確かめる

仕組みの変更: これまでは `docker compose run --rm client` がシナリオを最後まで一気に流していたため、途中でホスト側から `ap` を再起動できなかった。client を `sleep infinity` で常駐させ、`e2e_test.go` が `docker compose exec` でシナリオを段階（start → reconnect → stop）ごとに呼び、その間に `docker compose restart ap` を挟む。`up` は start の段階で `setsid` を使って背景で起動し、段階をまたいで動かし続ける。

- [ ] **Step 1: E2E を書き換える**

`test/e2e/client/Dockerfile` の `ENTRYPOINT` の行を次に置き換える。

```dockerfile
# シナリオは e2e_test.go が docker compose exec で段階ごとに呼ぶ。コンテナは常駐させておく
CMD ["sleep", "infinity"]
```

`test/e2e/compose.yaml` の `client` に `init: true` を足す（背景で動かす `hashigo up` と、その子の ssh を回収させるため）。

```yaml
  client:
    build:
      context: .
      dockerfile: client/Dockerfile
    init: true
    depends_on: [bastion, mid, ap, db]
    volumes: [keys:/keys:ro]
    networks: [front]
```

`test/e2e/hashigo.yaml` の `wsl:` の前に、切断を早く検知するための keepalive を足す。

```yaml
# ap の再起動で切れたことを早く検知するため、keepalive を短くする
defaults:
  keepalive: 5s

```

`test/e2e/scenario.sh` を次の内容で置き換える。

```sh
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
```

`test/e2e/e2e_test.go` の `TestE2E` を次に置き換える。

```go
func TestE2E(t *testing.T) {
	run(t, []string{"CGO_ENABLED=0", "GOOS=linux"}, "go", "build", "-o", "bin/hashigo", "../../cmd/hashigo")
	t.Cleanup(func() {
		cmd := exec.Command("docker", "compose", "down", "-v", "--remove-orphans")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_ = cmd.Run()
	})
	compose := func(args ...string) { run(t, nil, "docker", append([]string{"compose"}, args...)...) }
	scenario := func(phase string) { compose("exec", "-T", "client", "sh", "/e2e/scenario.sh", phase) }

	compose("build")
	compose("up", "-d", "client")
	scenario("start")
	// ap を再起動し、ap を経由する three-hop だけが切れて張り直されることを確かめる
	compose("restart", "ap")
	scenario("reconnect")
	scenario("stop")
}
```

`.github/workflows/ci.yaml` の e2e ジョブの上のコメントを次にする。

```yaml
  # Docker で bastion → mid → ap → db を立て、本物の ssh で2段・3段のフォワード、
  # ap の再起動後の再接続、status、Ctrl-C 後の後始末を確かめる。
```

- [ ] **Step 2: E2E を実行して通ることを確認する**

Run: `make e2e`
Expected: `OK: ap の再起動後に three-hop が張り直された` と `E2E OK` が出て PASS。Docker が無い環境では実行できないので、その場合は CI の e2e ジョブで確かめる

- [ ] **Step 3: 再接続を確かめていることを確認する**

`test/e2e/hashigo.yaml` の `defaults` に一時的に `reconnect: false` を足して `make e2e` を実行する。
Expected: reconnect の段階で `NG: three-hop の再接続が始まらない` または `NG: 25432 で db に届かない` で FAIL する。確認したら `reconnect: false` を消す

- [ ] **Step 4: コミットする**

```bash
git add test/e2e .github/workflows/ci.yaml
git commit -m "test(e2e): ap の再起動後の再接続と status を確かめる段階式のシナリオにする

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

### Task 17: ドキュメント — `docs/acceptance.md`、README、設計書

**Files:**
- Create: `docs/acceptance.md`
- Modify: `README.md`, `README.ja.md`, `docs/superpowers/specs/2026-10-23-hashigo-design.md`

**Interfaces:**
- Consumes: Task 1〜16 のすべて
- Produces: 仕様 10.4 の手動の受け入れ手順（WSL mirrored / relay、実 ECS、Windows ネイティブ、macOS、12章の未確認事項）、新しいコマンドと設定の説明、設計書への裁定の反映

- [ ] **Step 1: `docs/acceptance.md` を書く**

```markdown
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
```

- [ ] **Step 2: README を直す**

`README.ja.md` を次のように直す。

状態の注記:

```markdown
> **状態: MVP。** フォアグラウンドの `up`（Ctrl-C で停止）、`cmd`、`validate` が動きます。再接続、`status`、`doctor`、ECS（SSM）のフォワード、WSL の relay モードは次のフェーズで対応します（[ロードマップ](#ロードマップ)）。
```

を次にする。

```markdown
> **状態: 安定化。** 再接続、ヘルスチェック、`status`、`doctor`、ECS（SSM）のフォワード、WSL の relay モード、Windows ネイティブに対応しています。実機での確認の手順は [docs/acceptance.md](docs/acceptance.md) にあります。
```

「必要なもの」の WSL の行:

```markdown
- WSL では mirrored ネットワーク（`%UserProfile%\.wslconfig` に `networkingMode=mirrored`）。使えない場合は `wsl.mode: native` を指定して、Linux 版の `ssh` を使います。
```

を次にする。

```markdown
- WSL では mirrored ネットワーク（`%UserProfile%\.wslconfig` に `networkingMode=mirrored`）を推奨します。NAT のままなら relay モードになり、WSL 側に `socat` が必要です。`wsl.mode: native` を指定すると Linux 版の `ssh` を使います。
- ECS のトンネルには `aws`（WSL では `aws.exe`）と `session-manager-plugin`、ECS Exec が有効なサービスが必要です。
```

キーの表の `defaults` と `tunnels.<名前>` の行を次にする。

```markdown
| `defaults` | トップレベル | `ssh`（`auto` / `ssh` / `ssh.exe` / パス）、`ssh_config` と `aws_config`（`auto` / `windows` / `wsl` / パス）、`aws`、`keepalive`（既定 `30s`）、`reconnect`（既定 `true`）。下の階層に継承する |
| `tunnels.<名前>` | トップレベル、環境 | ssh: `via` か `host`（どちらか一方）、`forwards`、`hostname`、`keepalive`、`ssh`、`ssh_config`。ECS: `type: ecs`、`cluster`、`service`、`container`、`forwards`、`profile`、`region`、`aws`、`aws_config` |
```

キーの表の直後（「フォワードは、アドレスを指定しない限り…」の段落の前）に追加する。

````markdown
ECS のトンネルは SSM Session Manager（ECS Exec）でつなぎます。タスク ID は接続のたびに引き直します。

```yaml
environments:
  stg:
    aws: { profile: stg }            # 環境内の ECS トンネルに継承する
    tunnels:
      db:
        type: ecs
        region: ap-northeast-1
        cluster: stg-cluster
        service: api
        container: app
        forwards: ["35432:stg-db.internal:5432"]
```
````

「使い方」のコードブロックを次にする。

```sh
hashigo validate                 # 設定を検証する
hashigo cmd prod-db              # 実行する ssh / aws コマンドを表示する
hashigo up prod-db               # トンネルを起動する。Ctrl-C で全部止まる
hashigo up dev                   # 環境内の全トンネル
hashigo up dev prod              # 複数をまとめて
hashigo up dev.db                # 環境内の1本だけ
hashigo up all-db --yes          # protected 環境の確認を省略する
hashigo status                   # 状態を表示する（全部 up なら終了コード 0）
hashigo doctor                   # 実行環境と設定の前提を確かめる
hashigo version
```

「`up` はフォアグラウンドで動きます。」で始まる段落を次にする。

```markdown
`up` はフォアグラウンドで動きます。ローカルポートがすでに使われていると起動せず、使っている相手（別の `hashigo up` か、hashigo 外のプロセスか）を表示します。トンネルが切れたら、1s → 2s → 4s … 最大 60s の間隔で張り直します（`reconnect: false` なら張り直しません）。10 秒ごとにローカルポートへの接続を確かめ、つながらなければ `unhealthy` と表示します。SSO の期限切れのように張り直しても直らない失敗は、`aws sso login` などの案内を出して止めます。Ctrl-C、SIGTERM、端末を閉じることのどれでも、起動したプロセスをすべて止めます。WSL では、Windows 側に残った `ssh.exe` / `aws.exe` も片付けます。

`status` は、`up` が書く状態ファイル（`~/.local/state/hashigo/run/`、Windows は `%LOCALAPPDATA%\hashigo\run\`）と TCP 接続で、トンネルごとの状態、再接続の回数、最後のエラーを表示します。`doctor` は、WSL のネットワークモード、ssh / aws / session-manager-plugin / socat の有無、AWS の認証、ポートの衝突、実際に読む config、鍵ファイル、hosts の設定漏れ（`hostname` を書いたトンネル）、relay の LAN 公開を確かめます。
```

ロードマップの表を次にする。

```markdown
| フェーズ | 内容 |
|---|---|
| MVP | `validate`、`cmd`、フォアグラウンドの `up`、environments と `include`、WSL の `ssh.exe`（mirrored） |
| 安定化（現在） | 指数バックオフでの再接続、ヘルスチェック、`status`、`doctor`、ECS（SSM）のフォワード、WSL の relay モード、`ssh_config` / `aws_config` の切り替え、Windows ネイティブ |
| 拡張 | `up -d` / `down` / `logs`、`-R` と `-D` のフォワード、シェル補完 |
```

その下の段落を次にする。

```markdown
詳しくは [設計書](docs/superpowers/specs/2026-10-23-hashigo-design.md) を参照してください。
```

`README.md`（英語）も同じ箇所を、同じ内容で直す。

状態の注記:

```markdown
> **Status: stabilization.** Reconnecting, health checks, `status`, `doctor`, ECS (SSM)
> forwards, the WSL relay mode and native Windows are supported. The manual acceptance
> steps for real machines are in [docs/acceptance.md](docs/acceptance.md).
```

「Requirements」の WSL の行を次の2行にする。

```markdown
- On WSL: mirrored networking (`networkingMode=mirrored` in `%UserProfile%\.wslconfig`) is
  recommended. With NAT, hashigo uses the relay mode, which needs `socat` on the WSL side.
  `wsl.mode: native` uses the Linux `ssh` instead.
- For ECS tunnels: `aws` (`aws.exe` on WSL), `session-manager-plugin`, and a service with
  ECS Exec enabled.
```

キーの表の2行:

```markdown
| `defaults` | top level | `ssh` (`auto` / `ssh` / `ssh.exe` / a path), `ssh_config` and `aws_config` (`auto` / `windows` / `wsl` / a path), `aws`, `keepalive` (default `30s`), `reconnect` (default `true`); inherited by the levels below |
| `tunnels.<name>` | top level, environment | ssh: `via` or `host` (one of them), `forwards`, `hostname`, `keepalive`, `ssh`, `ssh_config`. ECS: `type: ecs`, `cluster`, `service`, `container`, `forwards`, `profile`, `region`, `aws`, `aws_config` |
```

キーの表の直後に追加する。

````markdown
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
````

「Usage」のコードブロック:

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

「`up` runs in the foreground.」で始まる段落を次にする。

```markdown
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
```

ロードマップの表:

```markdown
| Phase | Contents |
|---|---|
| MVP | `validate`, `cmd`, foreground `up`, environments and `include`, WSL `ssh.exe` (mirrored) |
| Stabilization (current) | Reconnect with backoff, health checks, `status`, `doctor`, ECS (SSM) forwards, WSL relay mode, `ssh_config` / `aws_config` switching, Windows native |
| Extensions | `up -d` / `down` / `logs`, `-R` and `-D` forwards, shell completion |
```

その下の段落:

```markdown
The details are in the
[design document](docs/superpowers/specs/2026-10-23-hashigo-design.md).
```

- [ ] **Step 3: 設計書に裁定を反映する**

`docs/superpowers/specs/2026-10-23-hashigo-design.md` を直す。

3.4 の「待ち受けアドレスの既定は `127.0.0.1`」の行の後ろに追加する。

```markdown
- `type: ecs` の forwards には待ち受けアドレスを書けない（SSM の `localPortNumber` は 127.0.0.1 で待ち受けるため。validate エラー）
```

5.1 の「あるスロットが終了したときは、」で始まる段落の後ろに追加する。

```markdown
スロットの数と並びは `Preview()` で決める。起動直後も含め、各スロットは接続のたびに自分で `Prepare` を呼ぶ（ECS で forward が N 本なら、起動時の list-tasks も N 回になる）。
```

5.2 の「- **ヘルスチェック**:」の行の後ろに追加する。

```markdown
- **起動できないとき**: 子プロセスを起動できない（バイナリが無いなど）ときは、再接続せずに failed にする
```

7.1 の「- **残骸の判定**:」の行の後ろに追加する。

```markdown
- `started_at` には、OS から取ったプロセスの起動時刻（8章）を書き、残骸の判定では同じ方法で取った値と 1 秒以内かで比べる
```

7.2 の「設定にある全トンネル（引数があればそれに絞る）を、スロットごとに1行で表示する。」を次にする。

```markdown
設定にある全トンネル（引数があればそれに絞る）を表示する。状態ファイルがあるトンネルはスロットごとに1行、無いトンネルはトンネルごとに1行（ポートをまとめて表示）にする。
```

12章の表の直後（「MVP の実機受け入れ（プラン Task 14 Step 5）は未実施」の段落の前）に追加する。

```markdown
安定化プラン（`docs/superpowers/plans/2026-10-23-hashigo-stabilization.md`）での扱い:

| # | 扱い |
|---|---|
| 1 | 結果によらず、WSL で .exe を使ったときは停止後に 6.5 の後始末を必ず行う。実機の確認は `docs/acceptance.md` 1-3 |
| 2 | 設計どおり UNC パスの `-F` で実装した。実機の確認は `docs/acceptance.md` 4-1 |
| 3 | 設計どおり `WSLENV` の `/p` で実装した。実機の確認は `docs/acceptance.md` 4-2 |
| 4 | 届かない側（保守的な側）を選び、ECS トンネルを relay で起動しようとしたらエラーにして mirrored を案内する。doctor でも ✗ にする。実機の確認は `docs/acceptance.md` 6-7 |
| 5 | `docs/acceptance.md` 6-3、6-4 で確認する |
| 6 | `docs/acceptance.md` 1-2 で確認する |
| 7 | `WSL_DISTRO_NAME` / `WSL_INTEROP` があるか、`/proc/version` に microsoft があり `/proc/sys/fs/binfmt_misc/WSLInterop` もある場合だけ WSL とみなすように直した |
```

「MVP の実機受け入れ（プラン Task 14 Step 5）は未実施（2026-10-23 時点）。」で始まる段落の最後に、次の1文を足す。

```markdown
手順は `docs/acceptance.md` の 1 章にまとめた。
```

- [ ] **Step 4: 確認する**

Run: `make check`
Expected: PASS（ドキュメントの変更でテストや lint が壊れていない）

Run: `grep -n "安定化フェーズで対応" -r README.md README.ja.md internal/`
Expected: 何も出ない

- [ ] **Step 5: コミットする**

```bash
git add docs/acceptance.md README.md README.ja.md docs/superpowers/specs/2026-10-23-hashigo-design.md
git commit -m "docs: 手動の受け入れ手順を追加し、README と設計書を安定化フェーズに合わせる

Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>"
```

---

## 仕様との対応（セルフレビュー）

### 11章「安定化」の項目

| 仕様の項目 | タスク |
|---|---|
| 再接続（指数バックオフ） | 7 |
| ヘルスチェック | 8 |
| 状態ファイル | 6, 8, 12 |
| `status` | 13 |
| `doctor`（hosts の設定漏れ警告を含む） | 14, 15 |
| relay モード（socat の自動管理） | 4, 10, 11 |
| relay の LAN 公開の警告 | 4, 12, 15 |
| ssh.exe / aws.exe の後始末 | 4, 12 |
| ECS ドライバ（タスク ID の解決、1セッション1ポート、認証切れの検出） | 9 |
| AssumeRole＋MFA の事前認証 | 12 |
| ECS の `cmd` の表示 | 9, 11 |
| `ssh_config` / `aws_config` の参照先切り替え | 3, 11 |
| IdentityFile の存在チェック | 15 |
| Windows ネイティブ対応 | 1（状態ディレクトリ）, 2（Job Object、起動時刻）, 4（VT、hosts）, 13（色） |
| `docs/acceptance.md` | 17 |

### 13章で「安定化」とした行

| 計画書の項目 | タスク |
|---|---|
| 起動前のポートチェックと衝突相手の表示（状態ファイルで強化） | 12 |
| `status`（TCP で生死を判定） | 13 |
| `doctor`（WSL、ネットワークモード、ssh、socat、ポート衝突） | 14 |
| 指数バックオフの再接続（1s→上限 60s） | 7 |
| ヘルスチェック | 8 |
| relay（socat を CLI が管理） | 10, 11 |
| 既定は実行バイナリ側の config、`ssh_config` / `aws_config` で切り替え | 3, 11 |
| relay の LAN 公開の注意 | 12, 15 |
| 停止時に ssh.exe を taskkill | 4, 12 |
| status で環境名を色分け | 13 |
| ECS: `type` でドライバを切り替え、起動・監視・再接続は共通 | 9, 11 |
| ECS: 生成するコマンドを `cmd` で確認 | 9, 11 |
| ECS: 接続・再接続のたびにタスク ID を引き直す | 9（`Prepare`）, 7（R4） |
| ECS: 1セッション1ポート、status ではトンネル単位で表示 | 9, 13 |
| ECS: アイドルタイムアウト後の張り直し | 7, 9（Retry） |
| ECS: 認証切れで停止し `aws sso login` を案内 | 7（Fatal）, 9 |
| ECS: 前提条件（plugin、プロファイル認証）を doctor で確認 | 14, 15 |
| WSL では aws.exe、mirrored / relay は SSH と共通 | 1, 11（R2） |
| バイナリ × config の組み合わせ表 | 3 |
| cmd と doctor で config のパスを表示（doctor） | 14 |
| IdentityFile の存在確認 | 15 |
| AWS プロファイル（`--profile`、AWS_PROFILE、default）、region | 9 |
| SSO のキャッシュは実行バイナリ側、同じバイナリで login を案内 | 9 |
| AssumeRole＋MFA の事前認証 | 12 |
| hosts ファイルでの対処（警告） | 15 |
| `hostname` を書くと doctor が hosts の設定漏れを警告 | 15 |
| 確認事項3（ECS Exec の有効化） | 17（acceptance 0 章） |
| 確認事項4（socat 中継の LAN 公開） | 10（仮想 NIC の IP で待ち受け）, 15 |
| 実装中の確認: kill 時に ssh.exe が残るか | 12（後始末）, 17（acceptance 1-3） |
| 実装中の確認: `-F` に UNC パス | 3, 17（acceptance 4-1） |
| 実装中の確認: WSLENV で AWS_CONFIG_FILE | 3, 17（acceptance 4-2） |
| 実装中の確認: ECS の安定性 | 17（acceptance 6-3、6-4） |
| 計画書に無い追加: aws.exe / session-manager-plugin.exe の後始末 | 4, 12 |
| 計画書に無い追加: macOS の残骸判定（`sysctl`） | 2 |
| 計画書に無い追加: `status` / `doctor` の終了コード | 13, 14 |

### 12章の未確認事項

| # | タスク |
|---|---|
| 1 | 4, 12（後始末を常に行う）, 17（acceptance 1-3） |
| 2 | 3, 17（acceptance 4-1） |
| 3 | 3, 17（acceptance 4-2） |
| 4 | 11, 14（R2）, 17（acceptance 6-7） |
| 5 | 17（acceptance 6-3、6-4） |
| 6 | 17（acceptance 1-2） |
| 7 | 1 |

### そのほかの仕様の記述

| 仕様の記述 | タスク |
|---|---|
| 2章: `Runner` と `Clock` で外部コマンドと時刻を差し替える | 1（Runner の拡張）, 7（Clock） |
| 4章: `Classify(err, stderrTail)` と `Exit{Fatal, Message}` | 5 |
| 4.2: aws の出力の CRLF を取り除く | 9 |
| 4.2: RUNNING のタスクが無ければ Retry | 9 |
| 5.3: 起動前のチェックの順番（検証 → protected → ポート → 事前認証） | 12 |
| 5.5: 状態が変わったときに1行ずつ出す、stderr の末尾 4KB を Classify に渡す | 7, 8 |
| 6.1: `wslinfo`、無ければ `.wslconfig`、分からなければ relay | MVP で実装済み（Task 14 の doctor で表示） |
| 6.2: `aws: auto` の選択 | 1 |
| 6.4: ゲートウェイ IP は Prepare のたびに `ip route` から取り直す | 4, 10 |
| 7.1: 正常終了したら削除、残骸の判定 | 6, 8 |
| 8章: `start` / `stop` / `isAlive` を OS ごとに実装 | 2 |
| 8章: Windows の色は `SetConsoleMode` で VT を有効にしてから | 4, 13 |
| 8章: Windows ネイティブでは WSL の doctor 項目は「対象外」 | 14 |
| 10.1: バックオフ（偽の Clock）、状態ファイル、WSL 判定、パス変換の単体テスト | 1, 3, 6, 7 |
| 10.2: 偽バイナリで再接続、Fatal、停止のエスカレーション、ポート衝突、状態ファイル | 7, 8, 12 |
| 10.3: Docker E2E で ap の再起動後に再接続、Ctrl-C で子が残らない | 16 |
| 10.4: 手動の受け入れ手順 | 17 |
