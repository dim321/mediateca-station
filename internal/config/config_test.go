package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validYAML = `
hub_base_url: "https://hub.example"
agent_token: "tok"
http_listen: "192.168.1.10:8080"
http_secret: "0123456789abcdef0123456789abcdef"
data_dir: "/var/lib/mediateca-station"
screens:
  - screen_id: 7
    adb_serial: "192.168.1.21:5555"
`

func writeConfig(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDefaultsAndScreens(t *testing.T) {
	cfg, err := Load(writeConfig(t, validYAML, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HubBaseURL != "https://hub.example" || cfg.AgentToken != "tok" {
		t.Fatalf("%+v", cfg)
	}
	if cfg.LateThreshold != 2*time.Second || cfg.PollInterval != time.Minute {
		t.Fatalf("defaults: late=%s poll=%s", cfg.LateThreshold, cfg.PollInterval)
	}
	serial, ok := cfg.Serial(7)
	if !ok || serial != "192.168.1.21:5555" {
		t.Fatalf("serial %q ok=%v", serial, ok)
	}
	if _, ok := cfg.Serial(8); ok {
		t.Fatal("unexpected serial")
	}
}

func TestLoadHTTPPublic(t *testing.T) {
	body := strings.Replace(validYAML, "http_listen: \"192.168.1.10:8080\"", "http_listen: \"0.0.0.0:8080\"\nhttp_public: \"10.0.0.8:8080\"", 1)
	cfg, err := Load(writeConfig(t, body, 0o600))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPListen != "0.0.0.0:8080" || cfg.HTTPPublic != "10.0.0.8:8080" {
		t.Fatalf("%+v", cfg)
	}
}

func TestLoadRejectsGroupReadableFile(t *testing.T) {
	_, err := Load(writeConfig(t, validYAML, 0o640))
	if err == nil || !strings.Contains(err.Error(), "group or world readable") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadRejectsInvalidFields(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"secret", strings.Replace(validYAML, "0123456789abcdef0123456789abcdef", "short", 1), "http_secret"},
		{"serial", strings.Replace(validYAML, "192.168.1.21:5555", "emulator", 1), "adb_serial"},
		{"no screens", strings.Replace(validYAML, "screens:\n  - screen_id: 7\n    adb_serial: \"192.168.1.21:5555\"\n", "screens: []\n", 1), "at least one screen"},
		{"dup", validYAML + "  - screen_id: 7\n    adb_serial: \"192.168.1.22:5555\"\n", "duplicate screen_id"},
		{"duration", validYAML + "late_threshold: \"nope\"\n", "late_threshold"},
		{"token", strings.Replace(validYAML, "agent_token: \"tok\"", "agent_token: \"\"", 1), "agent_token"},
		{"public", validYAML + "http_public: \"not-a-host\"\n", "http_public"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.yaml, 0o600))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want substring %q", err, tc.want)
			}
		})
	}
}
