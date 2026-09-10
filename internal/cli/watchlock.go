package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"fm-cli/internal/auth"
)

// acquireWatchLock takes the per-user watch lock, or fails with a usage-level
// error naming the holder. The lock file lives in the runtime directory and
// is released when the process exits.
func acquireWatchLock() (func(), error) {
	dir, err := auth.RuntimeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "watch.lock")
	file, err := auth.OpenPrivateFile(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := ""
		if data, readErr := os.ReadFile(path); readErr == nil {
			holder = strings.TrimSpace(string(data))
		}
		file.Close()
		msg := "another fm-cli watch is already running"
		if holder != "" {
			msg += " (pid " + holder + ")"
		}
		return nil, &Error{Code: "busy", Message: msg, Hint: "Fastmail keeps one push connection per sign-in, so only one watch can run at a time."}
	}
	_ = file.Truncate(0)
	_, _ = file.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
	}, nil
}
