//go:build !windows

package deploy

import "os"

func currentUserIsRoot() bool { return os.Geteuid() == 0 }
