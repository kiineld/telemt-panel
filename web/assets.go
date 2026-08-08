// Package webassets embeds the panel's server-rendered templates and
// static front-end assets (CSS, vendored htmx/Alpine) into the binary.
//
// This lives at the repository root, rather than inside internal/web where
// it is consumed, because Go's //go:embed patterns cannot cross a parent
// directory boundary ("..") — embedding must happen in a file that sits
// alongside (or above) the files it embeds.
package webassets

import "embed"

// The all: prefix is required, not decorative: without it, Go's embed
// excludes any file or directory whose name starts with "_" or ".", which
// would silently drop web/templates/_rows.html.
//
//go:embed all:templates all:static
var FS embed.FS
