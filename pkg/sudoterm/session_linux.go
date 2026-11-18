//go:build linux

package sudoterm

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// sessionKillSupported reports whether killSession can find every process
// of a session: Linux lists them in /proc.
const sessionKillSupported = true

// killSession kills every process of session sid that this account can
// signal, and returns how many it was refused. The shell leads its session
// (setsid), so sid is its pid. The runner is not root: a process that sudo
// started is one it cannot signal, and one that left the session (setsid)
// is one it does not find.
func killSession(sid int) (refused int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp session ...; comm may hold spaces.
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) > 3 && fields[3] == strconv.Itoa(sid) {
			if errors.Is(signalProcess(pid, syscall.SIGKILL), syscall.EPERM) {
				refused++
			}
		}
	}
	return refused
}
