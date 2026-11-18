//go:build linux

package sudoterm

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// sessionKillSupported reports whether killSession can find every process
// of a session: Linux lists them in /proc.
const sessionKillSupported = true

// killSession kills every process of session sid that this account can
// signal. The shell leads its session (setsid), so sid is its pid.
func killSession(sid int) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
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
			_ = signalProcess(pid, syscall.SIGKILL)
		}
	}
}
