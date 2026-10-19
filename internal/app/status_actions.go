package app

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/skills"
	"github.com/juancer/milo/internal/system"
)

// Questions about the device that have a short answer. They return numbers and names in Data, so
// the voice router can word the answer in the language the question was asked in.
func (a *App) registerStatusActions() {
	a.Actions.MustRegister(actions.Action{
		Name:        "now_playing",
		Description: "Say what song is playing right now, if any.",
		Exposure:    actions.ExposeDevice | actions.ExposeAssistant,
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			data := map[string]any{"playing": false}
			msg := "Nothing is playing."
			if a.Spotify != nil {
				if info := a.Spotify.Info(ctx); info.Playing && info.Track != "" {
					data["playing"], data["track"], data["source"] = true, info.Track, "spotify"
					msg = "Playing " + info.Track + "."
				}
			}
			if a.Music != nil && data["playing"] == false {
				if info := a.Music.Info(ctx); info.State == skills.StatePlaying && info.Title != "" {
					data["playing"], data["track"], data["source"] = true, info.Title, "local"
					msg = "Playing " + info.Title + "."
				}
			}
			return actions.Result{Message: msg, Data: data}, nil
		},
	})
	// Not offered to the language model: the address and the uptime are for the person at the device.
	a.Actions.MustRegister(actions.Action{
		Name:        "get_uptime",
		Description: "Say how long the device has been on.",
		Exposure:    actions.ExposeDevice,
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			raw, err := os.ReadFile("/proc/uptime")
			if err != nil {
				return actions.Result{}, errors.New("the uptime is not available here")
			}
			secs, err := strconv.ParseFloat(strings.Fields(string(raw))[0], 64)
			if err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Message: "Up for " + strconv.Itoa(int(secs)) + " seconds.", Data: map[string]any{"seconds": int(secs)}}, nil
		},
	})
	a.Actions.MustRegister(actions.Action{
		Name:        "get_ip",
		Description: "Say the device's address on the home network.",
		Exposure:    actions.ExposeDevice,
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			ip := system.LocalIP()
			if ip == "" {
				return actions.Result{}, errors.New("no network address")
			}
			return actions.Result{Message: "My address is " + ip + ".", Data: map[string]any{"ip": ip}}, nil
		},
	})
}
