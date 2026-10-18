# Wi-Fi

The device can know several networks (home, grandparents, a hotel, a phone hotspot) and joins
the best one it can see. Manage them under **Wi-Fi** in the admin UI. The very first
connection still needs Internet on the Pi: set that up with Raspberry Pi Imager or
`raspi-config`, then add the other networks here (there is no captive-portal setup).

- Each network has a **priority**; higher is preferred. Disabled networks are ignored.
- Passwords are stored encrypted in Milo's database, never returned by the API, and never
  logged.
- Milo picks the backend at start-up. With **NetworkManager** running (the default on Raspberry Pi
  OS Bookworm and later) it uses `nmcli` and creates profiles named `milo-…`. Otherwise, with
  **dhcpcd and wpa_supplicant** (Bullseye and earlier), it talks to wpa_supplicant's control socket directly. Either way it only
  touches its own networks (`milo-…`): your existing Wi-Fi, a fixed IP address in `dhcpcd.conf`
  and the current connection are left alone. Removing a network removes only Milo's entry.
- With wpa_supplicant, Milo does not rewrite `wpa_supplicant.conf` (no `save_config`): it applies
  its list again every time it starts. It sends wpa_supplicant the derived WPA key, never the
  password. Priorities compete with your own network, which has priority 0.
- The service user needs permission to manage connections: the installer adds it to the
  `netdev` group, which owns wpa_supplicant's control socket on Raspberry Pi OS.
- If neither is available, the Wi-Fi sheet says so and you configure Wi-Fi the usual way.

Security note: `nmcli` receives the password as a command-line argument, so another local user
on the Pi could see it in the process list for a moment. A Raspberry Pi running Milo has no other
users; keep it that way.

Left as is on purpose: Milo does not pass the password to `nmcli` on stdin or from a keyfile,
because that behaviour cannot be verified without a real NetworkManager. To close the window,
mount `/proc` with `hidepid=2` (or set `ProtectProc=invisible` on other services) so that other
users cannot read the command lines of the `milo` user's processes.

If a saved password cannot be read from the vault, Milo leaves that network's existing profile
untouched instead of deleting it. When a secured network is edited to be open, its profile is
deleted and recreated so the old key does not stay in NetworkManager (this has not been tested on
a real device).
