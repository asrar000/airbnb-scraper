package main

import (
	"fmt"
	"os"

	"airbnb-scraper/config"
	"airbnb-scraper/scraper/airbnb"
	"airbnb-scraper/services"
	"airbnb-scraper/storage"
	"airbnb-scraper/utils"
)

func main() {
	// ── Bootstrap ────────────────────────────────────────────────────────────
	logger := utils.NewLogger()
	cfg := config.Load()

	logger.Info("=== Airbnb Scraping System starting ===")
	logger.Info("Config — pages: %d | listings/page: %d | concurrency: %d | rate: %dms",
		cfg.PagesToScrape, cfg.ListingsPerPage, cfg.MaxConcurrency, cfg.RateLimitMs)

	// ── CSV writer (raw data) ─────────────────────────────────────────────
	csvWriter, err := storage.NewCSVWriter(cfg.CSVOutputPath)
	if err != nil {
		logger.Error("Failed to create CSV writer: %v", err)
		os.Exit(1)
	}
	defer csvWriter.Close()

	// ── PostgreSQL writer (clean data) ───────────────────────────────────
	pgWriter, err := storage.NewPostgresWriter(cfg.DSN())
	if err != nil {
		logger.Error("Failed to connect to PostgreSQL: %v", err)
		logger.Error("Make sure Docker is running: docker compose up -d")
		os.Exit(1)
	}
	defer pgWriter.Close()

	// ── Scrape ────────────────────────────────────────────────────────────
	airbnbScraper := airbnb.New(cfg, logger)
	rawListings, err := airbnbScraper.Scrape()
	if err != nil {
		logger.Error("Airbnb scrape failed: %v", err)
		// Continue with whatever was collected rather than hard-exiting
	}

	if len(rawListings) == 0 {
		logger.Error("No listings were scraped. Exiting.")
		os.Exit(1)
	}

	logger.Info("Scraped %d raw listings — writing to CSV …", len(rawListings))

	// ── Persist raw data to CSV ───────────────────────────────────────────
	if err := csvWriter.WriteRaw(rawListings); err != nil {
		logger.Error("CSV write failed: %v", err)
	} else {
		logger.Info("Raw listings saved to %s", cfg.CSVOutputPath)
	}

	// ── Clean ────────────────────────────────────────────────────────────
	cleaner := services.NewCleaner(logger)
	cleanListings := cleaner.Clean(rawListings)

	if len(cleanListings) == 0 {
		logger.Error("All listings were dropped during cleaning. Exiting.")
		os.Exit(1)
	}

	logger.Info("Cleaned dataset: %d listings", len(cleanListings))

	// ── Persist clean data to PostgreSQL ─────────────────────────────────
	if err := pgWriter.Write(cleanListings); err != nil {
		logger.Error("PostgreSQL write failed: %v", err)
	} else {
		logger.Info("Clean listings stored in PostgreSQL (table: listings)")
	}

	// ── Generate insights from the database ──────────────────────────────
	dbListings, err := pgWriter.FetchAll()
	if err != nil {
		logger.Error("Failed to fetch listings from DB for insights: %v", err)
		// Fall back to in-memory cleaned listings
		dbListings = cleanListings
	}

	insightSvc := services.NewInsightService(logger)
	report := insightSvc.Generate(dbListings)

	// ── Print report ─────────────────────────────────────────────────────
	insightSvc.Print(report)

	fmt.Printf("Done. Raw CSV -> %s | Clean data -> PostgreSQL (listings table)\n\n",
		cfg.CSVOutputPath)
}