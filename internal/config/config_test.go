package config

import (
	"encoding/base64"
	"path/filepath"
	"testing"
)

func TestLoadUsesPublicHTTPDefaults(t *testing.T) {
	t.Setenv(EnvMasterKey, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv(EnvListen, "")
	t.Setenv(EnvDBPath, "")
	t.Setenv(EnvCookieSecure, "")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "0.0.0.0:2556" || cfg.DBPath != filepath.Join("data", "app.db") || cfg.CookieSecure ||
		cfg.UpdateStatusPath != "/opt/sub2api5hlimit/update/status.json" ||
		cfg.UpdaterPath != "/opt/sub2api5hlimit/bin/sub2api-limit-updater" {
		t.Fatalf("defaults = %#v", cfg)
	}
}

func TestLoadAllowsExplicitSecureCookies(t *testing.T) {
	t.Setenv(EnvMasterKey, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv(EnvCookieSecure, "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CookieSecure {
		t.Fatal("explicit secure cookie setting was ignored")
	}
}
