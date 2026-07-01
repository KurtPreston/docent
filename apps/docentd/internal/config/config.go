package config

import (
	"os"
	"path/filepath"

	"github.com/KurtPreston/docent/libs/config/configschema"
	"github.com/KurtPreston/docent/libs/config/docentconfig"
	"github.com/KurtPreston/docent/libs/config/executionmode"
	"github.com/KurtPreston/docent/libs/config/userdata"
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
	BindHost       string `yaml:"bindHost"`               // listen interface; default 0.0.0.0 when token set, else 127.0.0.1
	UserdataDir    string `yaml:"userdataDir,omitempty"`  // deprecated alias for configDir
	ExtraConfig    string `yaml:"extraConfig,omitempty"`  // optional extra config file merged in
	DocentWMURL    string `yaml:"docentWmUrl"`            // local wm URL injected into dashboard
	OnClickScript  string `yaml:"onClickScript"`          // hook run when a work-item is launched from the dashboard
	SSHHost        string `yaml:"sshHost"`                // optional ssh alias for remote editor open (DOCENT_HOST)
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
	if v := os.Getenv("DOCENT_ONCLICK"); v != "" {
		cfg.OnClickScript = v
	}
	cfg.OnClickScript = resolveOnClickScript(cfg)
	return cfg, nil
}

// resolveOnClickScript returns the onclick hook path, defaulting to
// ~/.config/docent/onclick.sh when unset.
func resolveOnClickScript(cfg DaemonConfig) string {
	if cfg.OnClickScript != "" {
		return cfg.OnClickScript
	}
	return filepath.Join(docentconfig.DefaultDir(), "onclick.sh")
}

// ResolveBindHost picks the listen interface. Precedence: an explicit -host
// flag, then docentd.yaml's bindHost, then a token-gated default: when a shared
// secret is configured docentd binds all interfaces (so it is reachable off the
// loopback) — otherwise it stays loopback-only. Binding externally is only safe
// because the data endpoints require the token (see server.requireAuth).
func ResolveBindHost(cfg DaemonConfig, flagHost string) string {
	if flagHost != "" {
		return flagHost
	}
	if cfg.BindHost != "" {
		return cfg.BindHost
	}
	if cfg.Token != "" {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

// IsLoopbackHost reports whether host is a loopback bind address.
func IsLoopbackHost(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return false
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
	if cfg.ExtraConfig != "" {
		extra, err := loadConfigFile(cfg.ExtraConfig)
		if err != nil {
			return err
		}
		cfg.Directives = append(cfg.Directives, extra.Directives...)
		if cfg.AI.Provider == "" {
			cfg.AI = extra.AI
		}
		if len(cfg.ExecutionModes) == 0 {
			cfg.ExecutionModes = extra.ExecutionModes
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
