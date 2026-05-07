package configschema

// Model is derived from config.schema.json for the setup wizard (annotations only).
type Model struct {
	AIProviders                  []AIProviderBranch
	Collectors                   []CollectorBranch
	SkipDirectiveIDSetupPrompt   bool
	SkipDirectiveNameSetupPrompt bool
}

// AIProviderBranch describes one ai.provider option (rule-based / ollama / cursor).
type AIProviderBranch struct {
	Provider       string
	NestedKey      string    // "", "ollama", or "cursor"
	Fields         []AIField // leaves under ai.ollama or ai.cursor
	TopLevelFields []AIField // enum-like keys on ai next to provider (e.g. activity_formatter)
}

// AIField is a wizard prompt leaf (nested under ai.ollama/ai.cursor, or ai root for enums).
type AIField struct {
	Key             string
	Prompt          string
	Default         string
	Validator       string
	IsArgs          bool // cursor.args — optional comma-separated in wizard
	Enum            []string
	SkipSetupPrompt bool // setup uses default / existing value without prompting
}

// CollectorBranch describes one collector type from the directive oneOf.
type CollectorBranch struct {
	Collector   string
	DisplayName string
	DefaultID   string
	Fields      []Field
}

// FieldSection groups directive keys for the wizard.
type FieldSection string

const (
	SectionTop            FieldSection = ""
	SectionTarget         FieldSection = "target"
	SectionConfig         FieldSection = "config"
	SectionCredentialRefs FieldSection = "credential_refs"
	SectionPaths          FieldSection = "paths"
)

// Field is one wizard prompt for a directive leaf value.
type Field struct {
	Section   FieldSection
	Key       string // map key within Section; empty for paths list
	Prompt    string
	Default   string
	Secret    bool   // env var name only; never store secret in YAML value
	Validator string // url, dir-exists, non-empty
	IsPaths   bool   // []string paths on directive root
	Required  bool   // from schema required array for this object
}
