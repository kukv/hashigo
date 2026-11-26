// Package awscli は、aws CLI（SSM Session Manager）を使うドライバ（ecs / ssm）で共通の処理をまとめる。
package awscli

import (
	"fmt"
	"strings"

	"github.com/kukv/hashigo/internal/driver"
)

// PortForwardDocument は、宛先ホストへのポートフォワードに使う SSM ドキュメント。
const PortForwardDocument = "AWS-StartPortForwardingSessionToRemoteHost"

// Common は、すべての aws 呼び出しに付ける --profile と --region を返す。region が空なら --region を付けない。
func Common(profile, region string) []string {
	a := []string{"--profile", profile}
	if region != "" {
		a = append(a, "--region", region)
	}
	return a
}

// Clean は aws.exe の出力の CRLF と前後の空白を取り除く。
func Clean(b []byte) string {
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

// Classify は認証切れ（MFA の入力待ちで失敗した場合を含む）なら Fatal にする。それ以外（対象が見つからない、
// アイドルタイムアウトで切れたなど）は再接続する。
func Classify(bin, profile string, err error, tail []byte) driver.Exit {
	text := string(tail)
	if err != nil {
		text += "\n" + err.Error()
	}
	if IsAuthExpired(text) {
		return driver.Exit{Fatal: true, Message: LoginHint(bin, profile)}
	}
	// 実行中に MFA 付きの一時認証情報が切れると、再接続時に非対話で MFA を聞かれて失敗し続けるので、再試行しない
	if strings.Contains(text, "MFA") {
		return driver.Exit{Fatal: true, Message: fmt.Sprintf("MFA の認証が切れました。hashigo up をもう一度実行して MFA コードを入力してください（profile: %s）", profile)}
	}
	return driver.Exit{}
}
