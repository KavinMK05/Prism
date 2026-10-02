package main

import "embed"

//go:embed icon.png
//go:embed all:web/dist
var adminFS embed.FS
