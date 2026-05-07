package setup

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/kurt/slakkr-ai/internal/configschema"
	"github.com/kurt/slakkr-ai/internal/userdata"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// SchemaYAMLHint relative path from userdata/ for editors (yaml-language-server).
const SchemaYAMLHint = "../jsonschema/config.schema.json"

// Options configures the interactive setup wizard.
type Options struct {
	UserdataDir string
	ConfigPath  string // default: <UserdataDir>/config.yaml
	Stdout      *os.File
	Stderr      *os.File
	Stdin       *os.File
}

func (o *Options) userdataRoot() string {
	if strings.TrimSpace(o.UserdataDir) != "" {
		return o.UserdataDir
	}
	return userdata.DefaultDir
}

func (o *Options) configPath() string {
	if strings.TrimSpace(o.ConfigPath) != "" {
		return o.ConfigPath
	}
	return filepath.Join(o.userdataRoot(), "config.yaml")
}

func (o *Options) surveyOpts() survey.AskOpt {
	in := ioReader(o.Stdin)
	out := ioWriter(o.Stdout)
	errOut := ioWriter(o.Stderr)
	return survey.WithStdio(in, out, errOut)
}

func ioReader(f *os.File) *os.File {
	if f != nil {
		return f
	}
	return os.Stdin
}

func ioWriter(f *os.File) *os.File {
	if f != nil {
		return f
	}
	return os.Stdout
}

// Run executes the setup wizard.
func Run(opts Options) error {
	root := opts.userdataRoot()
	cfgPath := opts.configPath()
	surveyOpt := opts.surveyOpts()

	if err := os.MkdirAll(filepath.Join(root, "output"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, ".cache"), 0o755); err != nil {
		return err
	}

	model, err := configschema.WizardModel()
	if err != nil {
		return err
	}

	cfg, err := loadOrEmpty(cfgPath)
	if err != nil {
		return err
	}

	if err := pickAI(&cfg, model, surveyOpt); err != nil {
		return err
	}

	errOut := opts.Stderr
	if errOut == nil {
		errOut = os.Stderr
	}
	if err := manageCollectors(&cfg, model, surveyOpt, errOut); err != nil {
		return err
	}

	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	full := prependYAMLSchemaHint(SchemaYAMLHint, raw)
	if err := configschema.ValidateYAML(full); err != nil {
		problems := strings.Join(configschema.ValidationProblems(err), "\n")
		return fmt.Errorf("generated config failed validation: %w\n%s", err, problems)
	}

	envKeys := credentialEnvKeys(cfg)
	missing, err := reconcileDotEnv(filepath.Join(root, ".env"), envKeys)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath, full, 0o644); err != nil {
		return err
	}

	fmt.Fprintf(errOut, "\nWrote %s\n", cfgPath)
	if len(missing) > 0 {
		fmt.Fprintf(errOut, "\nPopulate these variables in userdata/.env (or your environment):\n")
		for _, k := range missing {
			fmt.Fprintf(errOut, "  - %s\n", k)
		}
	}
	return nil
}

func loadOrEmpty(path string) (userdata.ConfigFile, error) {
	var cfg userdata.ConfigFile
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.AI = userdata.AIConfig{Provider: "rule-based"}
			return cfg, nil
		}
		return cfg, err
	}
	b = stripYAMLComments(b)
	if len(strings.TrimSpace(string(b))) == 0 {
		cfg.AI = userdata.AIConfig{Provider: "rule-based"}
		return cfg, nil
	}
	if err := configschema.ValidateYAML(b); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func stripYAMLComments(b []byte) []byte {
	s := string(b)
	for strings.HasPrefix(strings.TrimSpace(s), "#") {
		s = strings.TrimSpace(s)
		idx := strings.IndexByte(s, '\n')
		if idx < 0 {
			return nil
		}
		s = strings.TrimSpace(s[idx+1:])
	}
	return []byte(strings.TrimSpace(s))
}

func prependYAMLSchemaHint(relativeSchemaPath string, body []byte) []byte {
	h := fmt.Sprintf("# yaml-language-server: $schema=%s\n\n", relativeSchemaPath)
	return append([]byte(h), body...)
}

