//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"syscall"
)

func acquireProcessSingleton() (func(), error) {
	if err := os.MkdirAll(weclawDir(), 0o700); err != nil {
		return nil, fmt.Errorf("create weclaw dir: %w", err)
	}

	f, err := os.OpenFile(lockFile(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("weclaw is already running")
	}

	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteString(fmt.Sprintf("%d\n", os.Getpid()))
		_, _ = f.Seek(0, 0)
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
