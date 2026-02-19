package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"airbnb-scraper/models"
)

// PostgresWriter persists cleaned listings to PostgreSQL.
type PostgresWriter struct {
	db *sql.DB
}

// NewPostgresWriter opens a connection to PostgreSQL, runs schema migrations,
// and returns a ready-to-use PostgresWriter.
func NewPostgresWriter(dsn string) (*PostgresWriter, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: open: %w", err)
	}

	for i := 0; i < 10; i++ {
		if err = db.Ping(); err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: ping failed after retries: %w", err)
	}

	pw := &PostgresWriter{db: db}
	if err := pw.migrate(); err != nil {
		return nil, fmt.Errorf("postgres: migrate: %w", err)
	}

	return pw, nil
}

func (pw *PostgresWriter) migrate() error {
	_, err := pw.db.Exec(`
		CREATE TABLE IF NOT EXISTS listings (
			id          SERIAL PRIMARY KEY,
			platform    VARCHAR(50)  NOT NULL,
			title       TEXT         NOT NULL,
			price       NUMERIC(10,2) NOT NULL DEFAULT 0,
			location    TEXT         NOT NULL DEFAULT '',
			rating      NUMERIC(4,2) NOT NULL DEFAULT 0,
			url         TEXT         UNIQUE NOT NULL,
			description TEXT         NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
		);

		CREATE INDEX IF NOT EXISTS idx_listings_price    ON listings(price);
		CREATE INDEX IF NOT EXISTS idx_listings_location ON listings(location);
		CREATE INDEX IF NOT EXISTS idx_listings_platform ON listings(platform);
		CREATE INDEX IF NOT EXISTS idx_listings_rating   ON listings(rating);
	`)
	return err
}

// Clear deletes all existing listings from the table.
func (pw *PostgresWriter) Clear() error {
	_, err := pw.db.Exec("DELETE FROM listings")
	if err != nil {
		return fmt.Errorf("postgres: clear: %w", err)
	}
	return nil
}

// Write batch-inserts ALL cleaned listings, clearing old data first.
func (pw *PostgresWriter) Write(listings []*models.Listing) error {
	if len(listings) == 0 {
		return nil
	}

	if err := pw.Clear(); err != nil {
		return err
	}

	const batchSize = 50
	for i := 0; i < len(listings); i += batchSize {
		end := i + batchSize
		if end > len(listings) {
			end = len(listings)
		}
		if err := pw.insertBatch(listings[i:end]); err != nil {
			return err
		}
	}
	return nil
}

func (pw *PostgresWriter) insertBatch(batch []*models.Listing) error {
	valueStrings := make([]string, 0, len(batch))
	valueArgs := make([]interface{}, 0, len(batch)*7)

	for idx, l := range batch {
		base := idx * 7
		valueStrings = append(valueStrings,
			fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d)",
				base+1, base+2, base+3, base+4, base+5, base+6, base+7))
		valueArgs = append(valueArgs,
			l.Platform, l.Title, l.Price, l.Location, l.Rating, l.URL, l.Description)
	}

	query := fmt.Sprintf(`
		INSERT INTO listings (platform, title, price, location, rating, url, description)
		VALUES %s
		ON CONFLICT (url) DO NOTHING
	`, strings.Join(valueStrings, ","))

	_, err := pw.db.Exec(query, valueArgs...)
	return err
}

func (pw *PostgresWriter) Close() error {
	return pw.db.Close()
}

// FetchAll retrieves all stored listings — used by the insight service.
func (pw *PostgresWriter) FetchAll() ([]*models.Listing, error) {
	rows, err := pw.db.Query(`
		SELECT id, platform, title, price, location, rating, url, description, created_at
		FROM listings
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("postgres: fetch all: %w", err)
	}
	defer rows.Close()

	var listings []*models.Listing
	for rows.Next() {
		l := &models.Listing{}
		if err := rows.Scan(
			&l.ID, &l.Platform, &l.Title, &l.Price, &l.Location,
			&l.Rating, &l.URL, &l.Description, &l.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("postgres: scan row: %w", err)
		}
		listings = append(listings, l)
	}
	return listings, rows.Err()
}