package app

import (
	"context"
	"testing"

	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/wifi"
)

func TestSyncWifiKeepsANetworkWhosePasswordIsUnavailable(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	if _, err := a.Store.CreateWifiNetwork(ctx, store.WifiNetwork{SSID: "Home", SecretName: "wifi.missing", Priority: 5, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.SyncWifi(ctx); err != nil {
		t.Fatal(err)
	}
	got := a.Wifi.(*wifi.Mock).Applied()
	if len(got) != 1 || got[0].SSID != "Home" || !got[0].Keep {
		t.Fatalf("applied = %+v, want Home marked Keep", got)
	}
}
