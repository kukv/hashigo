//go:build !linux && !darwin && !windows

package platform

import (
	"errors"
	"time"
)

// StartToken はこの OS では未対応。
func StartToken(int) (string, error) {
	return "", errors.New("この OS ではプロセスの起動時刻を取得できません")
}

// StartTime はこの OS では未対応。状態ファイルはすべて残骸とみなされる。
func StartTime(int) (time.Time, error) {
	return time.Time{}, errors.New("この OS ではプロセスの起動時刻を取得できません")
}
