package skills

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"
)

// PlaceOption is one suggestion in the town picker.
type PlaceOption struct {
	Label   string  `json:"label"` // "Esplugues de Llobregat, Catalonia, Spain"
	Name    string  `json:"name"`
	Country string  `json:"country"`
	Lat     float64 `json:"latitude"`
	Lon     float64 `json:"longitude"`
	Near    bool    `json:"near"` // in the country the device's internet address points at
}

const (
	maxSuggestions  = 10
	homeCountryTTL  = 6 * time.Hour
	minSearchLetter = 2
)

type geoResult struct {
	Name        string  `json:"name"`
	Admin1      string  `json:"admin1"`
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Lat         float64 `json:"latitude"`
	Lon         float64 `json:"longitude"`
}

// homeCountry is the ISO country code the device's internet address points at, or "" when it
// cannot be told. It is looked up once and kept for a few hours, and only while someone is
// typing a town. A VPN makes it wrong, which is why Search also looks everywhere.
func (w *Weather) homeCountry(ctx context.Context) string {
	w.homeMu.Lock()
	defer w.homeMu.Unlock()
	if time.Since(w.homeAt) < homeCountryTTL {
		return w.home
	}
	var out struct {
		Success     bool   `json:"success"`
		CountryCode string `json:"country_code"`
	}
	q := url.Values{"fields": {"success,country_code"}}
	cc := ""
	if err := w.fetchJSON(ctx, w.get(ctx, settingIPGeoAPI, defaultIPGeoAPI), "/", q, &out); err == nil && out.Success {
		if c := strings.ToUpper(strings.TrimSpace(out.CountryCode)); len(c) == 2 {
			cc = c
		}
	}
	w.home, w.homeAt = cc, time.Now()
	return cc
}

func (w *Weather) geoSearch(ctx context.Context, name, countryCode string) ([]geoResult, error) {
	var out struct {
		Results []geoResult `json:"results"`
	}
	q := url.Values{"name": {name}, "count": {fmt.Sprint(maxSuggestions)}, "language": {w.geoLanguage(ctx)}, "format": {"json"}}
	if countryCode != "" {
		q.Set("countryCode", countryCode)
	}
	if err := w.fetchJSON(ctx, w.get(ctx, settingGeoAPI, defaultGeoAPI), "/v1/search", q, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// Search suggests places for what the person has typed so far. Places in the country the
// internet address points at come first and arrive quickly; a search with no country filter
// runs at the same time, so typing "Bogotá" still finds it from Spain, or behind a VPN.
func (w *Weather) Search(ctx context.Context, text string) ([]PlaceOption, error) {
	text = strings.TrimSpace(text)
	if len([]rune(text)) < minSearchLetter || len([]rune(text)) > 80 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cc := w.homeCountry(ctx)

	var near, anywhere []geoResult
	var nearErr, anyErr error
	var wg sync.WaitGroup
	if cc != "" {
		wg.Add(1)
		go func() { defer wg.Done(); near, nearErr = w.geoSearch(ctx, text, cc) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); anywhere, anyErr = w.geoSearch(ctx, text, "") }()
	wg.Wait()
	if anyErr != nil && (cc == "" || nearErr != nil) {
		return nil, anyErr
	}

	var out []PlaceOption
	seen := map[[2]int]bool{}
	add := func(rs []geoResult, isNear bool) {
		for _, r := range rs {
			key := [2]int{int(math.Round(r.Lat * 100)), int(math.Round(r.Lon * 100))}
			if seen[key] || len(out) >= maxSuggestions || r.Name == "" || r.Lat < -90 || r.Lat > 90 || r.Lon < -180 || r.Lon > 180 {
				continue
			}
			seen[key] = true
			label := r.Name
			for _, part := range []string{r.Admin1, r.Country} {
				if part != "" && part != r.Name {
					label += ", " + part
				}
			}
			out = append(out, PlaceOption{Label: label, Name: r.Name, Country: r.Country, Lat: r.Lat, Lon: r.Lon, Near: isNear})
		}
	}
	add(near, true)
	add(anywhere, false)
	return out, nil
}

// SetExactPlace saves a place picked from Search, so a name shared by several towns is not
// geocoded again.
func (w *Weather) SetExactPlace(ctx context.Context, p Place) error {
	if p.Name = strings.TrimSpace(p.Name); p.Name == "" || p.Lat < -90 || p.Lat > 90 || p.Lon < -180 || p.Lon > 180 {
		return errors.New("that place is not valid")
	}
	return w.savePlace(ctx, p)
}
