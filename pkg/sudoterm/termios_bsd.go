//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package sudoterm

import "golang.org/x/sys/unix"

// ioctlGetTermios is the request that reads a terminal's settings.
const ioctlGetTermios = unix.TIOCGETA
