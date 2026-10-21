# Proofs for spoolroom-tsnet

This file is in two parts:

1. What was proven locally on 2026-10-20 without joining any tailnet.
2. The device runs on a real tailnet: what each run checks, and the results.

---

## Part 1: local results (2026-10-20 and 2026-10-21, no tailnet joined)

**Setup**

- Machine: Apple silicon Mac, macOS 27.0.
- Go 1.27.3 (go.dev tarball, sha256 verified). tailscale.com v1.104.2.
- Binary: `dist/spoolroom-tsnet-darwin-arm64` from `./build.sh`, helper version 0.1.0.
- Harness: `python3 -I scripts/proof-local.py <binary> <scratch dir> 3`.
  - It never prints a sign-in link, only the link's host.
  - Nobody signed in. Every process was stopped or signed out after the link appeared.

**Results**

**Re-run 2026-10-21, after the review fixes** (same harness; numbers below are for this build)

- Pins changed: the review fixes, x/net 0.61.0 and the Tailscale version stamp. `scripts/print-pins.sh` is the source of truth.
- Reproducibility: all 12 hashes matched again, with the second build using an empty cache.
- Cold start to `needs_login`: 0.81, 1.09 and 1.22 s. Warm start: 0.81 s.
- Idle RSS while in `needs_login`: 19.5 to 26.4 MB.
- Sockets: no TCP listener, and the only peer was Tailscale's control plane. That is expected while signed out; peerapi only opens once Running (see README, "Listening sockets").
- **`--paused` restart (review M2):**
  - The sequence was `pause`, close stdin (exit 0), then restart with `--paused`.
  - The first state was `paused` after 0.02 s.
  - Over 5 s: **no connection to anyone, no listener**, RSS 11.1 MB.
  - The node is never started, so nothing can reach the tailnet.
- Lifecycle:
  - `pause` → `paused`;
  - `resume` → `needs_login` (same link);
  - `login` → a new link;
  - `logout` → `signed_out ok:true`, exit 0, state dir removed.
- The helper reported `client version 1.104.2-te26db7a57` (stamped).
- `govulncheck` v1.9.0: no vulnerabilities in called code. One module-level advisory, GO-2026-5932 (`x/crypto/openpgp`), concerns a package that is not imported.

**Binary sizes** (2026-10-21 build, stripped, with the omit tags in `build.sh`)

| Target | Binary | `.gz` (what the watcher downloads) |
|---|---|---|
| darwin-arm64 | 19.34 MB | 7.50 MB |
| darwin-x64 | 20.74 MB | 8.07 MB |
| linux-x64 | 20.93 MB | 8.20 MB |
| linux-arm64 | 19.40 MB | 7.45 MB |
| linux-arm (ARMv7) | 19.79 MB | 7.79 MB |
| win32-x64 | 21.18 MB | 8.25 MB |

The first build (2026-10-20) was within 0.1 MB of these. The 2026-10-20 measurements below are kept for the size comparisons and the timing spread.

- These are smaller than the plan's estimate of about 25 MB and 10 MB.
- On darwin-arm64:
  - without `-s -w` the binary is 28.23 MB, 46 % larger;
  - without the omit tags it is 20.66 MB, so the tags save 1.35 MB (7 %).
- The darwin-arm64 binary is ad-hoc, linker-signed (`codesign -dv`: `flags=0x20002(adhoc,linker-signed)`) and runs.
- The darwin-x64, Linux and Windows binaries were built but **not run**. This Mac has no Rosetta, Docker or VM. That is still P4.

**Reproducible build**

- `scripts/verify-repro.sh` built all six targets twice, the second time with an empty `GOCACHE`. All 12 hashes matched (six binaries and six `.gz`).
- A rebuild after a later code change was again stable across a whitespace-only `gofmt` pass.
- **Not yet shown:** a match across two different machines, for example this Mac against the CI Linux runner. The release workflow repeats the check on Linux.

**Cold start to `needs_login`** (process spawn to the stdout line carrying the link; 9 runs over three batches, each with a fresh state dir)

- Times: 0.80, 0.82, 0.83, 0.87, 0.87, 1.08, 3.86, 5.84 and 19.5 s. The median is 0.87 s.
- The slow runs were spent waiting on Tailscale's control server.
  - In the 19.5 s run, stderr shows `StartLoginInteractive` at +0 s and the first link 20 s later.
  - The helper itself is up in under 0.1 s.
