package configschema

import (
	"bytes"
	"errors"
	"strings"
	"sync"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"
)

var (
	compiledSchema       *jsonschema.Schema
	compileOnce          sync.Once
	compileErr           error
	compiledDaemonSchema *jsonschema.Schema
	compileDaemonOnce    sync.Once
	compileDaemonErr     error
)

func mustCompiledSchema() (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		c := jsonschema.NewCompiler()
		if err := c.AddResource(SchemaURL, bytes.NewReader(SchemaBytes)); err != nil {
			compileErr = err
			return
		}
		compiledSchema, compileErr = c.Compile(SchemaURL)
	})
	return compiledSchema, compileErr
}

func mustCompiledDaemonSchema() (*jsonschema.Schema, error) {
	compileDaemonOnce.Do(func() {
		c := jsonschema.NewCompiler()
		if err := c.AddResource(DaemonSchemaURL, bytes.NewReader(DaemonSchemaBytes)); err != nil {
			compileDaemonErr = err
			return
		}
		compiledDaemonSchema, compileDaemonErr = c.Compile(DaemonSchemaURL)
	})
	return compiledDaemonSchema, compileDaemonErr
}

// ValidateYAML checks userdata config YAML against the embedded JSON Schema.
func ValidateYAML(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	sch, err := mustCompiledSchema()
	if err != nil {
		return err
	}
	if err := sch.Validate(doc); err != nil {
		return err
	}
	return nil
}

// ValidateDaemonYAML checks docentd.yaml against the embedded daemon JSON Schema.
func ValidateDaemonYAML(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	sch, err := mustCompiledDaemonSchema()
	if err != nil {
		return err
	}
	if err := sch.Validate(doc); err != nil {
		return err
	}
	return nil
}

// ValidationProblems turns jsonschema validation output into short strings (for userdata.ValidationError).
func ValidationProblems(err error) []string {
	if err == nil {
		return nil
	}
	var ve *jsonschema.ValidationError
	if errors.As(err, &ve) {
		return flattenValidationError(ve)
	}
	return []string{err.Error()}
}

func flattenValidationError(ve *jsonschema.ValidationError) []string {
	var out []string
	var walk func(*jsonschema.ValidationError, string)
	walk = func(n *jsonschema.ValidationError, prefix string) {
		if n == nil {
			return
		}
		loc := n.InstanceLocation
		if loc == "" {
			loc = prefix
		}
		msg := strings.TrimSpace(n.Message)
		if msg != "" && len(n.Causes) == 0 {
			line := msg
			if loc != "" && loc != "/" {
				line = loc + ": " + msg
			}
			out = append(out, line)
		}
		for _, c := range n.Causes {
			walk(c, loc)
		}
	}
	walk(ve, "")
	if len(out) == 0 && strings.TrimSpace(ve.Message) != "" {
		return []string{ve.Message}
	}
	return dedupeStrings(out)
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
