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
	LogLevel        string       `json:"logLevel"`
	ServerVersion   string       `json:"serverVersion"`
	MinAPIVersion   string       `json:"minAPIVersion"`
	APIVersion      string       `json:"apiVersion"`
	KubeConfigPath  string       `json:"kubeConfigPath"`
	Namespace       string       `json:"namespace"`
	Port            string       `json:"port"`
	DisableTLS      bool         `json:"disableTLS"`
	TLSPort         string       `json:"tlsPort"`
	EnvPrefix       string       `json:"envPrefix"`
	BuildKitAddress string       `json:"buildKitAddress"`
	AuthPlugins     []AuthPlugin `json:"authPlugins"`
}

type AuthPlugin struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func NewConfig() *Config {
	return &Config{
		LogLevel:       "info",
		ServerVersion:  "v1.0.0",
		MinAPIVersion:  "v1.40",
		APIVersion:     "v1.55",
		KubeConfigPath: "",
		Namespace:      "dink",
		Port:           "2375",
		DisableTLS:     false,
		TLSPort:        "2776",
		EnvPrefix:      "DINK",
		AuthPlugins:    []AuthPlugin{},
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
