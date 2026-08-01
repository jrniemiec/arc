// Package docs embeds help documentation files for arc.
package docs

import "embed"

// FS contains all documentation files (*.md).
//
//go:embed *.md
var FS embed.FS
