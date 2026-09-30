package app

import (
	"context"

	"github.com/juancer/nexo/internal/wifi"
)

// SyncWifi applies the configured Wi-Fi networks to the operating system.
func (a *App) SyncWifi(ctx context.Context) error {
	a.WifiMu.Lock()
	defer a.WifiMu.Unlock()
	return a.SyncWifiLocked(ctx)
}

// SyncWifiLocked is SyncWifi for a caller that already holds WifiMu.
func (a *App) SyncWifiLocked(ctx context.Context) error {
	nets, err := a.Store.ListWifiNetworks(ctx)
	if err != nil {
		return err
	}
	var list []wifi.Network
	for _, n := range nets {
		if !n.Enabled {
			continue
		}
		pw := ""
		if n.SecretName != "" {
			v, err := a.Vault.Get(ctx, n.SecretName)
			if err != nil {
				a.Log.Warn("wifi password unavailable; network left as it is", "ssid", n.SSID)
				list = append(list, wifi.Network{SSID: n.SSID, Keep: true})
				continue
			}
			pw = string(v)
		}
		list = append(list, wifi.Network{SSID: n.SSID, Password: pw, Priority: n.Priority})
	}
	return a.Wifi.Apply(ctx, list)
}

// Restart asks the process to exit cleanly; systemd (Restart=always) starts it again.
func (a *App) Restart() bool {
	if a.RestartFunc == nil {
		return false
	}
	go a.RestartFunc()
	return true
}
