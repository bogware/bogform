// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

// Package webui holds the dashboard's built web page, for "bogform ui". Release builds copy web/dist
// into assets/app before compiling. Development builds without it still work, and show a page that
// explains how to build it.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:assets
var assets embed.FS

// FS returns the built web page, or nil if this build doesn't include it.
func FS() fs.FS {
	app, err := fs.Sub(assets, "assets/app")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(app, "index.html"); err != nil {
		return nil
	}
	return app
}
