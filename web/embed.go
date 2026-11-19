// Package web holds the browser UI. The files are compiled into the server
// binary, so deploying is still "copy one file" (D-06).
package web

import "embed"

//go:embed index.html style.css app.js
var Files embed.FS
