// Package web embeds the built React frontend (web/dist) into the Go
// binary so the whole app ships as a single executable. Run
// `npm install && npm run build` in this directory to produce dist/
// before building the server for real use; a placeholder index.html
// ships so `go build` succeeds even before that step.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded frontend rooted at dist/, ready to serve
// directly over HTTP.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
