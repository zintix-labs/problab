package demo_configs

import (
	"embed"
)

// FS provides embedded default config YAMLs for external usage.
//
//go:embed *.yaml
var FS embed.FS
