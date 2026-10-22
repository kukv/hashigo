package config

import "time"

// Effective は defaults → 環境 → トンネルの順に上書きした、トンネルの実際の設定。
type Effective struct {
	SSH, SSHConfig, AWS, AWSConfig string
	Keepalive                      time.Duration
	Reconnect                      bool
	Profile, Region                string
	Protected                      bool
}

// Effective はトンネルの実際の設定を返す。
func (c *Config) Effective(t *Tunnel) Effective {
	e := Effective{
		SSH: "auto", SSHConfig: "auto", AWS: "auto", AWSConfig: "auto",
		Keepalive: 30 * time.Second,
		Reconnect: true,
	}
	e.apply(c.Defaults)
	if env := c.Envs[t.Env]; env != nil {
		e.apply(env.Settings)
		e.Protected = env.Protected
	}
	e.apply(t.Settings)
	return e
}

func (e *Effective) apply(s Settings) {
	if s.SSH != "" {
		e.SSH = s.SSH
	}
	if s.SSHConfig != "" {
		e.SSHConfig = s.SSHConfig
	}
	if s.AWS != "" {
		e.AWS = s.AWS
	}
	if s.AWSConfig != "" {
		e.AWSConfig = s.AWSConfig
	}
	if s.Keepalive != "" {
		if d, err := time.ParseDuration(s.Keepalive); err == nil {
			e.Keepalive = d
		}
	}
	if s.Reconnect != nil {
		e.Reconnect = *s.Reconnect
	}
	if s.Profile != "" {
		e.Profile = s.Profile
	}
	if s.Region != "" {
		e.Region = s.Region
	}
}
