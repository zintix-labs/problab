// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package optimalrt

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const mmapAvailable = true

func mmapReadOnly(filename string, expectedSize int64) ([]byte, func() error, error) {
	if expectedSize <= 0 || expectedSize > int64(^uint(0)>>1) {
		return nil, nil, fmt.Errorf("invalid mmap size: %d", expectedSize)
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("mmap source is not a regular file: %s", filename)
	}
	if info.Size() != expectedSize {
		_ = f.Close()
		return nil, nil, fmt.Errorf("mmap size mismatch for %s: got=%d want=%d", filename, info.Size(), expectedSize)
	}
	data, err := unix.Mmap(int(f.Fd()), 0, int(expectedSize), unix.PROT_READ, unix.MAP_SHARED)
	closeErr := f.Close()
	if err != nil {
		return nil, nil, err
	}
	if closeErr != nil {
		_ = unix.Munmap(data)
		return nil, nil, closeErr
	}
	return data, func() error { return unix.Munmap(data) }, nil
}
