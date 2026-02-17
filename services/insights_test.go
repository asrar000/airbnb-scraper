package services

import (
	"testing"

	"airbnb-scraper/models"
	"airbnb-scraper/utils"
)

func sampleListings() []*models.Listing {
	return []*models.Listing{
		{Platform: "airbnb", Title: "Villa A", Price: 200, Location: "Bangkok", Rating: 4.9, URL: "https://airbnb.com/rooms/1"},
		{Platform: "airbnb", Title: "Studio B", Price: 50, Location: "Bangkok", Rating: 4.5, URL: "https://airbnb.com/rooms/2"},
		{Platform: "airbnb", Title: "Loft C", Price: 120, Location: "Tokyo", Rating: 4.8, URL: "https://airbnb.com/rooms/3"},
		{Platform: "airbnb", Title: "Cabin D", Price: 300, Location: "Bali", Rating: 0, URL: "https://airbnb.com/rooms/4"},
		{Platform: "airbnb", Title: "Flat E", Price: 0, Location: "Tokyo", Rating: 4.7, URL: "https://airbnb.com/rooms/5"},
	}
}

func TestInsightCounts(t *testing.T) {
	svc := NewInsightService(utils.NewLogger())
	r := svc.Generate(sampleListings())
	if r.TotalListings != 5 {
		t.Errorf("TotalListings: got %d, want 5", r.TotalListings)
	}
	if r.AirbnbListings != 5 {
		t.Errorf("AirbnbListings: got %d, want 5", r.AirbnbListings)
	}
}

func TestInsightPrices(t *testing.T) {
	svc := NewInsightService(utils.NewLogger())
	r := svc.Generate(sampleListings())
	wantAvg := 167.50
	if r.AveragePrice != wantAvg {
		t.Errorf("AveragePrice: got %.2f, want %.2f", r.AveragePrice, wantAvg)
	}
	if r.MinPrice != 50 {
		t.Errorf("MinPrice: got %.2f, want 50", r.MinPrice)
	}
	if r.MaxPrice != 300 {
		t.Errorf("MaxPrice: got %.2f, want 300", r.MaxPrice)
	}
}

func TestInsightMostExpensive(t *testing.T) {
	svc := NewInsightService(utils.NewLogger())
	r := svc.Generate(sampleListings())
	if r.MostExpensive == nil {
		t.Fatal("MostExpensive should not be nil")
	}
	if r.MostExpensive.Title != "Cabin D" {
		t.Errorf("MostExpensive: got %q, want %q", r.MostExpensive.Title, "Cabin D")
	}
}

func TestInsightTopRated(t *testing.T) {
	svc := NewInsightService(utils.NewLogger())
	r := svc.Generate(sampleListings())
	if len(r.TopRated) != 4 {
		t.Errorf("TopRated len: got %d, want 4", len(r.TopRated))
	}
	if r.TopRated[0].Rating != 4.9 {
		t.Errorf("TopRated[0].Rating: got %.2f, want 4.9", r.TopRated[0].Rating)
	}
}

func TestInsightLocationGrouping(t *testing.T) {
	svc := NewInsightService(utils.NewLogger())
	r := svc.Generate(sampleListings())
	if r.ListingsByLocation["Bangkok"] != 2 {
		t.Errorf("Bangkok count: got %d, want 2", r.ListingsByLocation["Bangkok"])
	}
	if r.ListingsByLocation["Tokyo"] != 2 {
		t.Errorf("Tokyo count: got %d, want 2", r.ListingsByLocation["Tokyo"])
	}
}

func TestInsightEmptyInput(t *testing.T) {
	svc := NewInsightService(utils.NewLogger())
	r := svc.Generate(nil)
	if r.TotalListings != 0 {
		t.Errorf("expected 0 total listings for empty input")
	}
}
