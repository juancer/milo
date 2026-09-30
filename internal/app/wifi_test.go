package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/juancer/milo/internal/store"
	"github.com/juancer/milo/internal/wifi"
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

func TestSyncWifiWaitsForAnEditInProgress(t *testing.T) {
	a := newApp(t)
	a.WifiMu.Lock() // a save or delete is between its writes and its sync
	done := make(chan error, 1)
	go func() { done <- a.SyncWifi(context.Background()) }()
	select {
	case <-done:
		t.Fatal("SyncWifi ran while the Wi-Fi list was being edited")
	case <-time.After(100 * time.Millisecond):
	}
	a.WifiMu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceRoundContainsAPanic(t *testing.T) {
	a := &App{Log: slog.New(slog.NewTextHandler(io.Discard, nil))} // no Assistant: PruneHistory panics
	a.maintenanceRound(context.Background())                       // a panic here would fail the test
}
