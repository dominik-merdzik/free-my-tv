# HOMELAB.md — Fork-specific context for Claude

This fork of `simbasjungles/free-my-tv` adds a **Homelab card** to the TV home screen, fed by a
small **aggregator** service. Upstream's `CLAUDE.md` still applies to the app itself; this file
covers everything this fork adds. On any conflict about *this fork's* additions, this file wins.

The owner is learning toward an infrastructure/server-admin career. **Explain the why** behind
design decisions and commands, briefly and in plain terms, as you work.

---

## 1. Environment (where you are running)

| | |
|---|---|
| Host | LXC 104 `lxc-claude` (10.0.0.104), Debian 13, user `dev` |
| sudo | Requires a password you do not have. Anything system-level is the human's job — say exactly what to run and why |
| Repo | `~/code/free-my-tv` (fork: `dominik-merdzik/free-my-tv`, upstream: `simbasjungles/free-my-tv`) |
| Android SDK | `~/android-sdk` — build-tools **34.0.0 and 37.0.0**. 37 is required: d8 from 34 crashes (NPE in `SettingsView$1`) on javac 21 output. `build.sh` auto-picks the newest |
| JDK | OpenJDK 21 |
| Go | `~/.local/go` (per-user install) |
| Network | Same flat /24 as everything else. No Docker here (by design) |

## 2. The TV

| | |
|---|---|
| Name / IP | "Basement TV" · **10.0.0.218** (DHCP reservation in UniFi) · on **Wi-Fi** (`wlan0`) |
| Platform | TCL Google TV, **Android 11** (no Android 13 shaders → plain-fill rendering path) |
| ADB | `adb connect 10.0.0.218:5555` — `dev`'s key (`~/.android/adbkey`) is already approved ("Always allow") |
| App | Free My TV, package `io.freemytv`, **currently the active home screen** |
| Signing | `freemytv.keystore` in the repo root. **Never delete, regenerate, move, or commit it.** Every update must be signed with it; losing it means uninstall + reinstall |
| TCL quirk | Background permission is `APP_AUTO_START` (not `AUTO_START`). Already set to `allow` |
| Install | `./build.sh && adb install -r FreeMyTV.apk`. On Android 11, adb tries *incremental* install first, fails ("not allowed"), and falls back to *streamed* → `Success`. That error is expected |

## 3. The lab (what the aggregator reads)

| Thing | Address | Notes |
|---|---|---|
| Proxmox cluster `satoru-cluster` | pve **10.0.0.133** · pve2 **.134** · pve3 **.135** — all `:8006` | **Any node answers cluster-wide API calls.** Try them in order; fail over |
| Proxmox CA | `homelab/aggregator/certs/pve-root-ca.pem` | Cluster's self-signed root. Verify TLS against it — never `InsecureSkipVerify` |
| API token | `monitor@pve!tv`, role **PVEAuditor** (read-only) | Secret lives in `homelab/aggregator/.env` (gitignored). Do not print it, echo it, or paste it into chat |
| NUT (UPS) | `10.0.0.133:3493`, UPS name `cp1500` | Plaintext protocol; `LIST VAR`/`GET VAR` need no login. Unavailable whenever pve is down — that's expected |
| Aggregator host | **LXC 100** (10.0.0.100), Docker via Portainer | **You cannot reach Docker on LXC 100 from here.** Deployment is a human step (§7) |
| Reverse proxy | NPM on LXC 100 | Will front the aggregator as `https://stats.dominiktm.net` (LAN/Tailscale only — wildcard DNS → 10.0.0.100) |

Guests: 100 `lxc-docker`, 102 `lxc-tailscale`, 103 `lxc-webstack`, 104 `lxc-claude` — all on pve.
**Discover guests from the API; never hardcode this list.**

### Temperatures — read this before implementing
- **pve CPU package temp:** `/sys/class/hwmon` is **not namespaced** in LXC, so a container on
  LXC 100 sees **pve's** sensors. Find the hwmon whose `name` is `coretemp`, read the
  `temp*_input` whose label is `Package id 0` (millidegrees → °C). **Resolve by name every time —
  `hwmonN` indices are not stable across reboots.**
- **Ignore the `nct6795` chip's AUXTIN channels** — floating inputs that read −128 °C / +127 °C.
  If `nct6795` is ever used, only trust labelled CPU/system channels.
- **pve2 / pve3: no source yet.** Emit `null`; the card shows "—". (Planned: Beszel agents.)
- This trick only works because the aggregator runs in a guest **on pve**. Document that in code.

## 4. Architecture

```
Proxmox API (any node) ─┐
NUT on pve :3493 ───────┼─► aggregator (Go, Docker on LXC 100, :8099) ─► NPM ─► https://stats.dominiktm.net/stats
/sys/class/hwmon ───────┘                                                           ▲
                                                             TV polls every 60 s ───┘
```

**Pull, not push.** Free My TV never listens on the network — keep it that way.
**Secrets stay server-side.** The TV only ever sees the summarised JSON.
**HTTPS via NPM, not plain HTTP.** Android 9+ blocks cleartext HTTP by default; NPM already has
LE certs (DNS-01) for the wildcard zone. Do not add a cleartext exception to the app.

