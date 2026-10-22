package config

import (
	"strings"
	"testing"
)

func TestParseForward(t *testing.T) {
	got, err := ParseForward("15432:db.internal:5432")
	want := Forward{Bind: "127.0.0.1", LocalPort: 15432, RemoteHost: "db.internal", RemotePort: 5432}
	if err != nil || got != want {
		t.Fatalf("ParseForward = %+v, %v", got, err)
	}
	for _, bad := range []string{"15432:db", "a:db:5432", "15432::5432", "99999:db:1", "1:db:0"} {
		if _, err := ParseForward(bad); err == nil {
			t.Errorf("%q はエラーになるべき", bad)
		}
	}
}

func TestParseDetailedForward(t *testing.T) {
	cases := []struct {
		local, remote string
		want          Forward
	}{
		{"15432", "db:5432", Forward{"127.0.0.1", 15432, "db", 5432}},
		{"0.0.0.0:8080", "localhost:80", Forward{"0.0.0.0", 8080, "localhost", 80}},
		{"[::1]:8080", "[fd00::1]:80", Forward{"::1", 8080, "fd00::1", 80}},
	}
	for _, c := range cases {
		got, err := parseDetailedForward(c.local, c.remote)
		if err != nil || got != c.want {
			t.Errorf("parseDetailedForward(%q, %q) = %+v, %v", c.local, c.remote, got, err)
		}
	}
	if _, err := parseDetailedForward("", "db:1"); err == nil || !strings.Contains(err.Error(), "local") {
		t.Errorf("local が無ければエラー: %v", err)
	}
	if _, err := parseDetailedForward("1", "db"); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Errorf("remote の形式エラー: %v", err)
	}
}

func TestLocalAddr(t *testing.T) {
	if got := (Forward{Bind: "::1", LocalPort: 80}).LocalAddr(); got != "[::1]:80" {
		t.Errorf("LocalAddr = %q", got)
	}
}
