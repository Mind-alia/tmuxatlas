package monitor

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// The quota refresh and periodic collection can start together. Both must be
// able to read and populate the shared parsed-file cache without a data race.
func TestCLIAccountsConcurrentCollection(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC()
	const profiles = 8
	for i := 0; i < profiles; i++ {
		dir := filepath.Join(root, fmt.Sprintf("profile-%d", i))
		mustMkdir(t, filepath.Join(dir, "config", "projects", "project"))
		writeTestFile(t, filepath.Join(dir, "profile.json"), fmt.Sprintf(`{"id":"profile-%d","provider":"claude","name":"Test"}`, i))
		writeTestFile(t, filepath.Join(dir, "config", "projects", "project", "session.jsonl"), fmt.Sprintf(`{"timestamp":%q,"requestId":"request","message":{"id":"message","model":"test","usage":{"input_tokens":10,"output_tokens":5}}}`+"\n", now.Format(time.RFC3339)))
	}
	collector := newCLIAccountsCollector(root)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for j := 0; j < 4; j++ {
				accounts := collector.collect(now)
				if len(accounts) != profiles {
					t.Errorf("collected %d profiles, want %d", len(accounts), profiles)
				}
				for _, account := range accounts {
					if account.Tokens24h.Total != 15 {
						t.Errorf("%s: tokens = %d, want 15", account.ID, account.Tokens24h.Total)
					}
				}
			}
		}()
	}
	close(start)
	workers.Wait()
}
