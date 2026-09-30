package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/juancer/milo/internal/actions"
	"github.com/juancer/milo/internal/i18n"
)

// Weather settings (system_settings keys).
const (
	SettingWeatherLat   = "weather.latitude"
	SettingWeatherLon   = "weather.longitude"
	SettingWeatherPlace = "weather.place"
	SettingWeatherUnits = "weather.units" // celsius (default) or fahrenheit
	settingWeatherAPI   = "weather.api_base"
	settingGeoAPI       = "weather.geo_base"
	settingIPGeoAPI     = "weather.ipgeo_base"

	defaultForecastAPI = "https://api.open-meteo.com"
	defaultGeoAPI      = "https://geocoding-api.open-meteo.com"
	defaultIPGeoAPI    = "https://ipwho.is"
)

// maxPlaceLabel is the most runes of a place name that are stored.
const maxPlaceLabel = 80

// ErrNoLocation means the device's location could not be worked out from its address; the
// person should type their town instead.
var ErrNoLocation = errors.New("could not detect the location")

// Place is a geocoded location.
type Place struct {
	Name    string  `json:"name"`
	Country string  `json:"country"`
	Lat     float64 `json:"latitude"`
	Lon     float64 `json:"longitude"`
}

// Weather is the "weather" feature, using the free Open-Meteo service (no API
// key). Only the coordinates of the configured place leave the device.
type Weather struct {
	Common
	Client *http.Client
}

// Name implements features.Feature.
func (w *Weather) Name() string { return "weather" }

// Start implements features.Feature.
func (w *Weather) Start(ctx context.Context) error {
	if _, _, _, err := w.location(ctx); err != nil {
		return err
	}
	return nil
}

// Stop implements features.Feature.
func (w *Weather) Stop(context.Context) error { return nil }

func (w *Weather) get(ctx context.Context, key, def string) string {
	if v, err := w.Store.GetSetting(ctx, key); err == nil && v != "" {
		return v
	}
	return def
}

func (w *Weather) location(ctx context.Context) (lat, lon float64, place string, err error) {
	lat, e1 := strconv.ParseFloat(w.get(ctx, SettingWeatherLat, ""), 64)
	lon, e2 := strconv.ParseFloat(w.get(ctx, SettingWeatherLon, ""), 64)
	if e1 != nil || e2 != nil {
		return 0, 0, "", errors.New("no place is set: choose your town under Skills in the admin")
	}
	return lat, lon, w.get(ctx, SettingWeatherPlace, "your area"), nil
}

func (w *Weather) client() *http.Client {
	if w.Client != nil {
		return w.Client
	}
	return w.httpClient(15 * time.Second)
}

