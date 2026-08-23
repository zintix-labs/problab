// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package optimalrt

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

type artifactSource interface {
	ReadFile(name string) ([]byte, error)
	OpenBinary(name string, size int64) ([]byte, func() error, string, error)
}

type fsSource struct {
	fsys fs.FS
}

func (s fsSource) ReadFile(name string) ([]byte, error) {
	if s.fsys == nil {
		return nil, fmt.Errorf("optimal source is required when use_optimal=true")
	}
	return fs.ReadFile(s.fsys, name)
}

func (s fsSource) OpenBinary(name string, size int64) ([]byte, func() error, string, error) {
	data, err := s.ReadFile(name)
	if err != nil {
		return nil, nil, "", err
	}
	if int64(len(data)) != size {
		return nil, nil, "", fmt.Errorf("size mismatch for %s: got=%d want=%d", name, len(data), size)
	}
	return data, func() error { return nil }, "memory", nil
}

type dirSource struct {
	root string
}

func newDirSource(root string) (*dirSource, error) {
	if root == "" {
		return nil, fmt.Errorf("optimal directory is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve optimal directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("stat optimal directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("optimal directory is not a directory: %s", abs)
	}
	return &dirSource{root: abs}, nil
}

func (s *dirSource) path(name string) (string, error) {
	if !validArtifactPath(name) {
		return "", fmt.Errorf("invalid artifact path: %q", name)
	}
	return filepath.Join(s.root, filepath.FromSlash(name)), nil
}

func (s *dirSource) ReadFile(name string) ([]byte, error) {
	filename, err := s.path(name)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filename)
}

func (s *dirSource) OpenBinary(name string, size int64) ([]byte, func() error, string, error) {
	filename, err := s.path(name)
	if err != nil {
		return nil, nil, "", err
	}
	if mmapAvailable {
		data, closeFn, err := mmapReadOnly(filename, size)
		if err != nil {
			return nil, nil, "", err
		}
		return data, closeFn, "mmap", nil
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, nil, "", err
	}
	if int64(len(data)) != size {
		return nil, nil, "", fmt.Errorf("size mismatch for %s: got=%d want=%d", name, len(data), size)
	}
	return data, func() error { return nil }, "memory", nil
}
