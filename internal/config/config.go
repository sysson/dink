package config

import (
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/cliflagv3"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/urfave/cli/v3"
)

type Config struct {
	LogLevel         string       `json:"logLevel"`
	MinAPIVersion    string       `json:"minAPIVersion"`
	APIVersion       string       `json:"apiVersion"`
	KubeConfigPath   string       `json:"kubeConfigPath"`
	DefaultNamespace string       `json:"defaultNamespace"`
	Port             string       `json:"port"`
	TLSOnly          bool         `json:"tlsOnly"`
	TLSPort          string       `json:"tlsPort"`
	CertFile         string       `json:"certFile"`
	KeyFile          string       `json:"keyFile"`
	ClientCAFile     string       `json:"clientCAFile"`
	EnvPrefix        string       `json:"envPrefix"`
	AuthPlugins      []AuthPlugin `json:"authPlugins"`
}

type AuthPlugin struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func NewConfig() *Config {
	return &Config{
		LogLevel:         "info",
		MinAPIVersion:    "v1.40",
		APIVersion:       "v1.55",
		KubeConfigPath:   "",
		DefaultNamespace: "dink",
		Port:             "2375",
		TLSOnly:          true,
		TLSPort:          "2776",
		CertFile:         "/etc/dink/tls/tls.crt",
		KeyFile:          "/etc/dink/tls/tls.key",
		ClientCAFile:     "/etc/dink/tls/ca.crt",
		EnvPrefix:        "DINK",
		AuthPlugins:      []AuthPlugin{},
	}
}

func (c *Config) SetEffectiveConfig(cmd *cli.Command) error {
	k := koanf.New(".")
	var config = ""
	if cmd.String("config") != "" {
		config = cmd.String("config")
	}

	if config != "" {
		if err := k.Load(file.Provider(config), yaml.Parser()); err != nil {
			println("No configuration file found at " + config + ", using defaults and environment variables")
		}
	}

	if err := k.Load(env.Provider(".", env.Opt{
		Prefix: c.EnvPrefix,
		TransformFunc: func(k, v string) (string, any) {
			k = strings.ReplaceAll(strings.ToLower((strings.TrimPrefix(k, c.EnvPrefix+"_"))), "__", ".")
			if strings.Contains(v, " ") {
				return k, strings.Split(v, " ")
			}
			return k, v
		},
	}), nil); err != nil {
		return err
	}
	if err := k.Load(cliflagv3.Provider(cmd, "."), nil); err != nil {
		return err
	}
	return k.Unmarshal("", c)
}
