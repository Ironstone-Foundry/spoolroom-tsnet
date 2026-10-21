# Draft upstream issue for tailscale/tailscale (not filed)

**Title:** tsnet: peerapi opens a kernel TCP listener on all interfaces even though netstack intercepts peerapi

**Body:**

A tsnet node binds its peerapi port on every host interface once it is Running. This happens even though tsnet always uses netstack, and netstack already intercepts peerapi connections addressed to the node's Tailscale IPs.

For an app that embeds tsnet to put a single loopback service on the tailnet, this is the only TCP socket it opens on the host's LAN (the WireGuard UDP socket is expected). An app author can't avoid it with an option.

**Where (v1.104.2)**

- `ipn/ipnlocal/peerapi.go`, `peerAPIServer.listen`: when `s.b.sys.IsNetstack()` is true, `ipStr` is set to `""`, so the listener binds `:<port>` on all interfaces. On macOS, `initListenConfig` also clears `ipStr`.
- `skipKernelListener` returns true only for Android, and for FreeBSD when netstack is present. On those platforms `newFakePeerAPIListener` is used, and peers are served through netstack interception.
- `fakePeerAPIListener`'s own comment says the kernel listener is unnecessary "on almost all platforms (except iOS)", because netstack intercepts peerapi traffic first.

**Observed** (reported by our code review; re-checked in our PROOF.md runs)

On a tsnet node in the Running state, `lsof` shows `TCP *:<peerapi port> (LISTEN)` owned by the embedding process.

- A TCP handshake from a LAN host succeeds.
- The request is then rejected only by the WhoIs lookup in the peerapi handler.

**Request**

Return `newFakePeerAPIListener(ip)` from `listen` (or make `skipKernelListener` return true) whenever netstack is present and the backend is tsnet, or when there is no TUN device (`sys.IsNetstack()`).

The kernel listener is only needed when there is no netstack interception: iOS, and kernel-TUN tailscaled. Alternatively, add a `tsnet.Server` field to opt out.

**Workaround in the meantime**

Building with `ts_omit_peerapiserver` removes the listener, along with all peerapi features (Taildrop, peer DNS for exit nodes, and others). spoolroom-tsnet uses none of them. Whether anything else breaks is checked in PROOF.md (variant B).

**Why it matters**

- On Windows and macOS this listener triggers a firewall prompt for an app that never meant to accept LAN connections.
- It also widens the attack surface to the LAN for the length of the session.