func (w *Weather) fetchJSON(ctx context.Context, base, path string, q url.Values, out any) error {
	if err := w.checkURL(base); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := w.client().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("weather service unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("weather service answered %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// Geocode looks up a place by name.
func (w *Weather) Geocode(ctx context.Context, name string) (Place, error) {
	var out struct {
		Results []Place `json:"results"`
	}
	q := url.Values{"name": {name}, "count": {"1"}, "language": {w.geoLanguage(ctx)}, "format": {"json"}}
	if err := w.fetchJSON(ctx, w.get(ctx, settingGeoAPI, defaultGeoAPI), "/v1/search", q, &out); err != nil {
		return Place{}, err
	}
	if len(out.Results) == 0 {
		return Place{}, fmt.Errorf("no place found called %q", name)
	}
	return out.Results[0], nil
}

// geoLanguage is the language place names are returned in: the assistant language, or English
// when that is "auto" or not one Milo knows.
func (w *Weather) geoLanguage(ctx context.Context) string {
	if g, err := w.Store.GetGPTSettings(ctx); err == nil {
		if code, ok := i18n.Normalize(g.Language); ok {
			return code
		}
	}
	return i18n.Default
}

// SetPlace geocodes name and saves it as the device's location.
func (w *Weather) SetPlace(ctx context.Context, name string) (Place, error) {
	p, err := w.Geocode(ctx, name)
	if err != nil {
		return Place{}, err
	}
	return p, w.savePlace(ctx, p)
}

func (w *Weather) savePlace(ctx context.Context, p Place) error {
	label := p.Name
	if p.Country != "" {
		label += ", " + p.Country
	}
	if r := []rune(label); len(r) > maxPlaceLabel { // the geocoder's text is spoken later
		label = string(r[:maxPlaceLabel])
	}
	for k, v := range map[string]string{
		SettingWeatherLat: strconv.FormatFloat(p.Lat, 'f', 4, 64), SettingWeatherLon: strconv.FormatFloat(p.Lon, 'f', 4, 64),
		SettingWeatherPlace: label,
	} {
		if err := w.Store.SetSetting(ctx, k, v); err != nil {
			return err
		}
	}
	return nil
}

// DetectPlace asks a free geolocation service where this device's internet address is and
// saves that as the location. It runs only when a person asks for it: the service sees the
// device's public address on that one request, and only the place is kept. The result is
// approximate; typing the town (SetPlace) overrides it. Any failure is ErrNoLocation.
func (w *Weather) DetectPlace(ctx context.Context) (Place, error) {
	var out struct {
		Success bool    `json:"success"`
		City    string  `json:"city"`
		Country string  `json:"country"`
		Lat     float64 `json:"latitude"`
		Lon     float64 `json:"longitude"`
	}
	q := url.Values{"fields": {"success,city,country,latitude,longitude"}}
	if err := w.fetchJSON(ctx, w.get(ctx, settingIPGeoAPI, defaultIPGeoAPI), "/", q, &out); err != nil {
		w.Log.Warn("weather: location detection failed", "error", err)
		return Place{}, ErrNoLocation
	}
	p := Place{Name: strings.TrimSpace(out.City), Country: strings.TrimSpace(out.Country), Lat: out.Lat, Lon: out.Lon}
	if !out.Success || p.Name == "" || p.Lat < -90 || p.Lat > 90 || p.Lon < -180 || p.Lon > 180 {
		return Place{}, ErrNoLocation
	}
	if err := w.savePlace(ctx, p); err != nil {
		return Place{}, err
	}
	return p, nil
}

type forecast struct {
	Current struct {
		Temp float64 `json:"temperature_2m"`
		Code int     `json:"weather_code"`
	} `json:"current"`
	Daily struct {
		Code   []int     `json:"weather_code"`
		Max    []float64 `json:"temperature_2m_max"`
		Min    []float64 `json:"temperature_2m_min"`
		Precip []float64 `json:"precipitation_probability_max"`
	} `json:"daily"`
}

// Describe returns a spoken forecast for "today" or "tomorrow".
func (w *Weather) Describe(ctx context.Context, day string) (string, error) {
	lat, lon, place, err := w.location(ctx)
	if err != nil {
		return "", err
	}
	unit := "celsius"
	if w.get(ctx, SettingWeatherUnits, "celsius") == "fahrenheit" {
		unit = "fahrenheit"
	}
	q := url.Values{
		"latitude": {strconv.FormatFloat(lat, 'f', 4, 64)}, "longitude": {strconv.FormatFloat(lon, 'f', 4, 64)},
		"current": {"temperature_2m,weather_code"}, "timezone": {"auto"}, "forecast_days": {"2"}, "temperature_unit": {unit},
		"daily": {"weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max"},
	}
	var f forecast
	if err := w.fetchJSON(ctx, w.get(ctx, settingWeatherAPI, defaultForecastAPI), "/v1/forecast", q, &f); err != nil {
		return "", err
	}
	i := 0
	if day == "tomorrow" {
		i = 1
	}
	if len(f.Daily.Code) <= i || len(f.Daily.Max) <= i || len(f.Daily.Min) <= i {
		return "", errors.New("the weather service sent an incomplete forecast")
	}
	rain := ""
	if len(f.Daily.Precip) > i && f.Daily.Precip[i] > 0 {
		rain = fmt.Sprintf(", with a %d percent chance of rain", int(f.Daily.Precip[i]+0.5))
	}
	daily := fmt.Sprintf("%s, a high of %d and a low of %d%s", WeatherWords(f.Daily.Code[i]), round(f.Daily.Max[i]), round(f.Daily.Min[i]), rain)
	if i == 1 {
		return fmt.Sprintf("Tomorrow in %s: %s.", place, daily), nil
	}
	return fmt.Sprintf("In %s it is %d degrees and %s. Today: %s.", place, round(f.Current.Temp), WeatherWords(f.Current.Code), daily), nil
}

func round(v float64) int {
	if v < 0 {
		return int(v - 0.5)
	}
	return int(v + 0.5)
}

// WeatherWords turns a WMO weather code into words.
func WeatherWords(code int) string {
	switch {
	case code == 0:
		return "clear"
	case code == 1:
		return "mostly clear"
	case code == 2:
		return "partly cloudy"
	case code == 3:
		return "overcast"
	case code == 45 || code == 48:
		return "foggy"
	case code >= 51 && code <= 57:
		return "drizzly"
	case code >= 61 && code <= 67:
		return "rainy"
	case code >= 71 && code <= 77, code == 85 || code == 86:
		return "snowy"
	case code >= 80 && code <= 82:
		return "showery"
	case code >= 95:
		return "stormy"
	}
	return "unsettled"
}

// RegisterActions registers get_weather.
func (w *Weather) RegisterActions(e *actions.Engine) {
	e.MustRegister(actions.Action{
		Name: "get_weather", Description: "Tell the weather today or tomorrow for the family's town.",
		Feature: "weather", Exposure: actions.ExposeDevice | actions.ExposeAssistant,
		Params: []actions.Param{{Name: "day", Description: "today or tomorrow", Type: actions.String, Enum: []string{"today", "tomorrow"}}},
		Handler: func(ctx context.Context, c actions.Call) (actions.Result, error) {
			msg, err := w.Describe(ctx, c.Args.String("day"))
			if err != nil {
				return actions.Result{}, err
			}
			return actions.Result{Message: msg}, nil
		},
	})
}
