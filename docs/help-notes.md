# Notes for the Spoolroom help page (remote access, built in)

Facts the help copy must reflect. Each line says whether it is verified or still to be checked by a numbered proof in PROOF.md.

- **Firewall prompt.**
  - On Windows, Windows Defender Firewall may ask whether to allow `spoolroom-tsnet` on the network.
  - On a Mac with the macOS firewall on, macOS may ask whether `spoolroom-tsnet` may accept incoming connections.
  - **Choosing "Deny" or "Cancel" is fine.** Cameras still work over Tailscale, because the helper connects out and does not need incoming connections from your local network.
  - *To verify in PROOF.md P4/P10 before the copy ships.*
- **A second device.** If this computer already runs the Tailscale app, your Tailscale admin console shows two devices. That is expected. *P5.*
- **Turning off.**
  - "Turn off" keeps the device signed in but offline.
  - "Sign out of Tailscale" signs the device out and deletes its key from the computer. If the device still shows in your admin console afterwards, remove it there. *What the console shows after sign-out: P6.*
- **Signing out takes up to about 35 seconds.**
- **The device name is public.** `spoolroom-watcher-xxxx.<your tailnet>.ts.net` appears in public certificate logs. It contains nothing about you or your printers.
