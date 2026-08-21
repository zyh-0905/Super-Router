package api

import "testing"

func TestSortStationsStable(t *testing.T) {
	stations := []*relayStation{
		{DisplayName: "zeta"}, {DisplayName: "alpha"}, {DisplayName: "mid"},
	}
	sortStations(stations)
	if stations[0].DisplayName != "alpha" || stations[1].DisplayName != "mid" || stations[2].DisplayName != "zeta" {
		t.Fatalf("sorted = %v", []string{stations[0].DisplayName, stations[1].DisplayName, stations[2].DisplayName})
	}
}
