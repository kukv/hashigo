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
