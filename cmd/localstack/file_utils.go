package main

import (
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
