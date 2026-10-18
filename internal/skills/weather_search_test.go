package skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// geoServer fakes the geocoder: a search with countryCode=ES answers with the Spanish towns,
// one without answers with towns from several countries. It records the queries it got.
func geoServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Query().Get("countryCode") == "ES" {
			w.Write([]byte(`{"results":[{"name":"Esplugues de Llobregat","admin1":"Catalonia","country":"Spain","country_code":"ES","latitude":41.3775,"longitude":2.0875}]}`))
			return
		}
		w.Write([]byte(`{"results":[
			{"name":"Esplugues de Llobregat","admin1":"Catalonia","country":"Spain","country_code":"ES","latitude":41.3775,"longitude":2.0875},
			{"name":"Esplugues","admin1":"Occitanie","country":"France","country_code":"FR","latitude":43.5,"longitude":1.2},
			{"name":"","country":"Nowhere","latitude":1,"longitude":1},
			{"name":"Broken","country":"Nowhere","latitude":95,"longitude":1}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &queries
}

func TestWeatherSearchRanksTheHomeCountryFirstAndSearchesEverywhere(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	var ipHits int
	r.store.SetSetting(ctx, settingIPGeoAPI, ipGeoServer(t, 200, `{"success":true,"country_code":"es"}`, &ipHits).URL)
	geo, queries := geoServer(t)
	r.store.SetSetting(ctx, settingGeoAPI, geo.URL)
	w := &Weather{Common: r.common}

	got, err := w.Search(ctx, "esplugues")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d suggestions, want 2 (duplicates and invalid rows dropped): %+v", len(got), got)
	}
	if got[0].Label != "Esplugues de Llobregat, Catalonia, Spain" || !got[0].Near {
		t.Errorf("first = %+v, want the Spanish town marked near", got[0])
	}
	if got[1].Country != "France" || got[1].Near {
		t.Errorf("second = %+v, want the French one, not near", got[1])
	}
	joined := strings.Join(*queries, "\n")
	if !strings.Contains(joined, "countryCode=ES") || len(*queries) != 2 {
		t.Errorf("geocoder queries:\n%s", joined)
	}
	if _, err := w.Search(ctx, "bogota"); err != nil {
		t.Fatal(err)
	}
	if ipHits != 1 {
		t.Errorf("the internet address was looked up %d times, want once", ipHits)
	}
}

func TestWeatherSearchWorksWhenTheCountryCannotBeTold(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.store.SetSetting(ctx, settingIPGeoAPI, ipGeoServer(t, 503, `{}`, nil).URL)
	geo, queries := geoServer(t)
	r.store.SetSetting(ctx, settingGeoAPI, geo.URL)
	got, err := (&Weather{Common: r.common}).Search(ctx, "esplugues")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if strings.Contains(strings.Join(*queries, "\n"), "countryCode") || got[0].Near {
		t.Errorf("no country filter expected:\n%v %+v", *queries, got)
	}
}

func TestWeatherSearchIgnoresShortTextAndSetExactPlaceValidates(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	w := &Weather{Common: r.common}
	if got, err := w.Search(ctx, " e "); err != nil || got != nil {
		t.Errorf("short text: %v, %v", got, err)
	}
	if w.SetExactPlace(ctx, Place{Name: "", Lat: 1, Lon: 1}) == nil || w.SetExactPlace(ctx, Place{Name: "X", Lat: 91, Lon: 1}) == nil {
		t.Error("invalid place accepted")
	}
	if err := w.SetExactPlace(ctx, Place{Name: "Esplugues de Llobregat, Catalonia, Spain", Lat: 41.3775, Lon: 2.0875}); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.store.GetSetting(ctx, SettingWeatherPlace); got != "Esplugues de Llobregat, Catalonia, Spain" {
		t.Errorf("place = %q", got)
	}
}
