# Wi-Fi

The device can know several networks (home, grandparents, a hotel, a phone hotspot) and joins
the best one it can see. Manage them under **Wi-Fi** in the admin UI. The very first
connection still needs Internet on the Pi: set that up with Raspberry Pi Imager or
`raspi-config`, then add the other networks here (there is no captive-portal setup).

- Each network has a **priority**; higher is preferred. Disabled networks are ignored.
- Passwords are stored encrypted in Nexo's database, never returned by the API, and never
  logged.
- Nexo applies the list to **NetworkManager** (`nmcli`), the default on Raspberry Pi OS
  Bookworm and later. It creates profiles named `nexo-…` and only ever touches those; your own
  profiles are left alone. Removing a network removes its profile.
- The service user needs permission to manage connections: the installer adds it to the
  `netdev` group. If your system uses `dhcpcd`/`wpa_supplicant` instead of NetworkManager, the
  page says so and you configure Wi-Fi the usual way.

Security note: `nmcli` receives the password as a command-line argument, so another local user
on the Pi could see it in the process list for a moment. A Raspberry Pi running Nexo has no other
users; keep it that way.
