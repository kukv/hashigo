package config

import (
	"errors"
	"fmt"
)

// SettingsYAML は継承できるキーの YAML 表現。デコード専用で、パッケージ外からは使わない。
type SettingsYAML struct {
	SSH       string  `yaml:"ssh"`
	SSHConfig string  `yaml:"ssh_config"`
	AWS       *rawAWS `yaml:"aws"`
	AWSConfig string  `yaml:"aws_config"`
	Keepalive string  `yaml:"keepalive"`
	Reconnect *bool   `yaml:"reconnect"`
	Profile   string  `yaml:"profile"`
	Region    string  `yaml:"region"`
}

type rawFile struct {
	Include      []string             `yaml:"include"`
	Defaults     *SettingsYAML        `yaml:"defaults"`
	WSL          *rawWSL              `yaml:"wsl"`
	Hosts        map[string]rawHost   `yaml:"hosts"`
	Tunnels      map[string]rawTunnel `yaml:"tunnels"`
	Groups       map[string][]string  `yaml:"groups"`
	Environments map[string]rawEnv    `yaml:"environments"`
}

type rawWSL struct {
	Mode string `yaml:"mode"`
}

type rawHost struct {
	Host string `yaml:"host"`
	User string `yaml:"user"`
	Via  string `yaml:"via"`
}

type rawEnv struct {
	SettingsYAML `yaml:",inline"`
	Protected    bool                 `yaml:"protected"`
	Hosts        map[string]rawHost   `yaml:"hosts"`
	Tunnels      map[string]rawTunnel `yaml:"tunnels"`
}

type rawTunnel struct {
	SettingsYAML `yaml:",inline"`
	Type         string       `yaml:"type"`
	Host         string       `yaml:"host"`
	Via          string       `yaml:"via"`
	Cluster      string       `yaml:"cluster"`
	Service      string       `yaml:"service"`
	Container    string       `yaml:"container"`
	Hostname     string       `yaml:"hostname"`
	Forwards     []rawForward `yaml:"forwards"`
}

// rawAWS は aws: のスカラー（バイナリ）とマッピング（{path, profile, region}）の両方を受ける。
// 形式の誤りはデコードを止めずに Err に残し、ほかの問題と一緒に報告する。
type rawAWS struct {
	Path, Profile, Region string
	Err                   error
}

func (a *rawAWS) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	switch x := v.(type) {
	case string:
		a.Path = x
	case map[string]any:
		for k, val := range x {
			s, ok := val.(string)
			if !ok {
				a.Err = fmt.Errorf("aws.%s は文字列で書きます", k)
				return nil
			}
			switch k {
			case "path":
				a.Path = s
			case "profile":
				a.Profile = s
			case "region":
				a.Region = s
			default:
				a.Err = fmt.Errorf("aws に未知のキー %q があります", k)
				return nil
			}
		}
	default:
		a.Err = errors.New("aws は文字列か {path, profile, region} のマッピングで書きます")
	}
	return nil
}

// rawForward は forwards の短縮記法（文字列）と詳細記法（{local, remote}）の両方を受ける。
type rawForward struct {
	Forward
	Err error
}

func (f *rawForward) UnmarshalYAML(unmarshal func(any) error) error {
	var v any
	if err := unmarshal(&v); err != nil {
		return err
	}
	switch x := v.(type) {
	case string:
		f.Forward, f.Err = ParseForward(x)
	case map[string]any:
		var local, remote string
		for k, val := range x {
			switch k {
			case "local":
				local = fmt.Sprint(val)
			case "remote":
				remote = fmt.Sprint(val)
			default:
				f.Err = fmt.Errorf("forwards に未知のキー %q があります", k)
				return nil
			}
		}
		f.Forward, f.Err = parseDetailedForward(local, remote)
	default:
		f.Err = errors.New("forwards は文字列か {local, remote} で書きます")
	}
	return nil
}
