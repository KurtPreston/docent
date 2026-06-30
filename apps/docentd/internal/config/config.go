package config

import (
	"os"
	"path/filepath"

	"github.com/kurt/slakkr-ai/internal/userdata"
	"gopkg.in/yaml.v3"
)

// DaemonConfig extends slakkr directives with docentd settings.
type DaemonConfig struct {
	Port        int    `yaml:"port"`
	Token       string `yaml:"token"`
	RefreshSec  int    `yaml:"refreshSec"`
	TicketPattern string `yaml:"ticketPattern"`
	RegistryPath string `yaml:"registryPath"`
	UserdataDir string `yaml:"userdataDir"`
	SlakkrConfig string `yaml:"slakkrConfig"`
	DocentWMURL string `yaml:"docentWmUrl"` // local wm URL injected into dashboard
	Directives  []userdata.Directive `yaml:"directives,omitempty"`
}

func Load(path string) (DaemonConfig, error) {
	cfg := DaemonConfig{
		Port:       39787,
		RefreshSec: 60,
		UserdataDir: userdata.DefaultDir,
		DocentWMURL: "http://127.0.0.1:39788",
	}
	if path == "" {
		path = os.Getenv("DOCENT_CONFIG")
	}
	if path == "" {
		path = filepath.Join(userdata.DefaultDir, "docentd.yaml")
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
	if cfg.SlakkrConfig != "" {
		store := userdata.NewStore(filepath.Dir(cfg.SlakkrConfig))
		if cfg.UserdataDir != "" {
			store = userdata.NewStore(cfg.UserdataDir)
		}
		slakkr, err := store.LoadConfig()
		if err == nil && len(slakkr.Directives) > 0 {
			cfg.Directives = append(cfg.Directives, slakkr.Directives...)
		}
	} else if cfg.UserdataDir != "" {
		store := userdata.NewStore(cfg.UserdataDir)
		slakkr, err := store.LoadConfig()
		if err == nil && len(slakkr.Directives) > 0 {
			cfg.Directives = append(cfg.Directives, slakkr.Directives...)
		}
	}
	if token := os.Getenv("DOCENT_TOKEN"); token != "" {
		cfg.Token = token
	}
	return cfg, nil
}
