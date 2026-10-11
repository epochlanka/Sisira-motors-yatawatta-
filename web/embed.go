// Package web embeds HTML templates and static assets into the binary.
package web

import "embed"

//go:embed templates/*.html static
var FS embed.FS
