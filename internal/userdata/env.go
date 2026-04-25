package userdata

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ResolveEnv returns os.Getenv(key), or the value for key from userdataRoot/.env if set there.
func ResolveEnv(userdataRoot, key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	if userdataRoot == "" || key == "" {
		return ""
	}
	path := filepath.Join(userdataRoot, ".env")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		keyPart, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(keyPart) != key {
			continue
		}
		val = strings.TrimSpace(val)
		if unq, err := strconv.Unquote(val); err == nil {
			val = unq
		}
		return val
	}
	return ""
}
