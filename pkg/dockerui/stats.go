package dockerui

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	// statsTTL lets the 3 s polling of several tabs share one refresh.
	statsTTL = 3 * time.Second
	// statsWait bounds how long a list request waits for the refresh it
	// started; one-shot reads normally take well under 100 ms.
	statsWait = 1500 * time.Millisecond
	// statsMaxAge drops samples too old to describe the present.
	statsMaxAge = 30 * time.Second
	// statsRefreshTimeout bounds a whole refresh, every container included.
	statsRefreshTimeout = 8 * time.Second
	statsWorkers        = 6
)

// cpuSample is what the next sample needs to compute a CPU percentage.
type cpuSample struct {
	total  uint64
	system uint64
	online int
	at     time.Time
}

type statsEntry struct {
	cpu  cpuSample
	view Stats
	// fetched is this hub's clock when the sample arrived.
	fetched time.Time
}

type statsCache struct {
	mu       sync.Mutex
	entries  map[string]statsEntry
	fresh    time.Time
	inflight chan struct{}
}

// forget drops a container's samples, after an action changed it.
func (c *statsCache) forget(id string) {
	c.mu.Lock()
	delete(c.entries, id)
	c.fresh = time.Time{}
	c.mu.Unlock()
}

// containerStats returns the latest samples of the running containers,
// refreshing them at most every statsTTL. Concurrent callers share one
// refresh. Only the caller that starts a refresh waits for it, at most
// statsWait (or a caller with no sample at all yet): a slow daemon delays
// the numbers, not the list.
func (s *Service) containerStats(ctx context.Context, running []string, fallbackCPUs int) map[string]Stats {
	c := &s.stats
	c.mu.Lock()
	var wait chan struct{}
	if c.inflight == nil && len(running) > 0 && s.now().Sub(c.fresh) >= statsTTL {
		done := make(chan struct{})
		c.inflight = done
		wait = done
		previous := make(map[string]statsEntry, len(c.entries))
		for id, entry := range c.entries {
			previous[id] = entry
		}
		go s.refreshStats(append([]string(nil), running...), previous, fallbackCPUs, done)
	} else if c.inflight != nil && len(c.entries) == 0 {
		wait = c.inflight
	}
	c.mu.Unlock()
	if wait != nil {
		timer := time.NewTimer(statsWait)
		select {
		case <-wait:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Stats, len(running))
	for _, id := range running {
		if entry, ok := c.entries[id]; ok && s.now().Sub(entry.fetched) < statsMaxAge {
			out[id] = entry.view
		}
	}
	return out
}

func (s *Service) refreshStats(ids []string, previous map[string]statsEntry, fallbackCPUs int, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), statsRefreshTimeout)
	defer cancel()
	type result struct {
		id    string
		entry statsEntry
	}
	jobs := make(chan string)
	results := make(chan result, len(ids))
	var workers sync.WaitGroup
	for range min(statsWorkers, len(ids)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for id := range jobs {
				var prevSample *cpuSample
				if prev, ok := previous[id]; ok && s.now().Sub(prev.fetched) < statsMaxAge {
					prevSample = &prev.cpu
				}
				// One-shot reads answer at once (about 70 ms for 8 containers,
				// against 2 s for Docker's two-cycle read): CPU then comes from
				// the previous refresh, so it appears on the second listing.
				raw, err := s.client.stats(ctx, id, true)
				if err != nil {
					continue
				}
				now := s.now()
				view, sample := computeStats(raw, prevSample, fallbackCPUs, now)
				results <- result{id: id, entry: statsEntry{cpu: sample, view: view, fetched: now}}
			}
		}()
	}
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)
	workers.Wait()
	close(results)

	entries := make(map[string]statsEntry, len(ids))
	for r := range results {
		entries[r.id] = r.entry
	}
	c := &s.stats
	c.mu.Lock()
	c.entries = entries
	c.fresh = s.now()
	c.inflight = nil
	c.mu.Unlock()
	close(done)
}

// computeStats turns one Engine API sample into the browser view. The CPU
// percentage follows the docker CLI: (container delta / host delta) × CPUs ×
// 100. It uses Docker's own previous sample when there is one (a two-cycle
// read), otherwise prev, the sample this hub kept from its last refresh.
func computeStats(raw apiStats, prev *cpuSample, fallbackCPUs int, now time.Time) (Stats, cpuSample) {
	at := now
	if parsed, err := time.Parse(time.RFC3339Nano, raw.Read); err == nil && parsed.Year() > 1 {
		at = parsed
	}
	online := raw.CPUStats.OnlineCPUs
	if online == 0 {
		online = len(raw.CPUStats.CPUUsage.PercpuUsage)
	}
	if online == 0 {
		online = max(fallbackCPUs, 1)
	}
	sample := cpuSample{total: raw.CPUStats.CPUUsage.TotalUsage, system: raw.CPUStats.SystemUsage, online: online, at: at}

	var cpu *float64
	switch {
	case raw.PreCPUStats.SystemUsage > 0:
		cpu = cpuPercent(raw.PreCPUStats.CPUUsage.TotalUsage, sample.total, raw.PreCPUStats.SystemUsage, sample.system, online, 0)
	case prev != nil && prev.system > 0 && sample.system > 0:
		cpu = cpuPercent(prev.total, sample.total, prev.system, sample.system, online, 0)
	case prev != nil:
		// No host counter (some rootless setups): divide by wall time instead.
		cpu = cpuPercent(prev.total, sample.total, 0, 0, 1, at.Sub(prev.at))
	}

	view := Stats{
		CPUPercent:  cpu,
		MemoryUsage: memoryUsage(raw),
		MemoryLimit: raw.MemoryStats.Limit,
		PIDs:        raw.PidsStats.Current,
		At:          at.UnixMilli(),
	}
	for _, network := range raw.Networks {
		view.NetRx += network.RxBytes
		view.NetTx += network.TxBytes
	}
	for _, entry := range raw.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(entry.Op) {
		case "read":
			view.BlockRead += entry.Value
		case "write":
			view.BlockWrite += entry.Value
		}
	}
	return view, sample
}

func cpuPercent(prevTotal, total, prevSystem, system uint64, online int, wall time.Duration) *float64 {
	if total < prevTotal {
		return nil // the container restarted: its counter went back to zero
	}
	delta := float64(total - prevTotal)
	var value float64
	if wall > 0 {
		value = delta / float64(wall.Nanoseconds()) * 100
	} else {
		if system <= prevSystem {
			return nil
		}
		value = delta / float64(system-prevSystem) * float64(online) * 100
	}
	return &value
}

// memoryUsage excludes the page cache that the kernel can reclaim, like
// `docker stats`: inactive_file on cgroup v2, total_inactive_file on v1.
func memoryUsage(raw apiStats) uint64 {
	usage := raw.MemoryStats.Usage
	if v, ok := raw.MemoryStats.Stats["total_inactive_file"]; ok && v < usage {
		return usage - v
	}
	if v, ok := raw.MemoryStats.Stats["inactive_file"]; ok && v < usage {
		return usage - v
	}
	return usage
}
