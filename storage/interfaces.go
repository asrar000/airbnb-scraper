package storage

import "airbnb-scraper/models"

// ListingWriter is the interface any storage backend must satisfy.
type ListingWriter interface {
	Write(listings []*models.Listing) error
	Close() error
}

// RawListingWriter is the interface for persisting unprocessed scraped data.
type RawListingWriter interface {
	WriteRaw(listings []*models.RawListing) error
	Close() error
}
