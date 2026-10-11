package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed internal/assets/* extension/*
var assets embed.FS

func managementPage() []byte {
	raw, _ := assets.ReadFile("internal/assets/index.html")
	return raw
}

func extensionArchive() ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	errWalk := fs.WalkDir(assets, "extension", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, errRead := assets.ReadFile(path)
		if errRead != nil {
			return errRead
		}
		file, errCreate := archive.Create(strings.TrimPrefix(path, "extension/"))
		if errCreate != nil {
			return errCreate
		}
		_, errWrite := file.Write(data)
		return errWrite
	})
	if errWalk != nil {
		_ = archive.Close()
		return nil, fmt.Errorf("package Cookie extension: %w", errWalk)
	}
	if errClose := archive.Close(); errClose != nil {
		return nil, fmt.Errorf("close Cookie extension archive: %w", errClose)
	}
	return buffer.Bytes(), nil
}
