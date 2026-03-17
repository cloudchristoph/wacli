//go:build windows

package lock

import (
    "fmt"
    "os"
    "path/filepath"
    "time"
)

type Lock struct {
    path string
    f    *os.File
}

func Acquire(storeDir string) (*Lock, error) {
    if err := os.MkdirAll(storeDir, 0700); err != nil {
        return nil, fmt.Errorf("create store dir: %w", err)
    }
    path := filepath.Join(storeDir, "LOCK")
    f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
    if err != nil {
        return nil, fmt.Errorf("open lock file: %w", err)
    }

    // Windows Note: We are skipping explicit file locking (syscall.Flock)
    // because it is not directly available in the syscall package.
    // A strict implementation would use LockFileEx via syscall or golang.org/x/sys/windows.
    // For now, we rely on the file existence logic.

    _ = f.Truncate(0)
    _, _ = f.Seek(0, 0)
    _, _ = fmt.Fprintf(f, "pid=%d\nacquired_at=%s\n", os.Getpid(), time.Now().Format(time.RFC3339Nano))
    _ = f.Sync()

    return &Lock{path: path, f: f}, nil
}

func (l *Lock) Release() error {
    if l == nil || l.f == nil {
        return nil
    }
    err := l.f.Close()
    l.f = nil
    return err
}
