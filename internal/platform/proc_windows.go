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
