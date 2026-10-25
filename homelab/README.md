# Homelab card: the aggregator

A small Go service that summarises the lab for the Free My TV home screen. It reads the Proxmox cluster, the UPS
and pve's temperatures, and serves one JSON document (contract v1, `HOMELAB.md` §5) at `GET /stats`. The TV
polls it every minute while the home screen is showing.

```
Proxmox API (any node) ─┐
NUT on pve :3493 ───────┼─► aggregator :8099 ─► NPM ─► https://labstats.dominiktm.net/stats ◄── TV
/sys/class/hwmon ───────┘
```

## Why it's built this way

- **One service between the TV and the lab.** The Proxmox token stays on the server; the TV only ever sees
  the summary. The numbers are rounded for display here, so the TV does no maths.
- **Any Proxmox node will do.** Every node holds the whole cluster's state, so the aggregator tries the nodes
  in `PVE_HOSTS` in order and uses the first that answers. With pve down, pve2 gives the same picture.
- **TLS against the cluster's own CA** (`certs/pve-root-ca.pem`), never "skip verification". Proxmox signs
  each node's certificate with that CA, so checking against it proves we're talking to the cluster, and nobody
  else on the network can collect the token.
- **Partial failure is normal.** Proxmox, NUT and the sensors are read at the same time, each with its own
  deadline. One failing nulls only its own section and adds a short reason to `errors`.
- **A 15 s cache** in front, so several clients (or a refresh-happy browser) don't each hit the API.
- **Temperatures come from sysfs.** `/sys/class/hwmon` isn't namespaced in LXC, so a container in a guest on
  pve sees pve's own sensors. Chips are found by name (`coretemp`, `nvme`, `r8169…`) every time, because the
  `hwmonN` numbers change between reboots. pve2 and pve3 have no source yet: `null`, shown as "—".

## Settings

From the environment, or from `.env` in the working folder (the real environment wins).

| Variable | Default | |
|---|---|---|
| `PVE_TOKEN_ID` | (required) | `monitor@pve!tv`, role PVEAuditor (read-only) |
| `PVE_TOKEN_SECRET` | (required) | secret: only in `.env` / the stack's environment, never in git |
| `PVE_HOSTS` | (required) | nodes to try in order, `host:port,host:port,…` |
| `PVE_CA_FILE` | `certs/pve-root-ca.pem` | the cluster's root CA |
| `PVE_STORAGE` | `tank` | storage id behind `tank_used_pct` |
| `NUT_ADDR` | `10.0.0.133:3493` | upsd |
| `NUT_UPS` | `cp1500` | UPS name in upsd |
| `HWMON_ROOT` | `/sys/class/hwmon` | |
| `HWMON_NODE` | `pve` | the node whose sensors this container sees (the one its guest runs on) |
| `LISTEN` | `:8099` | |
| `TZ` | UTC | time zone of `updated`, e.g. `America/Toronto` |

## Run it locally (on 104)

```bash
cd homelab/aggregator
go test ./...
PVE_HOSTS=10.0.0.133:8006,10.0.0.134:8006,10.0.0.135:8006 TZ=America/Toronto go run .
curl -s localhost:8099/stats | jq
curl -s localhost:8099/healthz      # ok
```

## The image

`aggregator/Dockerfile` builds in two stages: a Go image compiles a static binary, which then goes alone into
`distroless/static:nonroot`. That image has no shell or package manager and runs as uid 65532, so there's very
little inside to attack. The result is about 10 MB. `.dockerignore` lets only the Go files and the CA into the
build, so the `.env` with the token can never end up in an image layer. The healthcheck is the binary itself
(`/aggregator -healthcheck`), since there's no curl in the image.

## Deploy (LXC 100, Portainer + NPM)

Portainer builds the image straight from this public repo (a *Repository* stack), so there's no registry to run.
The token's secret goes into the stack's environment variables in Portainer, never into git.

### 1. Merge to `main`

Portainer builds from `main`, so merge the aggregator PRs first.

### 2. Create the stack in Portainer

1. **Stacks → Add stack.** Name: `homelab`.
2. **Build method: Repository.**
   - Repository URL: `https://github.com/dominik-merdzik/free-my-tv`
   - Repository reference: `refs/heads/main`
   - Compose path: `homelab/deploy/compose.yaml`
   - Authentication: off (the repo is public). GitOps updates: off for now.
3. **Environment variables → Add an environment variable:** name `PVE_TOKEN_SECRET`, value = the secret
   (it's in `homelab/aggregator/.env` on 104; copy it from there yourself).
4. **Deploy the stack.** The first build takes a minute (it downloads the Go image).

**Check:**
- Containers → `homelab-aggregator`: *running*, then *healthy* after about 30 s.
- Its logs: `listening on :8099; Proxmox via 10.0.0.133:8006, …` and no error lines.
- On LXC 100 itself (the port is bound to loopback, so it isn't reachable from the LAN; that's the point):
  ```bash
  curl -s http://127.0.0.1:8099/healthz      # ok
  curl -s http://127.0.0.1:8099/stats | jq '{source_node, errors, pve_temp: .nodes[0].temp_c}'
  ```
  (No `jq` there? Drop the `| jq …` and read the raw JSON, or check through NPM after step 3.)
  `errors` should be `[]` and `pve_temp` a number. A number proves the container sees pve's sensors.
- `proxmox: no node answered (… HTTP 401 …)` in `errors` means the secret is wrong: fix the variable in the
  stack (Editor tab) and *Update the stack*.

### 3. Add the proxy host in NPM

*Why HTTPS:* Android 9+ refuses plain HTTP by default, and the app keeps that rule. NPM ends TLS with the
Let's Encrypt certificate and passes the request to the container on `127.0.0.1:8099`. That works because NPM
runs with host networking on LXC 100: its "localhost" is the host's, where the port is published.

1. **Hosts → Proxy Hosts → Add Proxy Host.**
   - Domain Names: `labstats.dominiktm.net`
   - Scheme `http`, Forward Hostname / IP `127.0.0.1`, Forward Port `8099`
   - Block Common Exploits: on. Cache Assets and Websockets Support: off.
2. **SSL tab:** pick the existing `*.dominiktm.net` Let's Encrypt certificate (DNS challenge). If there isn't
   one, *Request a new SSL Certificate* with *Use a DNS Challenge*: an HTTP challenge can't work for a name
   that only resolves on the LAN. Turn on **Force SSL** and **HTTP/2 Support**. Leave HSTS off for now: browsers
   remember it for months.
3. **Save.**

**Check** (from the LAN or over Tailscale, and without `-k`: the certificate has to be trusted as it is):
```bash
curl -s https://labstats.dominiktm.net/stats | jq '.version, .errors'     # 1, []
curl -s -o /dev/null -w '%{http_code}\n' http://labstats.dominiktm.net/stats   # 301: Force SSL works
echo | openssl s_client -connect labstats.dominiktm.net:443 -servername labstats.dominiktm.net 2>/dev/null \
  | openssl x509 -noout -issuer -enddate                                  # Let's Encrypt, a future date
```

### Updating and rolling back

- **Update:** after a merge to `main`, Stacks → `homelab` → **Pull and redeploy**. Portainer clones the repo
  again and rebuilds.
- **Roll back / remove:** stop or delete the stack. Nothing else depends on it; the TV card just shows
  *stale*.
- Port 8099 is published on `127.0.0.1` only, so plain HTTP never leaves LXC 100. If NPM ever stops using host
  networking, it can't reach loopback any more: put both containers on one Docker network instead.
