//go:build !linux

package sudoterm

// sessionKillSupported is false where the runner cannot list the processes
// of a session: a background job with its own process group that ignores
// SIGHUP would outlive the terminal, so NewRunner refuses to start.
const sessionKillSupported = false

func killSession(int) {}
