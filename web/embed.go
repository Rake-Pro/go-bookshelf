// Package web carries the built frontend, embedded into the binary so the
// server ships as a single file.
package web

import "embed"

// Dist is the built single-page application: index.html, app modules, icons
// and any vendored assets.
//
//go:embed all:dist
var Dist embed.FS