func pickAI(cfg *userdata.ConfigFile, model configschema.Model, surveyOpt survey.AskOpt) error {
	fmt.Fprintf(os.Stderr, "\n=== AI provider ===\n")
	var sel string
	prompt := &survey.Select{
		Message: "Which AI provider?",
		Options: []string{"cursor", "ollama", "none (offline rule-based)"},
		Default: providerDefaultLabel(cfg.AI.Provider),
	}
	if err := survey.AskOne(prompt, &sel, surveyOpt); err != nil {
		return err
	}
	switch {
	case strings.HasPrefix(sel, "cursor"):
		cfg.AI.Provider = "cursor"
		cfg.AI.Ollama = userdata.AIProviderOllama{}
		if err := fillAINested(cfg, model, "cursor", surveyOpt); err != nil {
			return err
		}
	case strings.HasPrefix(sel, "ollama"):
		cfg.AI.Provider = "ollama"
		cfg.AI.Cursor = userdata.AIProviderCursor{}
		if err := fillAINested(cfg, model, "ollama", surveyOpt); err != nil {
			return err
		}
	default:
		cfg.AI.Provider = "rule-based"
		cfg.AI.Ollama = userdata.AIProviderOllama{}
		cfg.AI.Cursor = userdata.AIProviderCursor{}
	}
	return pickActivityFormatter(cfg, model, surveyOpt)
}

func pickActivityFormatter(cfg *userdata.ConfigFile, model configschema.Model, surveyOpt survey.AskOpt) error {
	var branch *configschema.AIProviderBranch
	for i := range model.AIProviders {
		if model.AIProviders[i].Provider == cfg.AI.Provider {
			branch = &model.AIProviders[i]
			break
		}
	}
	if branch == nil {
		return nil
	}
	for _, tf := range branch.TopLevelFields {
		if tf.Key != "activity_formatter" || len(tf.Enum) == 0 {
			continue
		}
		def := activityFormatterSurveyDefault(tf, cfg.AI.ActivityFormatter)
		if tf.SkipSetupPrompt {
			cfg.AI.ActivityFormatter = def
			continue
		}
		var choice string
		p := &survey.Select{
			Message: tf.Prompt,
			Options: tf.Enum,
			Default: def,
		}
		if err := survey.AskOne(p, &choice, surveyOpt); err != nil {
			return err
		}
		cfg.AI.ActivityFormatter = choice
	}
	return nil
}

func activityFormatterSurveyDefault(tf configschema.AIField, cfgVal string) string {
	v := strings.TrimSpace(strings.ToLower(strings.ReplaceAll(cfgVal, "_", "-")))
	for _, opt := range tf.Enum {
		if opt == v {
			return opt
		}
	}
	d := strings.TrimSpace(tf.Default)
	for _, opt := range tf.Enum {
		if opt == d {
			return opt
		}
	}
	return tf.Enum[0]
}

func providerDefaultLabel(p string) string {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "_", "-")) {
	case "cursor":
		return "cursor"
	case "ollama":
		return "ollama"
	default:
		return "none (offline rule-based)"
	}
}

func fillAINested(cfg *userdata.ConfigFile, model configschema.Model, nestedKey string, surveyOpt survey.AskOpt) error {
	var branch *configschema.AIProviderBranch
	for i := range model.AIProviders {
		if model.AIProviders[i].NestedKey == nestedKey {
			branch = &model.AIProviders[i]
			break
		}
	}
	if branch == nil {
		return nil
	}
	for _, f := range branch.Fields {
		if f.IsArgs {
			def := ""
			if nestedKey == "cursor" && cfg.AI.Cursor.Args != nil {
				def = strings.Join(cfg.AI.Cursor.Args, ",")
			}
			var line string
			p := &survey.Input{Message: f.Prompt, Default: def}
			if err := survey.AskOne(p, &line, surveyOpt); err != nil {
				return err
			}
			line = strings.TrimSpace(line)
			if line != "" {
				var parts []string
				for _, p := range strings.Split(line, ",") {
					p = strings.TrimSpace(p)
					if p != "" {
						parts = append(parts, p)
					}
				}
				cfg.AI.Cursor.Args = parts
			}
			continue
		}
		def := f.Default
		switch nestedKey {
		case "ollama":
			if f.Key == "base_url" && cfg.AI.Ollama.BaseURL != "" {
				def = cfg.AI.Ollama.BaseURL
			}
			if f.Key == "model" && cfg.AI.Ollama.Model != "" {
				def = cfg.AI.Ollama.Model
			}
		case "cursor":
			if f.Key == "command" && cfg.AI.Cursor.Command != "" {
				def = cfg.AI.Cursor.Command
			}
		}
		var val string
		in := &survey.Input{Message: f.Prompt, Default: def}
		for {
			if err := survey.AskOne(in, &val, surveyOpt); err != nil {
				return err
			}
			val = strings.TrimSpace(val)
			if val == "" && def != "" {
				val = def
			}
			if err := validateFieldValue(val, f.Validator); err != nil {
				fmt.Fprintf(os.Stderr, "%v — try again.\n", err)
				continue
			}
			break
		}
		switch nestedKey {
		case "ollama":
			switch f.Key {
			case "base_url":
				cfg.AI.Ollama.BaseURL = val
			case "model":
				cfg.AI.Ollama.Model = val
			}
		case "cursor":
			if f.Key == "command" {
				cfg.AI.Cursor.Command = val
			}
		}
	}
	return nil
}

