package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// A three-node cluster with pve2 down, as /cluster/status and /cluster/resources report it.
const statusJSON = `[
  {"type":"cluster","name":"satoru-cluster","quorate":1,"nodes":3},
  {"type":"node","name":"pve","online":1,"local":0},
  {"type":"node","name":"pve2","online":0,"local":0},
  {"type":"node","name":"pve3","online":1,"local":1}
]`

const resourcesJSON = `[
  {"type":"node","node":"pve","status":"online","cpu":0.125,"maxcpu":12,"mem":16750372454,"maxmem":33604000000,"uptime":123456},
  {"type":"node","node":"pve2","status":"offline"},
  {"type":"node","node":"pve3","status":"online","cpu":0.5,"maxcpu":4,"mem":4294967296,"maxmem":17179869184,"uptime":42},
  {"type":"lxc","vmid":104,"name":"lxc-claude","node":"pve","status":"running","cpu":0.02,"mem":536870912,"maxmem":2147483648},
  {"type":"lxc","vmid":100,"name":"lxc-docker","node":"pve","status":"running","cpu":0.09,"mem":12992000000,"maxmem":22548578304},
  {"type":"qemu","vmid":900,"name":"tmpl","node":"pve","status":"stopped","template":1},
  {"type":"storage","storage":"tank","node":"pve","status":"available","disk":270,"maxdisk":1000},
  {"type":"storage","storage":"tank","node":"pve3","status":"unknown"},
  {"type":"storage","storage":"local","node":"pve","status":"available","disk":1,"maxdisk":2}
]`

func fixture(t *testing.T) *ClusterData {
	d := &ClusterData{Source: "pve3"}
	if err := json.Unmarshal([]byte(statusJSON), &d.Status); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(resourcesJSON), &d.Resources); err != nil {
		t.Fatal(err)
	}
	return d
}

func sources(t *testing.T) Sources {
	return Sources{
		Cluster:  func(context.Context) (*ClusterData, error) { return fixture(t), nil },
		UPS:      func(context.Context) (*UPS, error) { return &UPS{Status: "OL", ChargePct: intPtr(100)}, nil },
		Temps:    func() Temps { return Temps{CPU: intPtr(47), NVMe: intPtr(38)} },
		TempNode: "pve",
		Storage:  "tank",
	}
}

func TestCollectBuildsTheContract(t *testing.T) {
	s := Collect(context.Background(), sources(t), time.Date(2026, 10, 24, 19, 40, 0, 0, time.FixedZone("EDT", -4*3600)))

	if s.Version != 1 || s.Updated != "2026-10-24T19:40:00-04:00" || *s.SourceNode != "pve3" {
		t.Errorf("header = %d %s %v", s.Version, s.Updated, *s.SourceNode)
	}
	if *s.Cluster != (ClusterInfo{Online: 2, Total: 3, Quorate: true}) {
		t.Errorf("cluster = %+v", *s.Cluster)
	}
	// Weighted by cores: (0.125×12 + 0.5×4) / 16 = 21.9%, where a plain average would say 31%.
	if s.Overall.CPUPct != 22 || s.Overall.MemTotalGB != 47.3 {
		t.Errorf("overall = %+v", *s.Overall)
	}
	if len(s.Nodes) != 3 || s.Nodes[0].Name != "pve" || *s.Nodes[0].CPUPct != 13 || *s.Nodes[0].MemPct != 50 {
		t.Fatalf("nodes = %+v", s.Nodes)
	}
	if *s.Nodes[0].TempC != 47 || *s.Nodes[0].NVMeTempC != 38 || s.Nodes[0].NICTempC != nil {
		t.Errorf("pve temps = %v %v %v", s.Nodes[0].TempC, s.Nodes[0].NVMeTempC, s.Nodes[0].NICTempC)
	}
	if s.Nodes[2].TempC != nil {
		t.Errorf("pve3 has no sensor source, temp should be null")
	}
	if off := s.Nodes[1]; off.Online || off.CPUPct != nil || off.MemPct != nil || off.UptimeS != nil {
		t.Errorf("offline node should have null numbers: %+v", off)
	}
	if len(s.Guests) != 2 || s.Guests[0].VMID != 100 || s.Guests[0].MemUsedGB != 12.1 || s.Guests[0].MemTotalGB != 21 {
		t.Errorf("guests (sorted, no templates) = %+v", s.Guests)
	}
	if s.Storage == nil || s.Storage.TankUsedPct != 27 {
		t.Errorf("storage = %+v", s.Storage)
	}
	if len(s.Errors) != 0 {
		t.Errorf("errors = %v", s.Errors)
	}
}

