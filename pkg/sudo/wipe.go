package sudo

// Wipe overwrites a buffer that held typed input.
func Wipe(buffer []byte) {
	for i := range buffer {
		buffer[i] = 0
	}
}