func manageCollectors(cfg *userdata.ConfigFile, model configschema.Model, surveyOpt survey.AskOpt, errOut *os.File) error {
	fmt.Fprintf(errOut, "\n=== Collectors ===\n")

	options := make([]string, 0, len(model.Collectors))
	labelToBranch := make(map[string]configschema.CollectorBranch, len(model.Collectors))
	var defaults []string
	for _, br := range model.Collectors {
		label := collectorLabel(br)
		options = append(options, label)
		labelToBranch[label] = br
		if hasEnabledDirective(cfg, br.Collector) {
			defaults = append(defaults, label)
		}
	}

	var chosen []string
	prompt := &survey.MultiSelect{
		Message: "Collectors to enable (space to toggle, enter to confirm)",
		Options: options,
		Default: defaults,
	}
	if err := survey.AskOne(prompt, &chosen, surveyOpt); err != nil {
		return err
	}

	chosenSet := make(map[string]bool, len(chosen))
	for _, l := range chosen {
		if br, ok := labelToBranch[l]; ok {
			chosenSet[br.Collector] = true
		}
	}

	for i := range cfg.Directives {
		if !chosenSet[cfg.Directives[i].Collector] {
			cfg.Directives[i].Enabled = false
		}
	}

	for _, br := range model.Collectors {
		if !chosenSet[br.Collector] {
			continue
		}
		ix := indicesForCollector(cfg, br.Collector)
		if len(ix) == 0 {
			fmt.Fprintf(errOut, "\n— %s (%s) —\n", br.DisplayName, br.Collector)
			d := userdata.Directive{
				Collector:      br.Collector,
				Enabled:        true,
				Target:         map[string]string{},
				Config:         map[string]string{},
				CredentialRefs: map[string]string{},
			}
			if err := promptDirectiveIdentity(&d, br, model, surveyOpt); err != nil {
				return err
			}
			if err := promptDirectiveFields(&d, br, surveyOpt); err != nil {
				return err
			}
			cfg.Directives = append(cfg.Directives, d)
			continue
		}
		for _, i := range ix {
			cfg.Directives[i].Enabled = true
		}
	}

	return reconfigureExistingDirectives(cfg, model, surveyOpt, errOut)
}

func collectorLabel(br configschema.CollectorBranch) string {
	if br.DisplayName == "" || br.DisplayName == br.Collector {
		return br.Collector
	}
	return fmt.Sprintf("%s (%s)", br.DisplayName, br.Collector)
}

func hasEnabledDirective(cfg *userdata.ConfigFile, collector string) bool {
	for _, d := range cfg.Directives {
		if d.Collector == collector && d.Enabled {
			return true
		}
	}
	return false
}

func reconfigureExistingDirectives(cfg *userdata.ConfigFile, model configschema.Model, surveyOpt survey.AskOpt, errOut *os.File) error {
	type item struct {
		idx   int
		label string
	}
	var items []item
	for i := range cfg.Directives {
		d := &cfg.Directives[i]
		if !d.Enabled {
			continue
		}
		label := fmt.Sprintf("%s — %s (%s)", d.ID, d.Name, d.Collector)
		items = append(items, item{idx: i, label: label})
	}
	if len(items) == 0 {
		return nil
	}

	options := make([]string, 0, len(items))
	for _, it := range items {
		options = append(options, it.label)
	}

	var chosen []string
	prompt := &survey.MultiSelect{
		Message: "Reconfigure any directives? (space to toggle, enter to skip)",
		Options: options,
	}
	if err := survey.AskOne(prompt, &chosen, surveyOpt); err != nil {
		return err
	}
	if len(chosen) == 0 {
		return nil
	}

	branchByCollector := make(map[string]configschema.CollectorBranch, len(model.Collectors))
	for _, br := range model.Collectors {
		branchByCollector[br.Collector] = br
	}

	chosenSet := make(map[string]bool, len(chosen))
	for _, l := range chosen {
		chosenSet[l] = true
	}

	for _, it := range items {
		if !chosenSet[it.label] {
			continue
		}
		d := &cfg.Directives[it.idx]
		br, ok := branchByCollector[d.Collector]
		if !ok {
			continue
		}
		fmt.Fprintf(errOut, "\n— %s (%s) —\n", d.Name, d.ID)
		if err := promptDirectiveFields(d, br, surveyOpt); err != nil {
			return err
		}
	}
	return nil
}