## 5. JSON contract (v1) — `GET /stats`

Numbers are **pre-computed for display**: integer percents, GB to one decimal. The TV does no maths.

```json
{
  "version": 1,
  "updated": "2026-10-24T19:40:00-04:00",
  "source_node": "pve",
  "cluster": { "online": 3, "total": 3, "quorate": true },
  "overall": { "cpu_pct": 5, "mem_used_gb": 19.3, "mem_total_gb": 62.4, "mem_pct": 31 },
  "nodes": [
    { "name": "pve", "online": true, "cpu_pct": 13, "mem_used_gb": 15.6, "mem_total_gb": 31.3,
      "mem_pct": 50, "uptime_s": 123456, "temp_c": 47 }
  ],
  "guests": [
    { "vmid": 100, "name": "lxc-docker", "type": "lxc", "node": "pve", "status": "running",
      "cpu_pct": 9, "mem_used_gb": 12.1, "mem_total_gb": 21.0 }
  ],
  "storage": { "tank_used_pct": 27 },
  "ups": { "status": "OL", "charge_pct": 100, "runtime_min": 73, "load_pct": 10 },
  "errors": []
}
```

- **Partial failure is normal.** If NUT is down, `ups` is `null` and `errors` gets a short reason.
  Same for every section. Never fail the whole response because one source failed.
- `overall.cpu_pct` = weighted by each node's core count (`maxcpu`), not a plain average.
- Proxmox `cpu` is a 0–1 fraction; `mem`/`maxmem` are bytes. Convert server-side.
- Cache results ~15 s so multiple clients don't hammer the API.
- Bump `version` on any breaking change; the card must ignore unknown fields.
- `GET /healthz` → `200 ok` for Docker healthchecks.

## 6. The TV card

- Lives in the drawer where the **crypto card** goes (`HomeData.java` / `HomeView.java`). Crypto is
  off on this TV (`show_crypto=false`).
- New `config.properties` keys (add to `config.example.properties` with comments):
  `show_homelab=false` (default off — keep upstream behaviour unchanged) and
  `homelab_url=` (empty by default).
- Layout: overall CPU/RAM at top, one row per node (CPU, RAM, temp or "—"), then guests, then UPS
  and tank. Match the existing visual style. Android 11 → the plain-fill path; test that path.
- Poll every **60 s, only while the home screen/drawer is visible.** This TV is slow; don't run
  timers in the background.
- If `updated` is older than **3 minutes**, show the card as **stale** (dimmed + "stale"). A frozen
  number must never look like a healthy one.
- Network/parse failure → keep last good data, mark stale. Never crash the home screen — the
  watchdog reverts to stock after 3 crashes in 5 minutes.
- Add UI strings to `app/assets/i18n/en.json` and run `python3 tools/translate.py` to list gaps.

## 7. Repo layout & workflow

```
homelab/
  aggregator/          Go module (stdlib only unless there's a real reason)
    main.go …
    certs/pve-root-ca.pem   (public cert — safe to commit)
    .env                    (gitignored — PVE_TOKEN_ID, PVE_TOKEN_SECRET)
    Dockerfile              (multi-stage → distroless/static or scratch, non-root)
  deploy/compose.yaml       Portainer stack for LXC 100
  README.md                 how it works + how to deploy
```

- **Branches + PRs.** Never commit directly to `main`. Small, reviewable commits.
- **Keep upstream mergeable:** add new files rather than reshaping upstream ones; don't reformat
  code you didn't change. Sync with `git fetch upstream && git merge upstream/main` on a branch.
- **Run the aggregator locally first:** `go run ./homelab/aggregator` on 104 against the live API
  using `.env`, curl `localhost:8099/stats`, then ship.
- **Deploy (v1, human):** you produce `compose.yaml` + exact Portainer steps; the human deploys on
  LXC 100 and creates the NPM host. Later we may move to GHCR + Portainer git-stack auto-update.

## 8. Rules for touching the TV

Allowed freely: `adb connect/disconnect/devices`, `./build.sh`, `adb install -r FreeMyTV.apk`,
read-only queries (`getprop`, `dumpsys`, `appops get`, `pm list`, `logcat -d`).

**Ask first, every time:** anything that changes TV state — `pm disable/enable/uninstall/clear`,
`settings put`, `appops set`, `am force-stop` on other apps, reboots, editing `DEBLOAT` or
`PROTECTED` in `HomeMode.java`.

**Before installing:** the build must succeed and the change must be explained. After installing,
check `adb logcat -d | grep -i freemytv` for crashes and say what to look at on the TV.

**Rollback** is always available on the TV: Free My TV → Home Screen → **Original**.

## 9. Never

- Commit `.env`, `config.properties`, `freemytv.keystore`, or any token.
- Print or echo secrets, or put them in chat (transcripts are stored to sync devices).
- Use `InsecureSkipVerify` / `curl -k` in shipped code.
- Hardcode guest lists, `hwmonN` paths, or a single Proxmox node.
- Run `sudo`. Hand the human the command and explain it.
