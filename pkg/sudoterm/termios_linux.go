//go:build linux

package sudoterm

import "golang.org/x/sys/unix"

// ioctlGetTermios is the request that reads a terminal's settings.
const ioctlGetTermios = unix.TCGETS