func indicesForCollector(cfg *userdata.ConfigFile, collector string) []int {
	var ix []int
	for i := range cfg.Directives {
		if cfg.Directives[i].Collector == collector {
			ix = append(ix, i)
		}
	}
	return ix
}

func promptDirectiveIdentity(d *userdata.Directive, br configschema.CollectorBranch, model configschema.Model, surveyOpt survey.AskOpt) error {
	idDef := br.DefaultID
	if strings.TrimSpace(d.ID) != "" {
		idDef = d.ID
	}
	if model.SkipDirectiveIDSetupPrompt {
		d.ID = strings.TrimSpace(idDef)
	} else {
		var id string
		if err := survey.AskOne(&survey.Input{Message: "Directive id (lowercase, digits, hyphens)", Default: idDef}, &id, surveyOpt); err != nil {
			return err
		}
		d.ID = strings.TrimSpace(id)
	}

	nameDef := br.DisplayName
	if strings.TrimSpace(d.Name) != "" {
		nameDef = d.Name
	}
	if model.SkipDirectiveNameSetupPrompt {
		d.Name = strings.TrimSpace(nameDef)
	} else {
		var name string
		if err := survey.AskOne(&survey.Input{Message: "Short label / name", Default: nameDef}, &name, surveyOpt); err != nil {
			return err
		}
		d.Name = strings.TrimSpace(name)
	}
	return nil
}

func promptDirectiveFields(d *userdata.Directive, br configschema.CollectorBranch, surveyOpt survey.AskOpt) error {
	if br.Collector == "local-git" {
		mode := "code_home"
		if len(d.Paths) > 0 {
			mode = "paths"
		}
		defSel := "code_home (scan immediate children for .git)"
		if mode == "paths" {
			defSel = "paths (list repo directories)"
		}
		var sel string
		if err := survey.AskOne(&survey.Select{
			Message: "Local git: use scanned directory or explicit repo paths?",
			Options: []string{"code_home (scan immediate children for .git)", "paths (list repo directories)", "keep current / skip"},
			Default: defSel,
		}, &sel, surveyOpt); err != nil {
			return err
		}
		if strings.HasPrefix(sel, "code_home") {
			d.Paths = nil
			ch := d.CodeHome
			var v string
			for {
				if err := survey.AskOne(&survey.Input{Message: "Directory to scan (code_home)", Default: ch}, &v, surveyOpt); err != nil {
					return err
				}
				v = strings.TrimSpace(v)
				if err := validateFieldValue(v, "dir-exists"); err != nil {
					fmt.Fprintf(os.Stderr, "%v\n", err)
					continue
				}
				d.CodeHome = expandHome(v)
				break
			}
		} else if strings.HasPrefix(sel, "paths") {
			d.CodeHome = ""
			def := strings.Join(d.Paths, "\n")
			var block string
			if err := survey.AskOne(&survey.Multiline{Message: "One repo path per line"}, &block, surveyOpt); err != nil {
				return err
			}
			if strings.TrimSpace(block) == "" {
				block = def
			}
			var paths []string
			for _, line := range strings.Split(block, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				line = expandHome(line)
				if err := validateFieldValue(line, "dir-exists"); err != nil {
					return fmt.Errorf("path %q: %w", line, err)
				}
				paths = append(paths, line)
			}
			if len(paths) == 0 {
				return fmt.Errorf("at least one path is required for paths mode")
			}
			d.Paths = paths
		}
	}

	for _, f := range br.Fields {
		if f.IsPaths || (f.Section == configschema.SectionTop && f.Key == "code_home") {
			continue
		}
		cur := currentFieldValue(d, f)
		def := f.Default
		if cur != "" {
			def = cur
		}
		if f.Secret {
			msg := f.Prompt + " (environment variable name)"
			var envName string
			in := &survey.Input{Message: msg, Default: def}
			for {
				if err := survey.AskOne(in, &envName, surveyOpt); err != nil {
					return err
				}
				envName = strings.TrimSpace(envName)
				if envName == "" && !f.Required {
					clearCredential(d, f.Key)
					break
				}
				if envName == "" && f.Required {
					fmt.Fprintf(os.Stderr, "This field is required.\n")
					continue
				}
				setCredential(d, f.Key, envName)
				break
			}
			continue
		}

		var val string
		in := &survey.Input{Message: f.Prompt, Default: def}
		for {
			if err := survey.AskOne(in, &val, surveyOpt); err != nil {
				return err
			}
			val = strings.TrimSpace(val)
			if val == "" && def != "" {
				val = def
			}
			if val == "" && !f.Required {
				clearNonSecret(d, f)
				break
			}
			if val == "" && f.Required {
				fmt.Fprintf(os.Stderr, "This field is required.\n")
				continue
			}
			if err := validateFieldValue(val, f.Validator); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				continue
			}
			setNonSecret(d, f, val)
			break
		}
	}
	return nil
}

