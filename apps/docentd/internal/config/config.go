package config

import (
	"os"
	"path/filepath"

	"github.com/kurt/slakkr-ai/internal/configschema"
	"github.com/kurt/slakkr-ai/internal/docentconfig"
	"github.com/kurt/slakkr-ai/internal/executionmode"
	"github.com/kurt/slakkr-ai/internal/userdata"
	"gopkg.in/yaml.v3"
)

// DaemonConfig is docentd.yaml plus the unified app config from configDir/config.yaml.
type DaemonConfig struct {
	Port           int    `yaml:"port"`
	Token          string `yaml:"token"`
	RefreshSec     int    `yaml:"refreshSec"`
	TicketPattern  string `yaml:"ticketPattern"`
	RegistryPath   string `yaml:"registryPath"`
	ConfigDir      string `yaml:"configDir"`              // ~/.config/docent — config.yaml + .env
	UserdataDir    string `yaml:"userdataDir,omitempty"`  // deprecated alias for configDir
	SlakkrConfig   string `yaml:"slakkrConfig,omitempty"` // optional extra config from slakkr userdata
	DocentWMURL    string `yaml:"docentWmUrl"`            // local wm URL injected into dashboard
	Directives     []userdata.Directive `yaml:"directives,omitempty"`

	// Loaded from configDir/config.yaml (not docentd.yaml). AI is optional.
	AI             userdata.AIConfig             `yaml:"-"`
	ExecutionModes []executionmode.ExecutionMode `yaml:"-"`
}

func Load(path string) (DaemonConfig, error) {
	cfg := DaemonConfig{
		Port:        39787,
		RefreshSec:  60,
		DocentWMURL: "http://127.0.0.1:39788",
	}
	if path == "" {
		path = docentconfig.DaemonConfigPath()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	cfg.ConfigDir = resolveConfigDir(cfg)
	if err := mergeAppConfig(&cfg); err != nil {
		return cfg, err
	}
	if token := os.Getenv("DOCENT_TOKEN"); token != "" {
		cfg.Token = token
	}
	return cfg, nil
}

func resolveConfigDir(cfg DaemonConfig) string {
	if cfg.ConfigDir != "" {
		return cfg.ConfigDir
	}
	if cfg.UserdataDir != "" {
		return cfg.UserdataDir
	}
	return docentconfig.DefaultDir()
}

func mergeAppConfig(cfg *DaemonConfig) error {
	file, err := loadConfigFile(filepath.Join(cfg.ConfigDir, "config.yaml"))
	if err != nil {
		return err
	}
	cfg.Directives = append(cfg.Directives, file.Directives...)
	if cfg.AI.Provider == "" {
		cfg.AI = file.AI
	}
	if len(cfg.ExecutionModes) == 0 {
		cfg.ExecutionModes = file.ExecutionModes
	}
	if cfg.SlakkrConfig != "" {
		slakkr, err := loadConfigFile(cfg.SlakkrConfig)
		if err != nil {
			return err
		}
		cfg.Directives = append(cfg.Directives, slakkr.Directives...)
		if cfg.AI.Provider == "" {
			cfg.AI = slakkr.AI
		}
		if len(cfg.ExecutionModes) == 0 {
			cfg.ExecutionModes = slakkr.ExecutionModes
		}
	}
	return nil
}

// loadConfigFile reads config.yaml. Directives are always optional; ai and
// execution_modes are optional but validated when present.
func loadConfigFile(path string) (userdata.ConfigFile, error) {
	var file userdata.ConfigFile
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return file, nil
		}
		return file, err
	}
	if len(b) == 0 {
		return file, nil
	}
	if err := configschema.ValidateYAML(b); err != nil {
		return file, userdata.ValidationError{Problems: configschema.ValidationProblems(err)}
	}
	if err := yaml.Unmarshal(b, &file); err != nil {
		return file, err
	}
	if err := file.Validate(); err != nil {
		return file, err
	}
	return file, nil
}
