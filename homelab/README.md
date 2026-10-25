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