func currentFieldValue(d *userdata.Directive, f configschema.Field) string {
	switch f.Section {
	case configschema.SectionTarget:
		return d.Target[f.Key]
	case configschema.SectionConfig:
		return d.Config[f.Key]
	case configschema.SectionCredentialRefs:
		return d.CredentialRefs[f.Key]
	default:
		return ""
	}
}

func setNonSecret(d *userdata.Directive, f configschema.Field, val string) {
	switch f.Section {
	case configschema.SectionTarget:
		if d.Target == nil {
			d.Target = map[string]string{}
		}
		d.Target[f.Key] = val
	case configschema.SectionConfig:
		if d.Config == nil {
			d.Config = map[string]string{}
		}
		d.Config[f.Key] = val
	}
}

func clearNonSecret(d *userdata.Directive, f configschema.Field) {
	switch f.Section {
	case configschema.SectionTarget:
		delete(d.Target, f.Key)
	case configschema.SectionConfig:
		delete(d.Config, f.Key)
	}
}

func setCredential(d *userdata.Directive, key, envVar string) {
	if d.CredentialRefs == nil {
		d.CredentialRefs = map[string]string{}
	}
	d.CredentialRefs[key] = envVar
}

func clearCredential(d *userdata.Directive, key string) {
	if d.CredentialRefs != nil {
		delete(d.CredentialRefs, key)
	}
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

func validateFieldValue(val, validator string) error {
	switch validator {
	case "":
		return nil
	case "non-empty":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("must not be empty")
		}
	case "url":
		if strings.TrimSpace(val) == "" {
			return nil
		}
		u, err := url.Parse(strings.TrimSpace(val))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("invalid URL")
		}
	case "dir-exists":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("path is empty")
		}
		st, err := os.Stat(strings.TrimSpace(val))
		if err != nil {
			return fmt.Errorf("path not accessible: %w", err)
		}
		if !st.IsDir() {
			return fmt.Errorf("not a directory")
		}
	default:
		return nil
	}
	return nil
}

func credentialEnvKeys(cfg userdata.ConfigFile) []string {
	seen := map[string]bool{}
	var keys []string
	for _, d := range cfg.Directives {
		for _, v := range d.CredentialRefs {
			v = strings.TrimSpace(v)
			if v != "" && !seen[v] {
				seen[v] = true
				keys = append(keys, v)
			}
		}
	}
	return keys
}

// reconcileDotEnv ensures each key exists as KEY= in path; returns keys still needing a non-empty value.
func reconcileDotEnv(path string, keys []string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	values := map[string]string{}
	order := []string{}
	var preamble strings.Builder

	consume := func(line string) {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			preamble.WriteString(line)
			preamble.WriteByte('\n')
			return
		}
		k, rest, ok := strings.Cut(trim, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			preamble.WriteString(line)
			preamble.WriteByte('\n')
			return
		}
		if _, exists := values[k]; !exists {
			order = append(order, k)
		}
		values[k] = strings.TrimSpace(rest)
	}

	remaining := string(data)
	for remaining != "" {
		idx := strings.IndexByte(remaining, '\n')
		var line string
		if idx < 0 {
			line = remaining
			remaining = ""
		} else {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		}
		consume(line)
	}

	for _, k := range keys {
		if _, ok := values[k]; !ok {
			values[k] = ""
			order = append(order, k)
		}
	}

	var out strings.Builder
	out.WriteString(preamble.String())
	for _, k := range order {
		fmt.Fprintf(&out, "%s=%s\n", k, values[k])
	}

	var missing []string
	for _, k := range keys {
		if strings.TrimSpace(values[k]) == "" {
			missing = append(missing, k)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, []byte(out.String()), 0o600); err != nil {
		return nil, err
	}
	return missing, nil
}

// StdoutIsTerminal reports whether f is a tty (for callers).
func StdoutIsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
