package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Screen struct {
	ScreenID  int
	ADBSerial string
}

type File struct {
	HubBaseURL    string
	AgentToken    string
	HTTPListen    string
	HTTPSecret    string
	LateThreshold time.Duration
	PollInterval  time.Duration
	DataDir       string
	Screens       []Screen
}

func (f File) Serial(screenID int) (string, bool) {
	for _, screen := range f.Screens {
		if screen.ScreenID == screenID {
			return screen.ADBSerial, true
		}
	}
	return "", false
}

type fileYAML struct {
	HubBaseURL    string       `yaml:"hub_base_url"`
	AgentToken    string       `yaml:"agent_token"`
	HTTPListen    string       `yaml:"http_listen"`
	HTTPSecret    string       `yaml:"http_secret"`
	LateThreshold string       `yaml:"late_threshold"`
	PollInterval  string       `yaml:"poll_interval"`
	DataDir       string       `yaml:"data_dir"`
	Screens       []screenYAML `yaml:"screens"`
}

type screenYAML struct {
	ScreenID  int    `yaml:"screen_id"`
	ADBSerial string `yaml:"adb_serial"`
}

func Load(path string) (File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return File{}, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return File{}, fmt.Errorf("config %s is group or world readable", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var doc fileYAML
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return File{}, err
	}
	cfg := File{
		HubBaseURL:    doc.HubBaseURL,
		AgentToken:    doc.AgentToken,
		HTTPListen:    doc.HTTPListen,
		HTTPSecret:    doc.HTTPSecret,
		LateThreshold: 2 * time.Second,
		PollInterval:  time.Minute,
		DataDir:       doc.DataDir,
	}
	if err := cfg.applyDurations(doc); err != nil {
		return File{}, err
	}
	if err := cfg.validate(doc); err != nil {
		return File{}, err
	}
	return cfg, nil
}

func (f *File) applyDurations(doc fileYAML) error {
	if doc.LateThreshold != "" {
		parsed, err := time.ParseDuration(doc.LateThreshold)
		if err != nil {
			return fmt.Errorf("late_threshold: %w", err)
		}
		f.LateThreshold = parsed
	}
	if doc.PollInterval != "" {
		parsed, err := time.ParseDuration(doc.PollInterval)
		if err != nil {
			return fmt.Errorf("poll_interval: %w", err)
		}
		f.PollInterval = parsed
	}
	return nil
}

func (f *File) validate(doc fileYAML) error {
	if f.HubBaseURL == "" {
		return fmt.Errorf("hub_base_url is required")
	}
	u, err := url.Parse(f.HubBaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("hub_base_url is invalid")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("hub_base_url must not include a path")
	}
	if f.AgentToken == "" {
		return fmt.Errorf("agent_token is required")
	}
	if _, _, err := net.SplitHostPort(f.HTTPListen); err != nil {
		return fmt.Errorf("http_listen is invalid")
	}
	if len(f.HTTPSecret) < 32 {
		return fmt.Errorf("http_secret must be at least 32 characters")
	}
	if f.DataDir == "" {
		return fmt.Errorf("data_dir is required")
	}
	if len(doc.Screens) == 0 {
		return fmt.Errorf("at least one screen is required")
	}
	seen := map[int]struct{}{}
	for _, screen := range doc.Screens {
		if screen.ScreenID == 0 {
			return fmt.Errorf("screen_id is required")
		}
		if _, ok := seen[screen.ScreenID]; ok {
			return fmt.Errorf("duplicate screen_id %d", screen.ScreenID)
		}
		seen[screen.ScreenID] = struct{}{}
		if !stringsContainsColon(screen.ADBSerial) {
			return fmt.Errorf("adb_serial must be host:port")
		}
		f.Screens = append(f.Screens, Screen{ScreenID: screen.ScreenID, ADBSerial: screen.ADBSerial})
	}
	return nil
}

func stringsContainsColon(value string) bool {
	for _, r := range value {
		if r == ':' {
			return value != "" && value[0] != ':' && value[len(value)-1] != ':'
		}
	}
	return false
}
