# spoolroom-tsnet

A small helper that lets a Spoolroom watcher be reached from the member's own
devices over the member's own Tailscale network. It uses Tailscale
(`tailscale.com/tsnet`), so the watcher's computer needs no Tailscale app, no
admin rights and no network driver.

The member owns the Tailscale account and the network. Spoolroom never
provides, runs or supports it. This project is not made, endorsed or supported
by Tailscale Inc.

## What it does

- Joins the member's tailnet as its own device, `spoolroom-watcher-<4 chars>`,
  after the member signs in through a link.
- Serves HTTPS on tailnet port 443 with the certificate Tailscale issues for
  that device name.
- Forwards every request to one address only: the watcher's gateway on
  `127.0.0.1:<port>`. Streams (MJPEG, HTTP-FLV, server-sent events) pass
  through unbuffered.

What it never does:

- use Funnel;
- use auth keys;
- upload logs to Tailscale.

### Listening sockets

The helper listens on the tailnet only: HTTPS on tailnet port 443, proxied
to the watcher's loopback gateway. It opens no TCP port on the computer's own
network interfaces.

Release builds leave out Tailscale's peer API (`ts_omit_peerapiserver`,
`ts_omit_peerapiclient`), which tsnet would otherwise bind on every host
interface once running. The helper uses none of its features (Taildrop,
peer DNS), and the device runs confirmed serving, streaming and sign-in work
without it (PROOF.md, Part 3). Because nothing listens on the host network,
no firewall prompt appears.

## Running it

The watcher starts the helper as a child process:

```
spoolroom-tsnet --dir <state dir> --target http://127.0.0.1:<gateway port> [--paused]
```

| Flag | Meaning |
|---|---|
| `--dir` | State directory. Must be a real directory, not a symlink. It is restricted to the current user: mode 0700, or on Windows a protected ACL for the user and SYSTEM only. It holds the device's private key: never copy or share it. |
| `--target` | `http://127.0.0.1:<port>` or `http://[::1]:<port>`. Nothing else is accepted. |
| `--paused` | Start in state `paused`, without contacting Tailscale, until a `resume` command arrives. |
| `--hostname` | Device name. Default `spoolroom-watcher-<4 random chars>`, kept in `--dir`. |
| `--expiry-warn` | Report `key_expiring` this long before the device key expires (default 336h). |
| `--verbose` | Send backend logs to stderr. Links, credentials and keys are scrubbed. |
| `--control-url` | Test only: coordination server URL. Never set it in production. |
| `--version`, `--licenses` | Print and exit. |

Logs go to stderr. Stdout carries only the protocol.

## Supervisor contract (for the watcher)

1. **Spawn** with stdin, stdout and stderr as pipes, and an environment holding only `PATH` and `HOME`/`USERPROFILE`.
2. **Paused state.** If the member's intent is "off", spawn with `--paused`.
   - tsnet re-enables the device on every start, so a pause sent to a previous process does not survive a restart.
   - Without `--paused`, a restarted helper comes back online.
3. **Stopping.** Close stdin to stop the helper. It exits 0 within a second and keeps the sign-in. Kill it only if it is still running 5 s after stdin closed, and **never while a logout is in progress.**
4. **Logout can take about 35 s:** up to 15 s to reconnect a paused device, then up to 20 s for Tailscale to confirm.
   - Wait for the `signed_out` event, or for the process to exit.
   - If Tailscale could not be reached, `signed_out` carries `ok:false`. The local key is still deleted, and the device stays in the member's admin console until they remove it.
5. **Restarts.** Restart on unexpected exits with backoff (plan §2.3).
   - Exit code 2 means bad flags: do not retry.
   - Exit code 1 means the node could not start.
6. **One helper per `--dir`.**
7. **Sign-in link.** Show `loginUrl` only to the workspace owner, and never log it.

## Protocol

One JSON object per line, each with `"v":1`.

**Events on stdout**

