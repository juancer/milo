// Package admin embeds the built Astro admin UI.
//
// dist/ is produced by `make web-build`. Only a .gitkeep is committed, so the
// Go code always compiles and tests run without Node.
package admin

import "embed"

//go:embed all:dist
var Dist embed.FS
