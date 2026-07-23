//go:build linux

package storeio

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openPageFile(path string, mode DirectMode) (*os.File, bool, error) {
	if mode == DirectOff {
		file, err := os.Open(path)
		return file, false, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECT, 0)
	if err == nil {
		file := os.NewFile(uintptr(fd), path)
		if file == nil {
			_ = unix.Close(fd)
			return nil, false, fmt.Errorf("open direct Store page file %q", path)
		}
		return file, true, nil
	}
	if mode == DirectRequire && directIOUnsupported(err) {
		return nil, false, fmt.Errorf("%w: %w", ErrDirectIOUnsupported, err)
	}
	if !directIOUnsupported(err) {
		return nil, false, fmt.Errorf("open direct Store page file %q: %w", path, err)
	}
	file, fallbackErr := os.Open(path)
	return file, false, fallbackErr
}

func directIOUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES)
}
