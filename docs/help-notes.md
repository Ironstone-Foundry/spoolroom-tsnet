# Notes for the Spoolroom help page (remote access, built in)

Facts the help copy must reflect. Each line says whether it is verified or still to be checked by a numbered proof in PROOF.md.

- **No firewall prompt.** The release build opens no port on the computer's own network, so Windows and macOS have nothing to ask about. *Verified on Linux, in Docker and on Windows 11 (Part 3).*
- **A second device.** If this computer already runs the Tailscale app, your Tailscale admin console shows two devices. That is expected. *P5.*
- **Turning off.**
  - "Turn off" keeps the device signed in but offline.
  - "Sign out of Tailscale" signs the device out and deletes its key from the computer. The device then shows as **Expired** in your Tailscale admin until you remove it (Machines → ⋯ → Remove). *Verified, P6 (Part 3).*
- **Signing out takes up to about 35 seconds.**
- **The device name is public.** `spoolroom-watcher-xxxx.<your tailnet>.ts.net` appears in public certificate logs. It contains nothing about you or your printers.
