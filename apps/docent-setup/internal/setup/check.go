package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/ai"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

// CheckOptions configures directive + AI validation.
type CheckOptions struct {
	ConfigDir  string
	ConfigPath string // default: <ConfigDir>/config.yaml
	Stdout     io.Writer
	Stderr     io.Writer
}

func (o *CheckOptions) configDir() string {
	if strings.TrimSpace(o.ConfigDir) != "" {
		return o.ConfigDir
	}
	return userdata.DefaultDir
}

func (o *CheckOptions) configPath() string {
	if strings.TrimSpace(o.ConfigPath) != "" {
		return o.ConfigPath
	}
	return filepath.Join(o.configDir(), "config.yaml")
}

// RunCheck validates every enabled directive and the configured AI provider.
func RunCheck(ctx context.Context, opts CheckOptions) error {
	out := opts.Stdout
	if out == nil {
		out = os.Stdout
	}
	cfgPath := opts.configPath()
	cfg, err := loadOrEmpty(cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("%s: %w", cfgPath, err)
	}
	if len(cfg.Directives) == 0 {
		return fmt.Errorf("no directives in %s: add a `directives:` list", cfgPath)
	}

	reg := collectors.NewRegistry(time.Now)
	validateOpts := &collectors.ValidateOpts{UserdataDir: opts.configDir()}
	issues := runValidation(ctx, reg, cfg, validateOpts)
	renderValidationIssues(out, issues)
	if len(issues) > 0 {
		return fmt.Errorf("%d validation issue(s)", len(issues))
	}
	fmt.Fprintln(out, "All enabled directives passed validation.")
	return nil
}

func runValidation(ctx context.Context, reg *collectors.Registry, cfg userdata.ConfigFile, opts *collectors.ValidateOpts) []collectors.ValidationIssue {
	aiCh := make(chan []collectors.ValidationIssue, 1)
	go func() {
		raw := ai.Validate(ctx, cfg.AI, nil)
		aiCh <- aiIssuesAsValidation(raw)
	}()
	directiveIssues := reg.Validate(ctx, cfg.Directives, opts)
	aiIssues := <-aiCh
	return append(aiIssues, directiveIssues...)
}

func aiIssuesAsValidation(issues []ai.Issue) []collectors.ValidationIssue {
	if len(issues) == 0 {
		return nil
	}
	out := make([]collectors.ValidationIssue, 0, len(issues))
	for _, iss := range issues {
		out = append(out, collectors.ValidationIssue{
			DirectiveID: "ai",
			Description: "AI provider",
			Collector:   "ai/" + iss.Provider,
			Field:       iss.Field,
			Message:     iss.Message,
			Remediation: iss.Remediation,
		})
	}
	return out
}

func renderValidationIssues(out io.Writer, issues []collectors.ValidationIssue) {
	if len(issues) == 0 {
		return
	}
	fmt.Fprintln(out, "Validation warnings:")
	for _, iss := range issues {
		label := strings.TrimSpace(iss.DirectiveID)
		if d := strings.TrimSpace(iss.Description); d != "" && d != label {
			label = fmt.Sprintf("%s (%s)", label, d)
		}
		if label == "" {
			label = iss.Collector
		}
		field := ""
		if strings.TrimSpace(iss.Field) != "" {
			field = fmt.Sprintf(" [%s]", iss.Field)
		}
		fmt.Fprintf(out, "  ! %s [%s]%s: %s\n", label, iss.Collector, field, iss.Message)
		if rem := strings.TrimSpace(iss.Remediation); rem != "" {
			fmt.Fprintf(out, "      -> %s\n", rem)
		}
	}
	fmt.Fprintln(out)
}
