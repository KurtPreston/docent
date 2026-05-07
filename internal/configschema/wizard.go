package configschema

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// WizardModel parses x-slakkr-* annotations from the embedded schema.
func WizardModel() (Model, error) {
	var root map[string]any
	if err := json.Unmarshal(SchemaBytes, &root); err != nil {
		return Model{}, err
	}
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		return Model{}, fmt.Errorf("schema: missing $defs")
	}
	aiProviders, err := parseAIProviders(defs["ai"])
	if err != nil {
		return Model{}, err
	}
	collectors, err := parseCollectorBranches(defs)
	if err != nil {
		return Model{}, err
	}
	skipID, skipName := parseDirectiveIdentitySetup(defs)
	return Model{
		AIProviders:                  aiProviders,
		Collectors:                   collectors,
		SkipDirectiveIDSetupPrompt:   skipID,
		SkipDirectiveNameSetupPrompt: skipName,
	}, nil
}

func parseAIProviders(aiDef any) ([]AIProviderBranch, error) {
	m, ok := aiDef.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema: ai is not an object")
	}
	branches, ok := m["oneOf"].([]any)
	if !ok {
		return nil, fmt.Errorf("schema: ai.oneOf missing")
	}
	var out []AIProviderBranch
	for _, b := range branches {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		prov := nestedMap(bm, "properties", "provider")
		if prov == nil {
			continue
		}
		pval, _ := prov["const"].(string)
		if pval == "" {
			continue
		}
		branch := AIProviderBranch{Provider: pval}
		switch pval {
		case "rule-based":
			// no nested fields
		case "ollama":
			branch.NestedKey = "ollama"
			if om := nestedMap(bm, "properties", "ollama"); om != nil {
				branch.Fields = append(branch.Fields, extractAIFields(om)...)
			}
		case "cursor":
			branch.NestedKey = "cursor"
			if om := nestedMap(bm, "properties", "cursor"); om != nil {
				branch.Fields = append(branch.Fields, extractAIFields(om)...)
			}
		}
		extractAITopLevelEnumFields(&branch, bm)
		out = append(out, branch)
	}
	return out, nil
}

// extractAITopLevelEnumFields parses string enum keys on ai (same level as provider), e.g. activity_formatter.
func extractAITopLevelEnumFields(branch *AIProviderBranch, bm map[string]any) {
	props, ok := bm["properties"].(map[string]any)
	if !ok {
		return
	}
	var keys []string
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		switch key {
		case "provider", "ollama", "cursor":
			continue
		}
		vm, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		enumRaw, ok := vm["enum"].([]any)
		if !ok {
			continue
		}
		var opts []string
		for _, v := range enumRaw {
			s, ok := v.(string)
			if ok && strings.TrimSpace(s) != "" {
				opts = append(opts, s)
			}
		}
		if len(opts) == 0 {
			continue
		}
		def := strings.TrimSpace(strAnnotation(vm, "x-slakkr-default", ""))
		if def == "" {
			def = opts[0]
		}
		branch.TopLevelFields = append(branch.TopLevelFields, AIField{
			Key:             key,
			Prompt:          strAnnotation(vm, "x-slakkr-prompt", key),
			Default:         def,
			Enum:            opts,
			SkipSetupPrompt: boolAnnotation(vm, "x-slakkr-setup-skip-prompt"),
		})
	}
}

func parseDirectiveIdentitySetup(defs map[string]any) (skipID, skipName bool) {
	base, ok := defs["directiveBase"].(map[string]any)
	if !ok {
		return false, false
	}
	props, ok := base["properties"].(map[string]any)
	if !ok {
		return false, false
	}
	if idm, ok := props["id"].(map[string]any); ok {
		skipID = boolAnnotation(idm, "x-slakkr-setup-skip-prompt")
	}
	if nm, ok := props["name"].(map[string]any); ok {
		skipName = boolAnnotation(nm, "x-slakkr-setup-skip-prompt")
	}
	return skipID, skipName
}

func extractAIFields(objSchema map[string]any) []AIField {
	props, ok := objSchema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	var keys []string
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var fields []AIField
	for _, key := range keys {
		vm, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := vm["type"].(string); typ == "array" {
			if key == "args" {
				fields = append(fields, AIField{
					Key:       key,
					Prompt:    strAnnotation(vm, "x-slakkr-prompt", "Extra cursor-agent args (optional, comma-separated)"),
					IsArgs:    true,
					Validator: strAnnotation(vm, "x-slakkr-validator", ""),
				})
			}
			continue
		}
		fields = append(fields, AIField{
			Key:       key,
			Prompt:    strAnnotation(vm, "x-slakkr-prompt", key),
			Default:   strAnnotation(vm, "x-slakkr-default", ""),
			Validator: strAnnotation(vm, "x-slakkr-validator", ""),
		})
	}
	return fields
}

