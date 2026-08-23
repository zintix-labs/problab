// Copyright 2025 Zintix Labs
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package optimalrt

import "fmt"

const mmapAvailable = false

func mmapReadOnly(filename string, expectedSize int64) ([]byte, func() error, error) {
	return nil, nil, fmt.Errorf("mmap is not supported on this platform")
}
