#!/usr/bin/env python3
"""Slice 0 local proofs on a real helper binary, without joining a tailnet.

Starts the helper with a fresh state dir, waits for needs_login, and
records: cold-start time, that a sign-in link exists (its host only, never
the link), idle memory, sockets (no TCP listener; remote peers), the state
dir's mode and files. Then exercises pause, resume, login and logout, and
checks logout wipes the state dir. Nobody signs in; nothing joins a tailnet.

Usage: python3 -I scripts/proof-local.py <binary> <scratch dir> [runs]
"""
import json, os, socket, stat, subprocess, sys, time, urllib.parse

BIN, SCRATCH = sys.argv[1], sys.argv[2]
RUNS = int(sys.argv[3]) if len(sys.argv) > 3 else 3


def rss_kb(pid):
    out = subprocess.run(["ps", "-o", "rss=", "-p", str(pid)], capture_output=True, text=True).stdout
    return int(out.strip() or 0)


def sockets(pid):
    out = subprocess.run(["lsof", "-nP", "-a", "-p", str(pid), "-i"], capture_output=True, text=True).stdout
    return [l for l in out.splitlines()[1:]]


class Helper:
    def __init__(self, state, extra=()):
        self.t0 = time.monotonic()
        self.p = subprocess.Popen(
            [BIN, "--dir", state, "--target", "http://127.0.0.1:9", *extra],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, bufsize=1, env={"PATH": os.environ.get("PATH", ""), "HOME": SCRATCH},
        )
        self.events = []

    def send(self, cmd):
        self.p.stdin.write(json.dumps({"cmd": cmd, "id": cmd}) + "\n")
        self.p.stdin.flush()

    def wait(self, pred, timeout=30):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            line = self.p.stdout.readline()
            if not line:
                return None
            ev = json.loads(line)
            self.events.append(ev)
            if pred(ev):
                return ev
        return None

    def state(self, *names, timeout=30):
        return self.wait(lambda e: e.get("type") == "state" and e.get("state") in names, timeout)


def redact(ev):
    ev = dict(ev)
    for k in ("loginUrl", "consentUrl"):
        if k in ev:
            ev[k] = "<%s link>" % urllib.parse.urlsplit(ev[k]).hostname
    return ev


def resolve(names):
    ips = {}
    for n in names:
        try:
            for ai in socket.getaddrinfo(n, 443, proto=socket.IPPROTO_TCP):
                ips[ai[4][0]] = n
        except OSError:
            pass
    return ips


known = resolve(["controlplane.tailscale.com", "login.tailscale.com", "log.tailscale.com", "log.tailscale.io"])
results = []
for i in range(RUNS):
    state = os.path.join(SCRATCH, "state-%d" % i)
    h = Helper(state)
    ev = h.state("needs_login")
    if not ev:
        print("run %d: no needs_login; events: %s" % (i, [redact(e) for e in h.events]))
        h.p.kill(); sys.exit(1)
    cold = time.monotonic() - h.t0
    host = urllib.parse.urlsplit(ev["loginUrl"]).hostname
    first_link = ev["loginUrl"]  # compared only, never printed
    peers, listen = set(), []
    for _ in range(20):  # 10 s of socket samples while idle in needs_login
        for l in sockets(h.p.pid):
            if "LISTEN" in l:
                listen.append(l)
            if "->" in l:
                peers.add(l.split("->")[1].split()[0].rsplit(":", 1)[0].strip("[]"))
        time.sleep(0.5)
    time.sleep(20)
    rss = rss_kb(h.p.pid)
    mode = oct(stat.S_IMODE(os.stat(state).st_mode))
    files = sorted(os.listdir(state))
    r = {"run": i, "coldStartS": round(cold, 2), "loginLinkHost": host, "idleRssMB": round(rss / 1024, 1),
         "tcpListeners": len(listen), "peers": {p: known.get(p, "other") for p in sorted(peers)},
         "dirMode": mode, "dirFiles": files}

    if i == 0:  # lifecycle on the real binary, still signed out
        steps = []
        h.send("pause"); steps.append(("pause", h.state("paused", timeout=10)))
        h.send("resume"); steps.append(("resume", h.state("needs_login", "starting", timeout=15)))
        h.send("login"); steps.append(("login", h.state("needs_login", timeout=15)))
        h.send("logout"); steps.append(("logout", h.state("signed_out", timeout=30)))
        code = h.p.wait(timeout=15)
        r["lifecycle"] = [(n, redact(e) if e else None) for n, e in steps]
        links = [e.get("loginUrl") for n, e in steps if e and e.get("loginUrl")]
        r["linkChangedAfterResume"] = bool(links) and links[0] != first_link
        r["linkChangedAfterLogin"] = len(links) > 1 and links[1] != links[0]
        r["exitAfterLogout"] = code
        r["dirAfterLogout"] = os.path.exists(state)
    else:  # EOF must exit cleanly and keep state
        if i == 1:  # pause first; the restart below must honour --paused
            h.send("pause"); r["pausedBeforeEOF"] = bool(h.state("paused", timeout=10))
        t = time.monotonic(); h.p.stdin.close(); code = h.p.wait(timeout=15)
        r["exitOnEOF"], r["eofExitS"], r["stateKept"] = code, round(time.monotonic() - t, 2), os.path.exists(os.path.join(state, "tailscaled.state"))
        if i == 1:  # restart with --paused: no Tailscale contact, no listener
            hp = Helper(state, ["--paused"])
            ev = hp.state("paused", "starting", "needs_login", timeout=10)
            r["pausedRestartState"] = ev and ev.get("state")
            r["pausedRestartS"] = round(time.monotonic() - hp.t0, 2)
            pp, pl = set(), 0
            for _ in range(10):
                for l in sockets(hp.p.pid):
                    pl += "LISTEN" in l
                    if "->" in l:
                        pp.add(l.split("->")[1].split()[0])
                time.sleep(0.5)
            r["pausedRestartPeers"], r["pausedRestartListeners"] = sorted(pp), pl
            r["pausedRestartRssMB"] = round(rss_kb(hp.p.pid) / 1024, 1)
            hp.p.stdin.close(); hp.p.wait(timeout=15)
        if i == 1:  # warm start from the kept, still signed-out state
            h2 = Helper(state)
            ev = h2.state("needs_login")
            r["warmStartS"] = round(time.monotonic() - h2.t0, 2) if ev else None
            h2.p.stdin.close(); h2.p.wait(timeout=15)
    stderr = h.p.stderr.read()
    with open(os.path.join(SCRATCH, "stderr-%d.txt" % i), "w") as f:
        f.write(stderr)
    r["stderrHasURL"] = "://" in stderr
    r["stderrLines"] = len(stderr.splitlines())
    results.append(r)
    print(json.dumps(r, indent=1))