func parseCollectorBranches(defs map[string]any) ([]CollectorBranch, error) {
	dirItem, ok := defs["directiveItem"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema: directiveItem missing")
	}
	allOf, ok := dirItem["allOf"].([]any)
	if !ok || len(allOf) < 2 {
		return nil, fmt.Errorf("schema: directiveItem.allOf")
	}
	disc, ok := allOf[1].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("schema: directive discriminator")
	}
	oneOf, ok := disc["oneOf"].([]any)
	if !ok {
		return nil, fmt.Errorf("schema: directiveItem discriminator oneOf")
	}
	var out []CollectorBranch
	for _, ref := range oneOf {
		rm, ok := ref.(map[string]any)
		if !ok {
			continue
		}
		refStr, _ := rm["$ref"].(string)
		name := strings.TrimPrefix(refStr, "#/$defs/")
		if name == "" {
			continue
		}
		def, ok := defs[name].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema: missing def %s", name)
		}
		cb, err := parseCollectorBranch(def)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out = append(out, cb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Collector < out[j].Collector })
	return out, nil
}

func parseCollectorBranch(def map[string]any) (CollectorBranch, error) {
	props, ok := def["properties"].(map[string]any)
	if !ok {
		return CollectorBranch{}, fmt.Errorf("properties missing")
	}
	collProp, ok := props["collector"].(map[string]any)
	if !ok {
		return CollectorBranch{}, fmt.Errorf("collector property missing")
	}
	collector, _ := collProp["const"].(string)
	if collector == "" {
		return CollectorBranch{}, fmt.Errorf("collector const missing")
	}
	cb := CollectorBranch{
		Collector:   collector,
		DefaultID:   strAnnotation(collProp, "x-slakkr-default-id", collector),
		DisplayName: strAnnotation(collProp, "x-slakkr-display-name", collector),
	}

	if ch, ok := props["code_home"].(map[string]any); ok {
		cb.Fields = append(cb.Fields, Field{
			Section:   SectionTop,
			Key:       "code_home",
			Prompt:    strAnnotation(ch, "x-slakkr-prompt", "code_home"),
			Default:   strAnnotation(ch, "x-slakkr-default", ""),
			Validator: strAnnotation(ch, "x-slakkr-validator", ""),
		})
	}
	if paths, ok := props["paths"].(map[string]any); ok {
		cb.Fields = append(cb.Fields, Field{
			Section:   SectionPaths,
			Key:       "",
			Prompt:    strAnnotation(paths, "x-slakkr-prompt", "paths"),
			IsPaths:   true,
			Validator: "dir-exists",
		})
	}

	if tgt, ok := props["target"].(map[string]any); ok {
		cb.Fields = append(cb.Fields, extractLeafFields(tgt, SectionTarget)...)
	}
	if cfg, ok := props["config"].(map[string]any); ok {
		cb.Fields = append(cb.Fields, extractLeafFields(cfg, SectionConfig)...)
	}
	if cr, ok := props["credential_refs"].(map[string]any); ok {
		if oo, ok := cr["oneOf"].([]any); ok {
			for _, branch := range oo {
				bm, ok := branch.(map[string]any)
				if !ok {
					continue
				}
				cb.Fields = append(cb.Fields, extractLeafFields(bm, SectionCredentialRefs)...)
			}
		} else {
			cb.Fields = append(cb.Fields, extractLeafFields(cr, SectionCredentialRefs)...)
		}
	}

	dedupeFields(&cb.Fields)
	return cb, nil
}

func extractLeafFields(objSchema map[string]any, section FieldSection) []Field {
	props, ok := objSchema["properties"].(map[string]any)
	if !ok {
		return nil
	}
	reqSet := map[string]bool{}
	if req, ok := objSchema["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				reqSet[s] = true
			}
		}
	}
	var keys []string
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var fields []Field
	for _, key := range keys {
		vm, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		if _, hasConst := vm["const"]; hasConst {
			continue
		}
		typ, _ := vm["type"].(string)
		if typ != "string" {
			continue
		}
		secret := false
		if b, ok := vm["x-slakkr-secret"].(bool); ok {
			secret = b
		}
		fields = append(fields, Field{
			Section:         section,
			Key:             key,
			Prompt:          strAnnotation(vm, "x-slakkr-prompt", key),
			Default:         strAnnotation(vm, "x-slakkr-default", ""),
			Secret:          secret,
			Validator:       strAnnotation(vm, "x-slakkr-validator", ""),
			Required:        reqSet[key],
			SkipSetupPrompt: boolAnnotation(vm, "x-slakkr-setup-skip-prompt"),
		})
	}
	return fields
}

func dedupeFields(fields *[]Field) {
	seen := map[string]bool{}
	var out []Field
	for _, f := range *fields {
		key := string(f.Section) + "\x00" + f.Key + fmt.Sprintf("\x00%v", f.IsPaths)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, f)
	}
	*fields = out
}

func nestedMap(m map[string]any, path ...string) map[string]any {
	cur := m
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return cur
}

func strAnnotation(vm map[string]any, key, fallback string) string {
	if v, ok := vm[key].(string); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func boolAnnotation(vm map[string]any, key string) bool {
	b, ok := vm[key].(bool)
	return ok && b
}
