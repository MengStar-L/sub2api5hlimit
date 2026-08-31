package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	EnvListen       = "SUB2API_LIMIT_LISTEN"
	EnvDBPath       = "SUB2API_LIMIT_DB_PATH"
	EnvMasterKey    = "SUB2API_LIMIT_MASTER_KEY"
	EnvCookieSecure = "SUB2API_LIMIT_COOKIE_SECURE"
)

type Config struct {
	Listen       string
	DBPath       string
	MasterKey    []byte
	CookieSecure bool
}

func Load() (Config, error) {
	cfg := Config{
		Listen:       "0.0.0.0:2556",
		DBPath:       filepath.Join("data", "app.db"),
		CookieSecure: false,
	}
	if value := strings.TrimSpace(os.Getenv(EnvListen)); value != "" {
		cfg.Listen = value
	}
	if value := strings.TrimSpace(os.Getenv(EnvDBPath)); value != "" {
		cfg.DBPath = value
	}
	if value := strings.TrimSpace(os.Getenv(EnvCookieSecure)); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("%s must be true or false", EnvCookieSecure)
		}
		cfg.CookieSecure = parsed
	}
	rawKey := strings.TrimSpace(os.Getenv(EnvMasterKey))
	if rawKey == "" {
		return Config{}, fmt.Errorf("%s is required; run sub2api-limit-portal keygen", EnvMasterKey)
	}
	key, err := base64.StdEncoding.DecodeString(rawKey)
	if err != nil || len(key) != 32 {
		return Config{}, fmt.Errorf("%s must be a base64-encoded 32-byte key", EnvMasterKey)
	}
	cfg.MasterKey = key
	if strings.TrimSpace(cfg.Listen) == "" {
		return Config{}, errors.New("listen address is required")
	}
	return cfg, nil
}
