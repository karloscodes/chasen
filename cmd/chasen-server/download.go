package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// serveDownload answers GET /v1/download: the databases of one backup of an
// app, as a tar.gz file. This is how the owner takes the data away: to look
// at it, to keep a copy, or to leave. The caller checked the token.
func serveDownload(w http.ResponseWriter, r *http.Request) {
	args := r.URL.Query()["arg"]
	if len(args) == 0 || len(args) > 2 || checkAppName(args[0]) != nil {
		http.Error(w, "usage: /v1/download?arg=<app>[&arg=<backup>]", http.StatusBadRequest)
		return
	}
	name, stamp := args[0], ""
	if len(args) == 2 {
		stamp = args[1]
	}
	cfg, err := loadServerConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The backup can be offsite only: fetchBackup brings it to the server first.
	stamp, err = fetchBackup(name, stamp, cfg)
	if errors.Is(err, errNoBackups) {
		http.Error(w, fmt.Sprintf("%s has no backup yet. Make one: chasen backup", name), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s.tar.gz"`, name, stamp))
	if err := writeBackup(w, filepath.Join(backupsDir(name), stamp), stamp); err != nil {
		// The file is half sent. Break the connection, so the client does not
		// take a part of a backup for a backup.
		panic(http.ErrAbortHandler)
	}
}

// writeBackup writes the databases of a local backup as a tar.gz archive.
// The backup keeps each database compressed. The archive has them as they
// were, ready to open: data/db.sqlite3.
func writeBackup(out io.Writer, dir, stamp string) error {
	files, err := backupFiles(dir)
	if err != nil {
		return err
	}
	taken, _ := time.Parse(stampLayout, stamp)
	zip := gzip.NewWriter(out)
	archive := tar.NewWriter(zip)
	for _, file := range files {
		// A tar entry starts with its size, and the size of a database is
		// known only after it is unpacked. So read it two times: count, then copy.
		size, err := unpack(io.Discard, filepath.Join(dir, file))
		if err != nil {
			return err
		}
		header := &tar.Header{Name: strings.TrimSuffix(filepath.ToSlash(file), ".gz"), Mode: 0600, Size: size, ModTime: taken}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if _, err := unpack(archive, filepath.Join(dir, file)); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	return zip.Close()
}

// unpack writes a gzip file, unpacked, to out. It returns how many bytes it wrote.
func unpack(out io.Writer, path string) (int64, error) {
	in, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return 0, err
	}
	return io.Copy(out, zr)
}
