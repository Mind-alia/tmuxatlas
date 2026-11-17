package dockerui

// Engine API payloads, limited to the fields this package reads.

type apiContainer struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	Created int64             `json:"Created"`
	Ports   []apiPort         `json:"Ports"`
	Labels  map[string]string `json:"Labels"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	// Health is only sent by API 1.52 and later.
	Health *struct {
		Status string `json:"Status"`
	} `json:"Health"`
}

type apiPort struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

type apiInfo struct {
	NCPU          int    `json:"NCPU"`
	MemTotal      int64  `json:"MemTotal"`
	ServerVersion string `json:"ServerVersion"`
}

// apiInspect is the part of the inspect document kept in the list cache.
type apiInspect struct {
	RestartCount int `json:"RestartCount"`
	State        struct {
		Status     string `json:"Status"`
		ExitCode   int    `json:"ExitCode"`
		OOMKilled  bool   `json:"OOMKilled"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Tty         bool `json:"Tty"`
		StopTimeout *int `json:"StopTimeout"`
	} `json:"Config"`
}

type apiCPUStats struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PercpuUsage []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs  int    `json:"online_cpus"`
}

type apiStats struct {
	Read        string      `json:"read"`
	CPUStats    apiCPUStats `json:"cpu_stats"`
	PreCPUStats apiCPUStats `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

// Views sent to the browser.

// ListResponse is the payload of GET /api/docker/containers.
type ListResponse struct {
	Available bool `json:"available"`
	// Error explains why Docker is unavailable, or a partial failure.
	Error      string      `json:"error,omitempty"`
	Host       *HostInfo   `json:"host,omitempty"`
	Summary    Summary     `json:"summary"`
	Containers []Container `json:"containers"`
	// Terminal tells whether a shell can be opened in a container.
	Terminal bool `json:"terminal"`
	// CollectedAt is the server time of the listing, in unix milliseconds.
	CollectedAt int64 `json:"collected_at"`
}

// HostInfo is the Docker host summary.
type HostInfo struct {
	NCPU     int    `json:"ncpu"`
	MemTotal int64  `json:"mem_total"`
	Version  string `json:"version,omitempty"`
}

// Summary adds up the running containers.
type Summary struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	// CPUPercent follows Docker: 100 is one full CPU.
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryUsage uint64  `json:"memory_usage"`
	// StatsPending is true while some running container has no sample yet.
	StatsPending bool `json:"stats_pending"`
}

// Container is one row of the list.
type Container struct {
	ID           string `json:"id"`
	ShortID      string `json:"short_id"`
	Name         string `json:"name"`
	Image        string `json:"image"`
	State        string `json:"state"`  // created | running | paused | restarting | removing | exited | dead
	Status       string `json:"status"` // Docker's text, such as "Up 2 hours (healthy)"
	Health       string `json:"health,omitempty"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	OOMKilled    bool   `json:"oom_killed,omitempty"`
	Project      string `json:"compose_project,omitempty"`
	Service      string `json:"compose_service,omitempty"`
	Ports        []Port `json:"ports"`
	Created      int64  `json:"created"`
	StartedAt    string `json:"started_at,omitempty"`
	FinishedAt   string `json:"finished_at,omitempty"`
	RestartCount int    `json:"restart_count"`
	Stats        *Stats `json:"stats,omitempty"`
}

// Port is a published or exposed port, IPv4 and IPv6 bindings merged.
type Port struct {
	IP      string `json:"ip,omitempty"`
	Private int    `json:"private"`
	Public  int    `json:"public,omitempty"`
	Type    string `json:"type"`
}

// Stats is the last sample of a running container.
type Stats struct {
	// CPUPercent is nil until two samples can be compared.
	CPUPercent  *float64 `json:"cpu_percent"`
	MemoryUsage uint64   `json:"memory_usage"`
	MemoryLimit uint64   `json:"memory_limit"`
	NetRx       uint64   `json:"net_rx"`
	NetTx       uint64   `json:"net_tx"`
	BlockRead   uint64   `json:"block_read"`
	BlockWrite  uint64   `json:"block_write"`
	PIDs        uint64   `json:"pids"`
	// At is the sample time in unix milliseconds.
	At int64 `json:"at"`
}

// ActionResponse answers start, stop, restart and delete.
type ActionResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Action  string `json:"action"`
	Changed bool   `json:"changed"`
}

// TerminalResponse names the tmux session that runs the container shell.
type TerminalResponse struct {
	HostID  string `json:"host_id"`
	Session string `json:"session"`
	Created bool   `json:"created"`
}
