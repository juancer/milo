package main

import (
	"context"
	"fmt"
	"os"

	"github.com/juancer/nexo/internal/auth"
	"github.com/juancer/nexo/internal/secrets"
	"github.com/juancer/nexo/internal/store"
	"github.com/juancer/nexo/internal/system"
	"github.com/juancer/nexo/internal/telegram"
)

// devSeed prepares a demo installation for local development: an admin
// password, the simulator's FAKE bot token, a demo family and enabled
// features. It refuses to run against real hardware and only runs once per
// database, so it can never touch a real device or overwrite parent settings.
func devSeed(ctx context.Context, cfg system.Config) error {
	if cfg.Hardware != "mock" {
		return fmt.Errorf("dev-seed only runs with NEXO_HARDWARE=mock; it will never modify a real installation")
	}
	st, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	if _, err := st.GetSetting(ctx, "dev.seeded"); err == nil {
		fmt.Println("dev-seed: already done")
		return nil
	}
	key, err := secrets.LoadKey(cfg.KeyPath)
	if err != nil {
		return err
	}
	vault, err := secrets.New(key, st)
	if err != nil {
		return err
	}

	password := getenv("NEXO_DEV_PASSWORD", "nexo-demo-password")
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	st.SetSetting(ctx, "admin.password_hash", hash) //nolint:errcheck

	// A fake token that only the simulator accepts.
	token := getenv("NEXO_SIM_BOT_TOKEN", "123456:DEMO-TOKEN-NOT-A-REAL-BOT-abcdefghijk")
	if err := vault.Put(ctx, telegram.SecretBotToken, []byte(token)); err != nil {
		return err
	}

	family := []struct {
		name, role string
		tg         int64
	}{
		{"Dad", "PARENT", 1001},
		{"Mum", "PARENT", 1002},
		{"Grandma", "FAMILY", 1003},
	}
	for _, f := range family {
		uid, err := st.CreateTelegramUser(ctx, f.name, f.role, f.tg, f.name+"_demo")
		if err != nil {
			return err
		}
		if _, err := st.CreateChat(ctx, store.Chat{Name: f.name, Kind: "user", UserID: uid, TelegramChatID: f.tg,
			Favorite: true, Position: int(f.tg - 1000), Enabled: true}); err != nil {
			return err
		}
	}
	for _, name := range devFeatures {
		st.EnsureFeature(ctx, name)           //nolint:errcheck
		st.SetFeatureEnabled(ctx, name, true) //nolint:errcheck
	}
	st.SetSetting(ctx, "dev.seeded", "1") //nolint:errcheck
	fmt.Printf("dev-seed: admin password %q, demo family Dad/Mum/Grandma, features enabled\n", password)
	return nil
}

// devFeatures are switched on in the demo. Later phases append to this list.
var devFeatures = []string{"telegram"}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
