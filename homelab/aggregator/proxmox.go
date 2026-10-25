package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// ClusterData is what one Proxmox node told us about the whole cluster.
type ClusterData struct {
	Source    string // name of the node that answered
	Status    []statusEntry
	Resources []resource
}

// /cluster/status: one "cluster" entry (absent on a single node) and one "node" entry per member.
type statusEntry struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Online  int    `json:"online"`
	Local   int    `json:"local"`
	Quorate int    `json:"quorate"`
}

// /cluster/resources: nodes, guests (qemu, lxc) and storage in one list. cpu is a 0–1 fraction, the rest bytes.
type resource struct {
	Type     string  `json:"type"`
	Node     string  `json:"node"`
	Status   string  `json:"status"`
	VMID     int     `json:"vmid"`
	Name     string  `json:"name"`
	Storage  string  `json:"storage"`
	Template int     `json:"template"`
	CPU      float64 `json:"cpu"`
	MaxCPU   int     `json:"maxcpu"`
	Mem      float64 `json:"mem"`
	MaxMem   float64 `json:"maxmem"`
	Disk     float64 `json:"disk"`
	MaxDisk  float64 `json:"maxdisk"`
	Uptime   int64   `json:"uptime"`
}

// Proxmox reads the cluster through whichever node answers first. Every node holds the whole cluster's state, so
// with pve down, pve2 gives the same picture.
type Proxmox struct {
	Hosts   []string // host:port, tried in order
	TokenID string
	Secret  string
	Client  *http.Client
}

// NewProxmoxClient trusts only the cluster's own CA: Proxmox signs its node certificates with it, so verifying
// against it proves we're talking to the cluster, and nobody else on the network gets the token.
func NewProxmoxClient(caPEM []byte) (*http.Client, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no certificate in the Proxmox CA file")
	}
	return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}, nil
}

func (p *Proxmox) Fetch(ctx context.Context) (*ClusterData, error) {
	var failures []string
	for _, host := range p.Hosts {
		d, err := p.fetchFrom(ctx, host)
		if err == nil {
			return d, nil
		}
		failures = append(failures, host+": "+shortErr(err))
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.New("no node answered (" + strings.Join(failures, "; ") + ")")
}

func (p *Proxmox) fetchFrom(ctx context.Context, host string) (*ClusterData, error) {
	d := &ClusterData{}
	if err := p.get(ctx, host, "/cluster/status", &d.Status); err != nil {
		return nil, err
	}
	if err := p.get(ctx, host, "/cluster/resources", &d.Resources); err != nil {
		return nil, err
	}
	for _, e := range d.Status {
		if e.Type == "node" && e.Local == 1 {
			d.Source = e.Name
		}
	}
	return d, nil
}

func (p *Proxmox) get(ctx context.Context, host, path string, into any) error {
	// Each node gets at most 4 s so a dead one leaves time to try the others.
	ctx, cancel := context.WithTimeout(ctx, 4e9)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/api2/json"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+p.TokenID+"="+p.Secret)
	res, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", path, res.StatusCode)
	}
	var body struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return fmt.Errorf("%s: %v", path, err)
	}
	return json.Unmarshal(body.Data, into)
}

// shortErr drops Go's long "Get \"https://…\": " and "dial tcp …: " prefixes: these reasons end up on a TV.
func shortErr(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && (strings.HasPrefix(msg, "Get ") || strings.HasPrefix(msg, "dial ")) {
		return msg[i+2:]
	}
	return msg
}

// fillCluster converts the raw Proxmox data into the contract's cluster, overall, nodes, guests and storage.
func fillCluster(s *Stats, d *ClusterData, storage, tempNode string, temps Temps) {
	if d.Source != "" {
		s.SourceNode = stringPtr(d.Source)
	}

	info := &ClusterInfo{Quorate: true} // a lone node with no "cluster" entry is trivially quorate
	for _, e := range d.Status {
		switch e.Type {
		case "cluster":
			info.Quorate = e.Quorate == 1
		case "node":
			info.Total++
			if e.Online == 1 {
				info.Online++
			}
		}
	}
	s.Cluster = info

	var fractions []float64
	var cores []int
	var memUsed, memTotal float64
	for _, r := range d.Resources {
		switch r.Type {
		case "node":
			n := Node{Name: r.Node, Online: r.Status == "online"}
			if n.Online {
				n.CPUPct = intPtr(pct(r.CPU))
				n.MemUsedGB = floatPtr(gb(r.Mem))
				n.MemTotalGB = floatPtr(gb(r.MaxMem))
				n.MemPct = intPtr(ratioPct(r.Mem, r.MaxMem))
				n.UptimeS = int64Ptr(r.Uptime)
				fractions = append(fractions, r.CPU)
				cores = append(cores, r.MaxCPU)
				memUsed += r.Mem
				memTotal += r.MaxMem
				if r.Node == tempNode {
					n.TempC, n.NVMeTempC, n.NICTempC = temps.CPU, temps.NVMe, temps.NIC
				}
			}
			s.Nodes = append(s.Nodes, n)
		case "qemu", "lxc":
			if r.Template == 1 {
				continue
			}
			s.Guests = append(s.Guests, Guest{
				VMID: r.VMID, Name: r.Name, Type: r.Type, Node: r.Node, Status: r.Status,
				CPUPct: pct(r.CPU), MemUsedGB: gb(r.Mem), MemTotalGB: gb(r.MaxMem),
			})
		}
	}
	sort.Slice(s.Nodes, func(i, j int) bool { return s.Nodes[i].Name < s.Nodes[j].Name })
	sort.Slice(s.Guests, func(i, j int) bool { return s.Guests[i].VMID < s.Guests[j].VMID })

	if len(fractions) > 0 {
		s.Overall = &Overall{
			CPUPct:     weightedCPU(fractions, cores),
			MemUsedGB:  gb(memUsed),
			MemTotalGB: gb(memTotal),
			MemPct:     ratioPct(memUsed, memTotal),
		}
	}

	if st := findStorage(d.Resources, storage); st != nil {
		s.Storage = &Storage{TankUsedPct: ratioPct(st.Disk, st.MaxDisk)}
	} else {
		s.Errors = append(s.Errors, "storage: no available storage named \""+storage+"\"")
	}
}

// findStorage picks the storage entry with that id that has a size. A storage enabled on several nodes is listed
// once per node; a shared one reports the same numbers everywhere, and nodes that can't reach it report no size.
func findStorage(rs []resource, name string) *resource {
	var best *resource
	for i := range rs {
		r := &rs[i]
		if r.Type != "storage" || r.Storage != name || r.Status != "available" || r.MaxDisk <= 0 {
			continue
		}
		if best == nil || r.MaxDisk > best.MaxDisk {
			best = r
		}
	}
	return best
}
