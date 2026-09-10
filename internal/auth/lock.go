package auth

import (
	"os"
	"path/filepath"
	"syscall"
)

// refreshLock serializes token refresh across fm-cli processes. Fastmail
// rotates refresh tokens and revokes the whole grant when an old one is reused,
// so two processes must never refresh at once. The lock lives in the user's
// runtime directory, or the temp directory when there is none.
type refreshLock struct {
	file *os.File
}

func acquireRefreshLock() (*refreshLock, error) {
	dir, err := RuntimeDir()
	if err != nil {
		return nil, err
	}
	file, err := OpenPrivateFile(filepath.Join(dir, "refresh.lock"))
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return &refreshLock{file: file}, nil
}

func (l *refreshLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	l.file.Close()
	l.file = nil
}
