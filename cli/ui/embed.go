package ui

import (
	"embed"
	"io/fs"
)

//go:embed out/**
var files embed.FS

func Static() (fs.FS, error) {
	return fs.Sub(files, "out")
}
