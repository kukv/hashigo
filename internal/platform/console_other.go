//go:build !windows

package platform

import "os"

// EnableVT は Windows 以外では何もしない。端末は ANSI エスケープを解釈できる。
func EnableVT(*os.File) bool { return true }
