package main

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/karloscodes/chasen/protocol"
)

// A page of the version before a deploy asks for the files of its own
// version: application-4f3a2b.js, or a page that loads on the first click.
// The new image has other names, so those answer 404 until the visitor
// reloads. `asset_path:` in chasen.yml names the folder of those files in
// the image, like /rails/public/assets. The server mounts one folder of the
// host over it, and fills it before the swap with the files of the new image,
// next to the files of the version that runs. After the deploy it keeps the
// files of these two versions, and removes the others.
//
// The folder is shared, so the version before sees the new files for the
// seconds of the swap too. A file with the same name in both versions, like
// the manifest of Propshaft, is the one of the new version: each file is
// replaced in one rename, never written in place.
//
// The app mounts the folder, so it can put a link in it. Root writes there
// through an os.Root, which follows no link out of the folder.

// assetDir is the folder of the host that the app sees at its asset path, or
// "" for an app with none. The engine maps a mount by its last name, like the
// volumes: /rails/public/assets is /var/matcha/<app>/assets.
func assetDir(name string) string {
	settings, err := loadSettings(name)
	if err != nil || settings.AssetPath == "" {
		return ""
	}
	return filepath.Join(appDir(name), path.Base(settings.AssetPath))
}

// mounts are the paths of the container that the engine mounts from the host:
// the volumes, then the asset path.
func mounts(sh protocol.Shape, settings protocol.Settings) []string {
	if settings.AssetPath == "" {
		return sh.Volumes
	}
	return append(slices.Clone(sh.Volumes), settings.AssetPath)
}

// syncAssets copies the files at the asset path of the image into the asset
// folder of the app, and returns their names.
func syncAssets(name, image, assetPath string) ([]string, error) {
	dir := filepath.Join(appDir(name), path.Base(assetPath))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return imageAssets(image, assetPath, root)
}

// pruneAssets removes from the asset folder what none of the images has at
// the asset path. An image it cannot read keeps the folder as it is.
func pruneAssets(name, assetPath string, images ...string) {
	keep := map[string]bool{}
	for _, image := range images {
		if image == "" {
			continue
		}
		files, err := imageAssets(image, assetPath, nil)
		if err != nil {
			fmt.Printf("Warning: the old assets stay: %v\n", err)
			return
		}
		for _, f := range files {
			keep[f] = true
		}
	}
	root, err := os.OpenRoot(filepath.Join(appDir(name), path.Base(assetPath)))
	if err != nil {
		return
	}
	defer root.Close()
	var gone []string
	fs.WalkDir(root.FS(), ".", func(file string, d fs.DirEntry, err error) error {
		if err == nil && file != "." && !keep[file] {
			gone = append(gone, file)
		}
		return nil
	})
	// The deepest first, so a folder is empty when its turn comes. A folder
	// that still holds a kept file stays.
	slices.Reverse(gone)
	for _, file := range gone {
		root.Remove(file)
	}
}

// imageAssets returns the names of the files and folders at the asset path of
// the image, relative to it. With a root, it also writes them there. It
// takes folders and regular files only: a link in an image is not an asset.
func imageAssets(image, assetPath string, root *os.Root) ([]string, error) {
	id, err := docker("create", "--entrypoint", "none", image)
	if err != nil {
		return nil, errors.New(id)
	}
	defer docker("rm", "-f", id)

	cp := exec.Command("docker", "cp", id+":"+assetPath, "-")
	out, err := cp.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cp.Stderr = &stderr
	if err := cp.Start(); err != nil {
		return nil, err
	}
	names, readErr := readAssets(out, root)
	io.Copy(io.Discard, out)
	if err := cp.Wait(); err != nil {
		return nil, fmt.Errorf("the image has no folder %s, the asset_path of chasen.yml: %s", assetPath, strings.TrimSpace(stderr.String()))
	}
	return names, readErr
}

// readAssets reads the archive of docker cp, whose names start with the last
// name of the asset path.
func readAssets(archive io.Reader, root *os.Root) ([]string, error) {
	var names []string
	tr := tar.NewReader(archive)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names, nil
		}
		if err != nil {
			return names, err
		}
		_, name, _ := strings.Cut(strings.TrimSuffix(h.Name, "/"), "/")
		if name == "" || !filepath.IsLocal(name) {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if root != nil {
				if err := root.MkdirAll(name, 0755); err != nil {
					return names, err
				}
			}
		case tar.TypeReg:
			if root != nil {
				if err := writeAsset(root, name, tr); err != nil {
					return names, err
				}
			}
		default:
			continue
		}
		names = append(names, name)
	}
}

// writeAsset writes a file next to its place, then renames it there: a
// request never gets half a file.
func writeAsset(root *os.Root, name string, content io.Reader) error {
	if err := root.MkdirAll(path.Dir(name), 0755); err != nil {
		return err
	}
	tmp := name + ".chasen-new"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, content)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		root.Remove(tmp)
	}
	return err
}