| `type` | Fields |
|---|---|
| `hello` | `version`, `tailscale` |
| `state` | `state` plus the fields below |
| `ack` | `cmd`, `re` (the command's `id`), `ok`, `detail` |
| `check` | `ok`, `status` (HTTP status from the gateway), `code`, `detail` |
| `error` | `code`, `detail` (at most 200 characters, never a link) |

| `state` | Meaning | Extra fields |
|---|---|---|
| `starting` | Coming up, or waiting for a sign-in link | |
| `control_unreachable` | Still starting after 45 s: Tailscale's coordination server is not answering | `code` |
| `needs_login` | Waiting for the member to sign in | `loginUrl` |
| `needs_approval` | The tailnet requires device approval | |
| `tailnet_locked` | Tailnet Lock is on and this device is not signed yet | `code` |
| `magicdns_off` | MagicDNS is off in the tailnet | `need: "magicdns"` |
| `https_off` | HTTPS certificates are off in the tailnet | `need: "https_certificates"`, `dnsName`, `https: false`, `consentUrl` if Tailscale offers one |
| `cert_pending` | First certificate being issued | `dnsName`, `https: true` |
| `running` | Serving | `url`, `dnsName`, `https: true`, `keyExpiry` |
| `key_expiring` | Serving, but the device key expires soon | as `running` |
| `paused` | Not serving; device offline; sign-in kept | |
| `signed_out` | Signed out and state wiped; the helper exits | `ok` (whether Tailscale confirmed), `code`, `detail` |
| `error` | Could not start | `code`, `detail` |

`loginUrl` and `consentUrl` are reported only if they are `https` links on
Tailscale's login host. Anything else is withheld and reported once as
`bad_auth_host` or `bad_consent_host`, with the host only. Treat `loginUrl`
like a password: whoever signs in with it adds this device to their tailnet.

**Error codes**

- General: `start_failed`, `backend`, `status`, `cert_failed`, `rate_limited`, `listen_failed`, `bad_auth_host`, `bad_consent_host`.
- Input: `bad_command`, `too_long` (a line over 4 KiB; skipped), `stdin`.
- `check`: `not_running`, `self_dial_failed`, `proxy_target_down`, `gateway_error`.
- Sign-out: `remote_logout_failed`, `wipe_failed`.

**Commands on stdin**: `{"cmd":"<name>","id":"<optional>"}`

| `cmd` | Effect |
|---|---|
| `status` | Re-send the current state |
| `login` | Ask Tailscale for a fresh sign-in link |
| `pause` | Stop serving, end every open stream and disconnect; keep the sign-in |
| `resume` | Reconnect and serve again (or start for the first time after `--paused`) |
| `check` | Fetch `/v1/info` through the device's own tailnet address |
| `logout` | Sign out at Tailscale, delete the state directory, exit 0 |

Closing stdin makes the helper exit cleanly without signing out. That is how
it notices the watcher has gone, on every platform.

## Proxy rules

- Only the configured loopback port is ever dialed. Proxy environment variables are ignored.
- **Refused:**
  - CONNECT, TRACE and any `Upgrade` (the gateway has no WebSocket endpoint);
  - inbound `Forwarded`/`X-Forwarded-*`/`X-Real-IP` and every `Tailscale-*` header, including `_` spellings.
- **Set on the request to the gateway:**
  - `Host` becomes the gateway's address;
  - `X-Forwarded-Proto: https`;
  - `X-Forwarded-Host` carries the tailnet name.
- **Responses:**
  - a `Location` pointing at the loopback address is rewritten to the tailnet name;
  - every response gets `X-Content-Type-Options: nosniff`.
- **Limits:**
  - 128 concurrent tailnet connections and 64 to the gateway;
  - request headers within 10 s;
  - a request body within 30 s;
  - no limit on how long a response streams.

## Building

Go is pinned by the `go 1.27.3` line in `go.mod`; CI installs exactly that version. Tailscale is pinned by `go.mod` and `go.sum`, and the manifest records the Go version used.

```
./build.sh                       # dist/: six binaries, .gz, SHA256SUMS, manifest.json
scripts/verify-repro.sh          # builds twice (second with an empty cache); fails if any hash differs
scripts/print-pins.sh            # the sha256 table the watcher pins
scripts/gen-notices.sh           # regenerate THIRD_PARTY_NOTICES.md
scripts/build-proof-tools.sh     # binaries for the PROOF.md device runs
go vet ./... && go test -race ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
```

**Targets:** `darwin-arm64`, `darwin-x64`, `linux-x64`, `linux-arm64`, `linux-arm` (ARMv7) and `win32-x64`. They are named as Node reports `process.platform-process.arch`.

**Flags:** every build is `CGO_ENABLED=0 go build -trimpath -buildvcs=false` with `-ldflags "-s -w -buildid="`. Tailscale's version is stamped too (`1.104.2-t<commit>`), so Tailscale's coordination server sees a real client version. Anyone can rebuild a release and compare the hashes.

**Download:** the watcher fetches the `.gz` asset and checks the size and sha256 of the decompressed binary against the pins in its own release before running it.

**Release repo:** when the public repo is created, protect `v*` tags with a tag ruleset, because pushing a tag publishes a release (`.github/workflows/release.yml`).

## License

MIT for this project's code; see `LICENSE`. It uses Tailscale, which is
BSD-3-Clause; that notice is in `LICENSE`. The notices for the Go standard
library and every other linked module are in `THIRD_PARTY_NOTICES.md`.
