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
