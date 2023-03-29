package main

import (
	"archive/zip"
	"encoding/json"
	log "github.com/sirupsen/logrus"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
)

type ArchiveDownload struct {
	Url        string `json:"url"`
	TargetPath string `json:"target_path"`
}

func DownloadCodeArchives(archives string) error {
	if archives == "" {
		log.Debugln("No code archives set. Skipping download.")
		return nil
	}
	var parsedArchives []ArchiveDownload
	err := json.Unmarshal([]byte(archives), &parsedArchives)
	if err != nil {
		return err
	}

	for _, downloadArchive := range parsedArchives {
		if err := DownloadCodeArchive(downloadArchive.Url, downloadArchive.TargetPath); err != nil {
			return err
		}
	}
	return nil
}

func DownloadCodeArchive(url string, targetPath string) error {
	// download and unzip code archive
	log.Infoln("Downloading code archive")
	// create tmp directory
	// empty string will make use of the default tmp directory
	tmpDir, err := os.MkdirTemp("", "localstack-code-archive")
	defer os.RemoveAll(tmpDir)
	if err != nil {
		return err
	}
	// download code archive into tmp directory
	res, err := http.Get(url)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	tmp_file_path := path.Join(tmpDir, "code-archive.zip")
	tmp_file, err := os.OpenFile(tmp_file_path, os.O_WRONLY|os.O_CREATE, os.ModePerm)
	if err != nil {
		return err
	}
	_, err = io.Copy(tmp_file, res.Body)
	if err != nil {
		return err
	}
	err = tmp_file.Close()
	if err != nil {
		return err
	}
	// unzip into targetPath
	log.Infoln("Unzipping code archive")
	r, err := zip.OpenReader(tmp_file_path)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			return err
		}
		// TODO: check if exists, otherwise build path
		target_file_name := path.Join(targetPath, f.Name)
		if f.FileInfo().IsDir() {
			err = os.MkdirAll(target_file_name, os.ModePerm)
			if err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target_file_name), os.ModePerm); err != nil {
			panic(err)
		}
		target_file, err := os.OpenFile(target_file_name, os.O_WRONLY|os.O_CREATE, os.ModePerm)
		if err != nil {
			return err
		}
		_, err = io.Copy(target_file, rc)
		if err != nil {
			return err
		}
		target_file.Close()
		rc.Close()
	}
	return nil
}
