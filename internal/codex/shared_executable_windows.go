package codex

import (
	"io"
	"os"
	"path/filepath"
	"time"
)

// The Windows installer closes processes under its installation directory.
// A detached bridge uses its own executable so an update can replace the GUI.
func sharedWorkerExecutable(dir string) (string, func(), error) {
	source, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	in, err := os.Open(source)
	if err != nil {
		return "", nil, err
	}
	defer in.Close()
	cache := filepath.Join(dir, "codex-bridge")
	if err = os.MkdirAll(cache, 0700); err != nil {
		return "", nil, err
	}
	// Windows protects running images. Leave recent copies to concurrent launches.
	old, _ := filepath.Glob(filepath.Join(cache, "bridge-*.exe"))
	for _, path := range old {
		if info, e := os.Stat(path); e == nil && time.Since(info.ModTime()) > 24*time.Hour {
			_ = os.Remove(path)
		}
	}
	out, err := os.CreateTemp(cache, "bridge-*.exe")
	if err != nil {
		return "", nil, err
	}
	remove := func() { _ = os.Remove(out.Name()) }
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		remove()
		return "", nil, err
	}
	return out.Name(), remove, nil
}
