package awscli

import (
	"errors"
	"strings"
	"testing"
)

func TestClassify(t *testing.T) {
	for _, msg := range []string{
		"An error occurred (ExpiredTokenException) when calling the StartSession operation",
		"The SSO session associated with this profile has expired or is otherwise invalid.",
		"Token has expired and refresh failed",
		"An error occurred (UnrecognizedClientException): The security token included in the request is expired",
	} {
		ex := Classify("aws", "stg", errors.New("exit status 255"), []byte(msg))
		if !ex.Fatal || !strings.Contains(ex.Message, "aws sso login --profile stg") {
			t.Errorf("認証切れは Fatal: %s → %+v", msg, ex)
		}
	}
	mfa := Classify("aws", "stg", errors.New("exit status 255"), []byte("Enter MFA code for arn:aws:iam::1:mfa/me: \nAborted!"))
	if !mfa.Fatal || !strings.Contains(mfa.Message, "MFA の認証が切れました") {
		t.Errorf("MFA の入力待ちで失敗したら Fatal: %+v", mfa)
	}
	if Classify("aws", "stg", errors.New("exit status 0"), []byte("Exiting session with sessionId: x")).Fatal {
		t.Error("アイドルタイムアウトなどの終了は再接続する")
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

func TestCommon(t *testing.T) {
	if got := strings.Join(Common("p", ""), " "); got != "--profile p" {
		t.Errorf("region が空なら付けない: %s", got)
	}
	if got := strings.Join(Common("p", "r"), " "); got != "--profile p --region r" {
		t.Errorf("Common = %s", got)
	}
}
