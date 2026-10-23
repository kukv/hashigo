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