- **P1's "link within 5 s" holds in 7 of 9 runs.** The app's "Starting…" copy must allow for control-plane delays of 20 s or more.
- Warm start (existing, still signed-out state dir): 0.84 and 0.95 s.

**Idle memory** (RSS after 30 s idle in `needs_login`)

- 18.5 to 29.7 MB over nine runs; the median was about 25 MB.
- Not measured: the `running` state with peers and streams, and memory on a Pi (P9).

**Network side effects** (sockets sampled every 0.5 s for 10 s while waiting in `needs_login`)

- The only remote peer was Tailscale's control plane: one IPv6 address in 2606:b740:49::/64. `controlplane.tailscale.com` and `login.tailscale.com` resolve into the same range.
- There was **no** connection to `log.tailscale.com`, and no DERP connection.
- There was **no TCP listening socket** on the host in any run; only the UDP WireGuard socket. This is the signed-out state only: once Running, tsnet's peerapi opens one TCP listener on all interfaces (review M1; README "Listening sockets"; checked in 2.2.5).
- Nothing joined a tailnet. The link was never opened.
- Each run left an unused pending registration at Tailscale, which is never completed.

**The sign-in link**

- Every run produced a link, always on `login.tailscale.com`.
- The helper's stderr never contained a link: URLs are scrubbed, and tsnet's repeating "To start this tsnet server … go to:" hint is dropped.
- `resume` after `pause` while signed out gave the **same** link.
- `{"cmd":"login"}` gave a **new** link.

**State directory**

- Created with mode `0700`.
- Contents while signed out:
  - `.spoolroom-tsnet` (marker);
  - `hostname-suffix`;
  - `tailscaled.state`;
  - `tailscaled.log.conf`.
- tsnet still writes `tailscaled.log.conf`, which holds a log ID, even with logtail compiled out. Nothing is uploaded.
- The default device name looked like `spoolroom-watcher-btt6`.

**Lifecycle on the real binary** (signed out, so control was never asked to log out a node)

- `pause` gave `paused`.
- `resume` gave `needs_login`.
- `login` gave a fresh `needs_login`.
- `logout` gave `{"state":"signed_out","ok":true}`, exit 0, and the state dir was removed.
- Closing stdin exited in under 0.01 s with code 0 and **kept** `tailscaled.state`.

**Unit tests** (`go vet ./...` clean; `go test -race ./...` passes)

- **Proxy:**
  - the target allow-list (17 rejected forms, including `localhost`, LAN, CGNAT, other loopback addresses, paths and credentials);
  - the transport refuses any other dial;
  - an absolute-form request to `169.254.169.254` still lands on the configured target;
  - CONNECT is refused;
  - header rules;
  - a 502 that leaks no detail;
  - MJPEG, unbuffered over HTTP/1.1 and over HTTP/2 with TLS: each frame must arrive before the backend may produce the next;
  - SSE, unbuffered;
  - WHEP POST pass-through.
- **Protocol:**
  - one line per event, with `v:1`;
  - duplicate states dropped;
  - command parsing;
  - bad lines answered;
  - oversize lines refused;
  - links and keys scrubbed.
- **State derivation:** every state in the README table, plus foreign-host links dropped.
- **Controller** (fake node):
  - sign-in to running;
  - a fresh link on `login`;
  - HTTPS off then on without a restart, with the consent link queried once;
  - approval, then MagicDNS off;
  - `cert_pending` before any listener;
  - the rate limit honored;
  - `key_expiring`;
  - pause stops serving and resume restores it;
  - `check`;
  - logout from paused, and logout while control is unreachable (still wipes);
  - EOF exits without signing out.
- **State dir:**
  - mode `0700`, tightened if it was looser;
  - a foreign directory is refused and never wiped;
  - the hostname persists.

---

## Part 2: device runs on a real tailnet

Each run uses a throwaway test device with `tools/fakegateway` as the camera gateway and `tools/drive` as a stand-in supervisor (`scripts/build-proof-tools.sh` builds both, plus a variant without peerapi). The device is signed in once by the tailnet owner and removed afterwards. Runs never change the tailnet's settings (HTTPS, MagicDNS, ACLs, approval, key expiry), and the sign-in link goes only to the person signing in.

