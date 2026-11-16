//go:build unix

package files

import "syscall"

// nofollow makes opening a symbolic link fail instead of following it.
const nofollow = syscall.O_NOFOLLOW
