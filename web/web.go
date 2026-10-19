// Package web serves the dashboard. It is a single static page that reads
// /api/stats, so it also works as a standalone demo when the data is inlined.
package web

import (
	"embed"
	"net/http"
)

//go:embed index.html
var files embed.FS

func Handler() http.Handler { return http.FileServer(http.FS(files)) }
