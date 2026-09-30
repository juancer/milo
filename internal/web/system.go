package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/juancer/milo/internal/audio"
	"github.com/juancer/milo/internal/telegram"
)

func (s *server) systemRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/system", s.requireAuth(s.getSystem))
	mux.Handle("PUT /api/system", s.requireAuth(s.putSystem))
	mux.Handle("POST /api/system/restart", s.requireAuth(s.restart))
	mux.Handle("GET /api/logs", s.requireAuth(s.logs))
}

type systemDTO struct {
	DeviceName     string   `json:"device_name"`
	Timezone       string   `json:"timezone"`
	RetentionDays  int      `json:"message_retention_days"`
	AnnounceSender bool     `json:"announce_sender"`
	LongPressMS    int      `json:"long_press_ms"`
	DoublePressMS  int      `json:"double_press_ms"`
	Audio          audioDTO `json:"audio"`
}

type audioDTO struct {
	CaptureDevice  string `json:"capture_device"`
	PlaybackDevice string `json:"playback_device"`
	MixerCard      string `json:"mixer_card"`
	MixerControl   string `json:"mixer_control"`
}

func (s *server) getSystem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name, _ := s.app.Store.DeviceName(ctx)
	writeJSON(w, http.StatusOK, systemDTO{
		DeviceName:     name,
		Timezone:       s.app.Schedules.Location(ctx).String(),
		RetentionDays:  s.app.IntSetting(ctx, "messages.retention_days", 30),
		AnnounceSender: s.app.Setting(ctx, telegram.SettingAnnounceName, "0") == "1",
		LongPressMS:    s.app.IntSetting(ctx, "buttons.long_press_ms", 800),
		DoublePressMS:  s.app.IntSetting(ctx, "buttons.double_press_ms", 350),
		Audio: audioDTO{
			CaptureDevice:  s.app.Setting(ctx, "audio.capture_device", audio.DefaultALSAConfig.CaptureDevice),
			PlaybackDevice: s.app.Setting(ctx, "audio.playback_device", audio.DefaultALSAConfig.PlaybackDevice),
			MixerCard:      s.app.Setting(ctx, "audio.mixer_card", audio.DefaultALSAConfig.MixerCard),
			MixerControl:   s.app.Setting(ctx, "audio.mixer_control", audio.DefaultALSAConfig.MixerControl),
		},
	})
}

func (s *server) putSystem(w http.ResponseWriter, r *http.Request) {
	var d systemDTO
	if !decodeJSON(w, r, &d) {
		return
	}
	d.DeviceName = strings.TrimSpace(d.DeviceName)
	cfg := audio.ALSAConfig{CaptureDevice: d.Audio.CaptureDevice, PlaybackDevice: d.Audio.PlaybackDevice,
		MixerCard: d.Audio.MixerCard, MixerControl: d.Audio.MixerControl}
	switch {
	case d.DeviceName == "" || len([]rune(d.DeviceName)) > 40:
		writeError(w, http.StatusBadRequest, "the device name must be 1-40 characters")
	case d.RetentionDays < 0 || d.RetentionDays > 365:
		writeError(w, http.StatusBadRequest, "message retention must be 0 (keep) to 365 days")
	case d.LongPressMS < 300 || d.LongPressMS > 3000 || d.DoublePressMS < 150 || d.DoublePressMS > 1000:
		writeError(w, http.StatusBadRequest, "long press must be 300-3000 ms and double press 150-1000 ms")
	case cfg.Validate() != nil:
		writeError(w, http.StatusBadRequest, cfg.Validate().Error())
	default:
		ctx := r.Context()
		set := func(k, v string) {
			if err := s.app.Store.SetSetting(ctx, k, v); err != nil {
				s.app.Log.Error("save setting", "key", k, "error", err)
			}
		}
		s.app.Store.SetDeviceName(ctx, d.DeviceName) //nolint:errcheck
		set("messages.retention_days", strconv.Itoa(d.RetentionDays))
		set(telegram.SettingAnnounceName, map[bool]string{true: "1", false: "0"}[d.AnnounceSender])
		set("buttons.long_press_ms", strconv.Itoa(d.LongPressMS))
		set("buttons.double_press_ms", strconv.Itoa(d.DoublePressMS))
		set("audio.capture_device", cfg.CaptureDevice)
		set("audio.playback_device", cfg.PlaybackDevice)
		set("audio.mixer_card", cfg.MixerCard)
		set("audio.mixer_control", cfg.MixerControl)
		s.audit(r, "settings.system", "", nil)
		s.getSystem(w, r)
	}
}

func (s *server) restart(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "system.restart", "", nil)
	if !s.app.Restart() {
		writeError(w, http.StatusNotImplemented, "restart is not available in this mode")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"restarting": true})
}

func (s *server) logs(w http.ResponseWriter, _ *http.Request) {
	lines := []string{}
	if s.app.Logs != nil {
		lines = s.app.Logs.Lines()
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines})
}
