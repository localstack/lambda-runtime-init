package main

import (
	"encoding/json"
	log "github.com/sirupsen/logrus"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

type Chmod struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// AdaptFilesystemPermissions Adapts the file system permissions to the mode specified in the chmodInfoString parameter
// chmodInfoString should be a json encoded list of `Chmod` structs.
// example: '[{"path": "/opt", "mode": "0755"}]'. The mode string should be an octal representation of the targeted file mode.
func AdaptFilesystemPermissions(chmodInfoString string) error {
	var chmodInfo []Chmod
	err := json.Unmarshal([]byte(chmodInfoString), &chmodInfo)
	if err != nil {
		return err
	}
	for _, chmod := range chmodInfo {
		mode, err := strconv.ParseInt(chmod.Mode, 0, 32)
		if err != nil {
			return err
		}
		if err := ChmodRecursively(chmod.Path, os.FileMode(mode)); err != nil {
			log.Warnf("Could not change file mode recursively of directory %s: %s\n", chmod.Path, err)
		}
	}
	return nil
}

// Inspired by https://stackoverflow.com/questions/73864379/golang-change-permission-os-chmod-and-os-chowm-recursively
// but using the more efficient WalkDir API
func ChmodRecursively(root string, mode os.FileMode) error {
	return filepath.WalkDir(root,
		func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			err = os.Chmod(path, mode)
			if err != nil {
				return err
			}
			return nil
		})
}

// Check if a directory is empty
// Source: https://stackoverflow.com/questions/30697324/how-to-check-if-directory-on-path-is-empty/30708914#30708914
func IsDirEmpty(name string) (bool, error) {
	f, err := os.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = f.Readdirnames(1) // faster than f.Readdir(1)
	if err == io.EOF {
		return true, nil
	}
	return false, err // Either not empty or error, suits both cases
}
