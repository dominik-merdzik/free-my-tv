package main

import (
	"context"
	"sync"
	"time"
)

// Stats is the JSON contract v1 served at GET /stats (see HOMELAB.md §5). Numbers are ready to display: the TV
// does no maths. A section whose source failed is null, with a short reason in Errors.
type Stats struct {
	Version    int          `json:"version"`
	Updated    string       `json:"updated"`
	SourceNode *string      `json:"source_node"`
	Cluster    *ClusterInfo `json:"cluster"`
	Overall    *Overall     `json:"overall"`
	Nodes      []Node       `json:"nodes"`
	Guests     []Guest      `json:"guests"`
	Storage    *Storage     `json:"storage"`
	UPS        *UPS         `json:"ups"`
	Errors     []string     `json:"errors"`
}

type ClusterInfo struct {
	Online  int  `json:"online"`
	Total   int  `json:"total"`
	Quorate bool `json:"quorate"`
}

type Overall struct {
	CPUPct     int     `json:"cpu_pct"`
	MemUsedGB  float64 `json:"mem_used_gb"`
	MemTotalGB float64 `json:"mem_total_gb"`
	MemPct     int     `json:"mem_pct"`
}

// Node is one Proxmox host. An offline node has only its name and online=false: its numbers are null, never a
// stale value that could pass for a healthy one.
type Node struct {
	Name       string   `json:"name"`
	Online     bool     `json:"online"`
	CPUPct     *int     `json:"cpu_pct"`
	MemUsedGB  *float64 `json:"mem_used_gb"`
	MemTotalGB *float64 `json:"mem_total_gb"`
	MemPct     *int     `json:"mem_pct"`
	UptimeS    *int64   `json:"uptime_s"`
	TempC      *int     `json:"temp_c"`
	// Optional extras, only on the node whose sensors we can read (see hwmon.go).
	NVMeTempC *int `json:"nvme_temp_c,omitempty"`
	NICTempC  *int `json:"nic_temp_c,omitempty"`
}

type Guest struct {
	VMID       int     `json:"vmid"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Node       string  `json:"node"`
	Status     string  `json:"status"`
	CPUPct     int     `json:"cpu_pct"`
	MemUsedGB  float64 `json:"mem_used_gb"`
	MemTotalGB float64 `json:"mem_total_gb"`
}

type Storage struct {
	TankUsedPct int `json:"tank_used_pct"`
}

type UPS struct {
	Status     string `json:"status"`
	ChargePct  *int   `json:"charge_pct"`
	RuntimeMin *int   `json:"runtime_min"`
	LoadPct    *int   `json:"load_pct"`
}

// Temps are the CPU package, boot drive and NIC temperatures of the host this container runs on. nil = unknown.
type Temps struct {
	CPU, NVMe, NIC *int
}

// Sources are the three places the numbers come from; tests swap them for fakes.
type Sources struct {
	Cluster func(ctx context.Context) (*ClusterData, error)
	UPS     func(ctx context.Context) (*UPS, error)
	Temps   func() Temps
	// TempNode is the Proxmox node the Temps belong to; Storage the storage id behind tank_used_pct.
	TempNode, Storage string
}

// Collect asks every source at once, each with its own deadline, and builds the response from whatever answered.
// One source failing nulls its own section and adds to Errors; it never fails the whole response.
func Collect(ctx context.Context, src Sources, now time.Time) Stats {
	s := Stats{Version: 1, Updated: now.Format(time.RFC3339), Nodes: []Node{}, Guests: []Guest{}, Errors: []string{}}

	var wg sync.WaitGroup
	var cluster *ClusterData
	var ups *UPS
	var clusterErr, upsErr error
	var temps Temps
	wg.Add(3)
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		cluster, clusterErr = src.Cluster(c)
	}()
	go func() {
		defer wg.Done()
		c, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		ups, upsErr = src.UPS(c)
	}()
	go func() {
		defer wg.Done()
		temps = src.Temps()
	}()
	wg.Wait()

	if clusterErr != nil {
		s.Errors = append(s.Errors, "proxmox: "+clusterErr.Error())
	} else {
		fillCluster(&s, cluster, src.Storage, src.TempNode, temps)
	}
	if upsErr != nil {
		s.Errors = append(s.Errors, "ups: "+upsErr.Error())
	} else {
		s.UPS = ups
	}
	return s
}

// Cache keeps the last response for a while so several TVs (or a refresh-happy browser) don't each hit the
// Proxmox API. The lock is held while refreshing: callers arriving meanwhile wait for that one fetch.
type Cache struct {
	TTL   time.Duration
	Fetch func() Stats

	mu   sync.Mutex
	at   time.Time
	last Stats
}

func (c *Cache) Get(now time.Time) Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() || now.Sub(c.at) >= c.TTL || now.Before(c.at) {
		c.last = c.Fetch()
		c.at = now
	}
	return c.last
}
