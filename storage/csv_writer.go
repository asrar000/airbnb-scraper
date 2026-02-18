package storage

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"airbnb-scraper/models"
)

// CSVWriter writes raw (uncleaned) listings to a CSV file.
// It is safe for concurrent use.
type CSVWriter struct {
	mu     sync.Mutex
	file   *os.File
	writer *csv.Writer
}

// NewCSVWriter creates (or truncates) the CSV file at the given path and
// writes the header row. Intermediate directories are created automatically.
func NewCSVWriter(path string) (*CSVWriter, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("csv: create output dir: %w", err)
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("csv: create file %q: %w", path, err)
	}

	w := csv.NewWriter(f)

	// Write header
	if err := w.Write([]string{
		"platform", "title", "raw_price", "location", "rating", "url", "description", "scraped_at",
	}); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("csv: write header: %w", err)
	}
	w.Flush()

	return &CSVWriter{file: f, writer: w}, nil
}

// WriteRaw writes the first 10 raw listings to the CSV file (truncating any previous data).
func (c *CSVWriter) WriteRaw(listings []*models.RawListing) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Limit to first 10 listings
	if len(listings) > 10 {
		listings = listings[:10]
	}

	for _, l := range listings {
		row := []string{
			l.Platform,
			l.Title,
			l.RawPrice,
			l.Location,
			l.Rating,
			l.URL,
			l.Description,
			l.ScrapedAt.Format(time.RFC3339),
		}
		if err := c.writer.Write(row); err != nil {
			return fmt.Errorf("csv: write row: %w", err)
		}
	}

	c.writer.Flush()
	return c.writer.Error()
}

// Close flushes and closes the underlying file.
func (c *CSVWriter) Close() error {
	c.writer.Flush()
	return c.file.Close()
}