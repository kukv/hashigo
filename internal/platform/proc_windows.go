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
