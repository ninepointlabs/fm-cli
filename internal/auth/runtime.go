package auth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// RuntimeDir returns fm-cli's private per-user runtime directory, used for
// the refresh lock, the watch lock and the TUI hand-off socket. It prefers
// $XDG_RUNTIME_DIR and otherwise a directory under the user's cache dir —
// never a shared /tmp, where another user could pre-create the path. The
// directory must be owned by this user, not a symlink, and closed to others.
func RuntimeDir() (string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("no runtime directory: %w", err)
		}
		base = filepath.Join(cache, "fm-cli")
	}
	dir := filepath.Join(base, "fm-cli")
	if base != os.Getenv("XDG_RUNTIME_DIR") {
		dir = filepath.Join(base, "run")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("runtime directory %s is not a plain directory", dir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return "", fmt.Errorf("runtime directory %s is owned by another user", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return "", fmt.Errorf("runtime directory %s is open to other users", dir)
		}
	}
	return dir, nil
}

// OpenPrivateFile opens (creating if needed) a regular file inside the
// runtime directory without following symlinks.
func OpenPrivateFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New(path + " is not a regular file")
	}
	return file, nil
}
