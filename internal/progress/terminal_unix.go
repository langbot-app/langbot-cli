//go:build !windows

package progress

import "os"

func enableVT(file *os.File) bool { return true }
