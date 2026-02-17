package services

import (
	"testing"
	"time"

	"airbnb-scraper/models"
	"airbnb-scraper/utils"
)

func newTestLogger() *utils.Logger { return utils.NewLogger() }

func TestCleanerParsePrice(t *testing.T) {
	c := NewCleaner(newTestLogger())

	tests := []struct {
		raw  string
		want float64
	}{
		{"$120 night", 120},
		{"฿3,500 /night", 3500},
		{"", 0},
		{"free", 0},
		{"$1,200.50", 1200.50},
		{"USD 99", 99},
	}

	for _, tt := range tests {
		got := c.parsePrice(tt.raw)
		if got != tt.want {
			t.Errorf("parsePrice(%q) = %.2f; want %.2f", tt.raw, got, tt.want)
		}
	}
}

func TestCleanerParseRating(t *testing.T) {
	c := NewCleaner(newTestLogger())

	tests := []struct {
		raw  string
		want float64
	}{
		{"4.85", 4.85},
		{"5.0", 5.0},
		{"3.5 (120 reviews)", 3.5},
		{"", 0},
		{"New", 0},
		{"6.0", 0},
	}

	for _, tt := range tests {
		got := c.parseRating(tt.raw)
		if got != tt.want {
			t.Errorf("parseRating(%q) = %.2f; want %.2f", tt.raw, got, tt.want)
		}
	}
}

func TestCleanerDropsEmptyURL(t *testing.T) {
	c := NewCleaner(newTestLogger())
	raw := []*models.RawListing{
		{Title: "No URL", RawPrice: "$100", URL: "", Platform: "airbnb", ScrapedAt: time.Now()},
		{Title: "Has URL", RawPrice: "$200", URL: "https://airbnb.com/rooms/1", Platform: "airbnb", ScrapedAt: time.Now()},
	}

	cleaned := c.Clean(raw)
	if len(cleaned) != 1 {
		t.Errorf("expected 1 listing after dropping empty URL, got %d", len(cleaned))
	}
}

func TestCleanerDeduplicatesURL(t *testing.T) {
	c := NewCleaner(newTestLogger())
	raw := []*models.RawListing{
		{Title: "A", URL: "https://airbnb.com/rooms/1", Platform: "airbnb", ScrapedAt: time.Now()},
		{Title: "B", URL: "https://airbnb.com/rooms/1", Platform: "airbnb", ScrapedAt: time.Now()},
	}

	cleaned := c.Clean(raw)
	if len(cleaned) != 1 {
		t.Errorf("expected 1 listing after deduplication, got %d", len(cleaned))
	}
}
