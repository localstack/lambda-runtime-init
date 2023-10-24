package main

import (
	"io"
	"os"
	"path/filepath"
)

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
