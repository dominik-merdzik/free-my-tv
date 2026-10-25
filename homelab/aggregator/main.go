// Command aggregator serves a summary of the homelab (Proxmox cluster, UPS, pve's temperatures) as JSON for the
// Free My TV home screen. The TV only ever sees this summary; the API token stays here. See HOMELAB.md.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	_ "time/tzdata" // the container image has no time zone files; "updated" is in TZ's local time
)

func main() {
	envFile := flag.String("env", ".env", "file with KEY=value lines; real environment variables win")
	health := flag.Bool("healthcheck", false, "ask the running server's /healthz and exit 0 if it answers ok (for Docker)")
	flag.Parse()
	if *health {
		os.Exit(healthcheck(env("LISTEN", ":8099")))
	}
	if err := loadEnv(*envFile); err != nil && !os.IsNotExist(err) {
		log.Fatalf("reading %s: %v", *envFile, err)
	}

	tokenID, secret := os.Getenv("PVE_TOKEN_ID"), os.Getenv("PVE_TOKEN_SECRET")
	if tokenID == "" || secret == "" {
		log.Fatal("PVE_TOKEN_ID and PVE_TOKEN_SECRET must be set")
	}
	caPEM, err := os.ReadFile(env("PVE_CA_FILE", "certs/pve-root-ca.pem"))
	if err != nil {
		log.Fatalf("Proxmox CA: %v", err)
	}
	client, err := NewProxmoxClient(caPEM)
	if err != nil {
		log.Fatal(err)
	}

	pve := &Proxmox{Hosts: list(env("PVE_HOSTS", "")), TokenID: tokenID, Secret: secret, Client: client}
	if len(pve.Hosts) == 0 {
		log.Fatal("PVE_HOSTS must list at least one node, e.g. 10.0.0.133:8006,10.0.0.134:8006")
	}
	nut := &NUT{Addr: env("NUT_ADDR", "10.0.0.133:3493"), UPS: env("NUT_UPS", "cp1500")}
	hw := &Hwmon{Root: env("HWMON_ROOT", "/sys/class/hwmon")}
	src := Sources{Cluster: pve.Fetch, UPS: nut.Fetch, Temps: hw.Read, TempNode: env("HWMON_NODE", "pve"), Storage: env("PVE_STORAGE", "tank")}

	cache := &Cache{TTL: 15 * time.Second, Fetch: func() Stats {
		s := Collect(context.Background(), src, time.Now())
		for _, e := range s.Errors {
			log.Print(e)
		}
		return s
	}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(cache.Get(time.Now()))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	addr := env("LISTEN", ":8099")
	log.Printf("listening on %s; Proxmox via %s", addr, strings.Join(pve.Hosts, ", "))
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

// healthcheck is Docker's HEALTHCHECK: the image has no shell or curl, so the binary asks itself.
func healthcheck(listen string) int {
	_, port, _ := strings.Cut(listen, ":")
	c := &http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func list(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// loadEnv sets KEY=value lines from a file, skipping keys already in the environment (Docker's env_file and
// -e flags win). It never logs values.
func loadEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, value)
		}
	}
	return sc.Err()
}
