package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDenylistConfig(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"yaml", "denylist:\n  packages:\n    - 'pkg:pypi/requests@2.31.0'\n"},
		{"json", `{"denylist":{"packages":["pkg:pypi/requests@2.31.0"]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config."+tc.name)
			if err := os.WriteFile(file, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(file)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Denylist.Packages) != 1 || cfg.Denylist.Packages[0] != "pkg:pypi/requests@2.31.0" {
				t.Fatalf("denylist = %+v", cfg.Denylist)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
			cfg.Denylist.Packages = []string{"pkg:pypi/requests"}
			if err := cfg.Validate(); err == nil {
				t.Fatal("unversioned denylist entry accepted")
			}
		})
	}
}
