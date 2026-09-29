// Command nexo is the Nexo device runtime and its management CLI.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/juancer/nexo/internal/system"
)

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

const usage = `Nexo %s - screenless family communication and AI assistant

Usage: nexo <command>

Commands:
  run             run the device (used by the systemd service)
  install         install and start the systemd service (root)
  keygen          create the master key if it does not exist
  admin-password  set the web admin password
                  (--password-stdin reads it from standard input)
  test-audio      play the built-in sounds, then record and play back 3 seconds
  test-buttons    print button presses (real GPIO only)
  start|stop|restart|status
                  control the systemd service
  version         print the version

Configuration comes from NEXO_* environment variables; see docs/configuration.md.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := execute(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nexo:", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Printf(usage, version)
		return nil
	}
	switch cmd, rest := args[0], args[1:]; cmd {
	case "version", "--version":
		fmt.Println(version)
		return nil
	case "help", "--help", "-h":
		fmt.Printf(usage, version)
		return nil
	case "install":
		return system.Install(ctx, os.Stdout)
	case "start", "stop", "restart", "status":
		return system.Systemctl(cmd)
	case "run", "keygen", "admin-password", "test-audio", "test-buttons":
		cfg, err := system.LoadConfig(os.Getenv)
		if err != nil {
			return err
		}
		switch cmd {
		case "run":
			return runServer(ctx, cfg)
		case "keygen":
			return keygen(cfg)
		case "test-audio":
			return testAudio(ctx, cfg)
		case "test-buttons":
			return testButtons(ctx, cfg)
		default:
			return adminPassword(ctx, cfg, rest)
		}
	default:
		return fmt.Errorf("unknown command %q (try `nexo help`)", cmd)
	}
}

var errUsage = errors.New("invalid usage")
