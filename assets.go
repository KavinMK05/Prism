package main

import "embed"

//go:embed admin.html
//go:embed icon.png
//go:embed all:web/dist
var adminFS embed.FS
