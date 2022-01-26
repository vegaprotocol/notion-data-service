package main

import "github.com/ilyakaznacheev/cleanenv"

type ConfigVars struct {
	Port               string `yaml:"port" env:"PORT" env-default:"5432"`
	Host               string `yaml:"host" env:"HOST" env-default:""`
	NotionPollDuration string `yaml:"notionPollDuration" env:"NOTION-POLL-DURATION" env-default:"6h"`
	NotionAccessToken  string `yaml:"notionAccessToken" env:"NOTION-TOKEN" env-default:""`
}

// ReadConfig loads configuration from the specified file path
func ReadConfig(path string) (ConfigVars, error) {
	var cfg ConfigVars
	err := cleanenv.ReadConfig(path, &cfg)
	if err != nil {
		return cfg, err
	}
	return cfg, nil
}
