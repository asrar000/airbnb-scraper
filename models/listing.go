package models

import "time"

// RawListing holds unprocessed scraped data directly from the browser.
// This is written to CSV before any cleaning or transformation.
type RawListing struct {
	Title       string
	RawPrice    string
	Location    string
	Rating      string
	URL         string
	Description string
	ScrapedAt   time.Time
	Platform    string
}

// Listing is the cleaned, validated record ready for PostgreSQL storage.
type Listing struct {
	ID          int64
	Platform    string
	Title       string
	Price       float64
	Location    string
	Rating      float64
	URL         string
	Description string
	CreatedAt   time.Time
}

// InsightReport holds the computed analytics over the cleaned dataset.
type InsightReport struct {
	TotalListings      int
	AirbnbListings     int
	AveragePrice       float64
	MinPrice           float64
	MaxPrice           float64
	MostExpensive      *Listing
	TopRated           []*Listing
	ListingsByLocation map[string]int
}
