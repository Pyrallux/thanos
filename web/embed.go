// Package web holds the embedded static frontend assets served by the API server.
package web

import "embed"

// WebFS is the embedded filesystem containing the web UI static files.
// The //go:embed directive includes all files under static/.
//
//go:embed static/index.html static/style.css static/app.js static/thanos-icon.jpg
var WebFS embed.FS