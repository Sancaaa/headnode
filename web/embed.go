// Package web holds the browser UI. The files are compiled into the server
// binary, so deploying is still "copy one file" (D-06).
package web

import "embed"

// vendor holds third-party files we copy in and pin (xterm.js, D-06). It is
// embedded as a whole directory, which also means the build fails if the
// directory is missing: web/vendor/README.md is always there.
//
//go:embed index.html style.css app.js headnode-mark.svg vendor
var Files embed.FS