func TestNUTDownNullsOnlyTheUPS(t *testing.T) {
	// A port nothing listens on: upsd is down (pve is off).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	src := sources(t)
	src.UPS = (&NUT{Addr: addr, UPS: "cp1500"}).Fetch
	s := Collect(context.Background(), src, time.Now())

	if s.UPS != nil {
		t.Errorf("ups = %+v, want null", s.UPS)
	}
	if len(s.Errors) != 1 || !strings.HasPrefix(s.Errors[0], "ups: ") {
		t.Errorf("errors = %v, want one ups entry", s.Errors)
	}
	if s.Cluster == nil || len(s.Nodes) != 3 {
		t.Error("the rest of the response should still be there")
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"ups":null`) {
		t.Errorf("JSON should carry ups: null, got %s", b)
	}
}

func TestProxmoxDownStillServesTheUPS(t *testing.T) {
	src := sources(t)
	src.Cluster = func(context.Context) (*ClusterData, error) { return nil, errors.New("no node answered") }
	s := Collect(context.Background(), src, time.Now())
	b, _ := json.Marshal(s)
	for _, want := range []string{`"cluster":null`, `"overall":null`, `"nodes":[]`, `"guests":[]`, `"storage":null`, `"status":"OL"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
	if len(s.Errors) != 1 || !strings.HasPrefix(s.Errors[0], "proxmox: ") {
		t.Errorf("errors = %v", s.Errors)
	}
}

func TestMissingStorageIsAnError(t *testing.T) {
	src := sources(t)
	src.Storage = "nope"
	s := Collect(context.Background(), src, time.Now())
	if s.Storage != nil || len(s.Errors) != 1 || !strings.HasPrefix(s.Errors[0], "storage: ") {
		t.Errorf("storage = %+v, errors = %v", s.Storage, s.Errors)
	}
}

func TestProxmoxFailsOverToTheNextNode(t *testing.T) {
	var auth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api2/json/cluster/status":
			w.Write([]byte(`{"data":` + statusJSON + `}`))
		case "/api2/json/cluster/resources":
			w.Write([]byte(`{"data":` + resourcesJSON + `}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// The first "node" refuses connections; the second is the test server, trusted through its own certificate
	// exactly as the real client trusts the cluster CA.
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := dead.Addr().String()
	dead.Close()
	client, err := NewProxmoxClient(pemOf(srv))
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxmox{Hosts: []string{deadAddr, strings.TrimPrefix(srv.URL, "https://")}, TokenID: "monitor@pve!tv", Secret: "s3cret", Client: client}

	d, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Source != "pve3" || len(d.Resources) != 9 {
		t.Errorf("source = %q, %d resources", d.Source, len(d.Resources))
	}
	if auth != "PVEAPIToken=monitor@pve!tv=s3cret" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestProxmoxRejectsAnUntrustedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	// Trust only the real cluster CA, which didn't sign the test server's certificate: the request must fail,
	// never fall back to skipping verification.
	ca, err := os.ReadFile("certs/pve-root-ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewProxmoxClient(ca)
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxmox{Hosts: []string{strings.TrimPrefix(srv.URL, "https://")}, Client: client}
	if _, err := p.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("err = %v, want a certificate error", err)
	}
}

// pemOf is the test server's certificate as a PEM "CA" file.
func pemOf(srv *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

func TestCacheFetchesOncePerTTL(t *testing.T) {
	n := 0
	c := &Cache{TTL: 15 * time.Second, Fetch: func() Stats { n++; return Stats{} }}
	t0 := time.Now()
	c.Get(t0)
	c.Get(t0.Add(14 * time.Second))
	if n != 1 {
		t.Errorf("fetched %d times within the TTL, want 1", n)
	}
	c.Get(t0.Add(15 * time.Second))
	c.Get(t0.Add(-time.Minute)) // the clock went back: fetch again rather than serve forever
	if n != 3 {
		t.Errorf("fetched %d times, want 3", n)
	}
}