| Proof | Check |
|---|---|
| P1 | Sign-in from the `needs_login` link; device approval if the tailnet requires it |
| P2 | First-certificate latency (time in `cert_pending`) |
| P3 | MJPEG, SSE, FLV and WHEP through the proxy over HTTP/2, 15 minutes on a phone and a desktop browser |
| P4 | Headless Linux, Docker with `--cap-drop ALL` and no TUN, Windows (firewall prompt, state-dir ACL, Defender) |
| P5 | A device that already runs Tailscale |
| P6 | What the admin console shows after `logout` |
| P7 | The `check` self-test |
| P8 | No connection to the log server |
| P10 | Port scan from a tailnet peer; the peerapi listener |
| — | `--paused` across a restart stays offline; Upgrade refused; the no-peerapi variant |

The fresh-account run repeats this on a new tailnet, where HTTPS is likely off: it records the `https_off` state, whether a `consentUrl` arrives and what the consent page grants (Funnel included), and that the helper reaches `running` without a restart once HTTPS is on.

Results are recorded below as "Part 3: device runs, <date>", by proof number.

## Part 3: device runs, 2026-10-21

**Device A:** a Debian 13 LXC container (unprivileged, amd64), headless, then Docker inside it. Signed in once by the tailnet owner; removed afterwards. Tailscale v1.104.2.

| Proof | Result |
|---|---|
| P1 sign-in | The link from `needs_login` worked; no device approval was required on this tailnet. The admin console showed the device with client version 1.104.2. |
| P2 first certificate | 34 s in `cert_pending` on first sign-in; 0.3–0.4 s on later starts (cached). Let's Encrypt, `CN=<device>.ts.net`. Key expiry about 180 days out. |
| HTTP/2 | **Broken in the first build**: the connection cap wrapped the TLS listener, so h2 requests reached the server as `PRI *`. Fixed (cap below TLS) with a regression test; every check below ran on the fixed build. |
| P3 streaming | 15 min of MJPEG from a tailnet peer: 4,500 frames at a steady 5 fps, no stall. 15+ min on an iPhone (Safari) and desktop Chrome at the same time with no MJPEG reconnect; SSE reconnected twice on the phone (backgrounded page). SSE one event per second, FLV streamed, WHEP POST 201. |
| P4 Linux | Runs unprivileged; RSS about 24 MB idle, 27–28 MB while streaming. |
| P4 Docker | `--cap-drop ALL`, `no-new-privileges`, read-only root, no `/dev/net/tun`, no published ports: `running` on the existing sign-in, served HTTP/2, 14 MB. |
| P7 self-check | `{"type":"check","ok":true,"status":401}`. |
| P8 log upload | Over 40 s the helper connected only to the loopback gateway and Tailscale's control/DERP servers; never to `log.tailscale.com`. |
| P10 sockets | Default build: one TCP listener (peerapi), here bound to loopback inside the container. Port scan from a peer: only 443 open; the gateway port was not reachable. **No-peerapi build: no TCP listener at all**, everything above still works, peer connection direct. Release builds now leave peerapi out. |
| Upgrade | `Connection: Upgrade` → 400. |
| `--paused` | pause → `paused`, the URL stops answering, no listeners. Restart with `--paused`: first state `paused`, no listener, no connection to anyone. `resume` → `running` with no new sign-in, same name. |
| P6 logout | `ack ok`, `signed_out ok:true`, exit 0, state directory removed. The admin console then lists the device as **Expired** (not removed); the owner removes it. |

**Device B:** a Windows 11 VM (build 26200, Defender real-time protection on), release build without peerapi, run as SYSTEM through the guest agent. Signed in once by the tailnet owner; removed afterwards.

| Proof | Result |
|---|---|
| P1/P2 | Sign-in worked; 35 s in `cert_pending`, then `running`. |
| P3 | HTTP/2 401 on `/v1/info`; MJPEG 100 frames in 20 s (5 fps); SSE one event per second; WHEP 201. |
| P4 Windows | **No TCP listener at all**, UDP only (WireGuard). No firewall rule created and no prompt. Defender: no detection. State dir ACL: owner only. Working set about 76 MB. |
| P10 | Port scan from a peer: only 443 open (not 135/139/445/3389/5985 or the gateway port). |
| Upgrade | 400. |
| `--paused` | Restart with `--paused`: first state `paused`, no TCP or UDP endpoints, offline in the tailnet. `resume` → `running` with no new sign-in. |
| P6 logout | `signed_out ok:true`, exit 0, state directory removed. |

Not yet run: a user account (not SYSTEM) on Windows, a device that already runs Tailscale (P5), and the fresh-account HTTPS consent run.
