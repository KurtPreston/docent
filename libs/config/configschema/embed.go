package configschema

import _ "embed"

//go:embed config.schema.json
var SchemaBytes []byte

//go:embed docentd.schema.json
var DaemonSchemaBytes []byte

//go:embed goals.schema.json
var GoalsSchemaBytes []byte

// SchemaURL matches $id in config.schema.json (used by the JSON Schema compiler).
const SchemaURL = "https://docent.local/config.schema.json"

// DaemonSchemaURL matches $id in docentd.schema.json.
const DaemonSchemaURL = "https://docent.local/docentd.schema.json"

// GoalsSchemaURL matches $id in goals.schema.json.
const GoalsSchemaURL = "https://docent.local/goals.schema.json"
