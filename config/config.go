package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	PostgresHost     string
	PostgresPort     string
	PostgresUser     string
	PostgresPassword string
	PostgresDB       string
	PostgresSSLMode  string

	MaxConcurrency  int
	RateLimitMs     int
	MaxRetries      int
	PagesToScrape   int
	ListingsPerPage int

	CSVOutputPath string
	ChromeBin     string
}

// Load reads the .env file and returns a populated Config struct.
func Load() *Config {
	if err := godotenv.Load(); err != nil {
		log.Println("[config] No .env file found, falling back to system env vars")
	}

	return &Config{
		PostgresHost:     getEnv("POSTGRES_HOST", "localhost"),
		PostgresPort:     getEnv("POSTGRES_PORT", "5432"),
		PostgresUser:     getEnv("POSTGRES_USER", "scraper"),
		PostgresPassword: getEnv("POSTGRES_PASSWORD", "scraper123"),
		PostgresDB:       getEnv("POSTGRES_DB", "rental_db"),
		PostgresSSLMode:  getEnv("POSTGRES_SSLMODE", "disable"),

		MaxConcurrency:  getEnvInt("MAX_CONCURRENCY", 3),
		RateLimitMs:     getEnvInt("RATE_LIMIT_MS", 2000),
		MaxRetries:      getEnvInt("MAX_RETRIES", 3),
		PagesToScrape:   getEnvInt("PAGES_TO_SCRAPE", 2),
		ListingsPerPage: getEnvInt("LISTINGS_PER_PAGE", 5),

		CSVOutputPath: getEnv("CSV_OUTPUT_PATH", "./output/raw_listings.csv"),
		ChromeBin:     getEnv("CHROME_BIN", ""),
	}
}

// DSN returns the PostgreSQL connection string.
func (c *Config) DSN() string {
	return "host=" + c.PostgresHost +
		" port=" + c.PostgresPort +
		" user=" + c.PostgresUser +
		" password=" + c.PostgresPassword +
		" dbname=" + c.PostgresDB +
		" sslmode=" + c.PostgresSSLMode
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		n, err := strconv.Atoi(val)
		if err == nil {
			return n
		}
	}
	return fallback
}
