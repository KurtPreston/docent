package configschema

import _ "embed"

//go:embed config.schema.json
var SchemaBytes []byte

// SchemaURL matches $id in config.schema.json (used by the JSON Schema compiler).
const SchemaURL = "https://docent.local/config.schema.json"
