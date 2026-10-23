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
