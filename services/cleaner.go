package services

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"airbnb-scraper/models"
	"airbnb-scraper/utils"
)

var (
	// priceRegexp captures numeric price values
	priceRegexp = regexp.MustCompile(`[\d,]+(?:\.\d+)?`)
	// nightsRegexp captures "X nights" or "X night" patterns
	nightsRegexp = regexp.MustCompile(`(\d+)\s*nights?`)
	// ratingRegexp captures a numeric rating in the 0.0–5.0 range
	ratingRegexp = regexp.MustCompile(`\b([0-5](?:\.\d{1,2})?)\b`)
)

// Cleaner transforms RawListings into clean, validated Listings.
type Cleaner struct {
	logger *utils.Logger
}

// NewCleaner creates a Cleaner with the given logger.
func NewCleaner(logger *utils.Logger) *Cleaner {
	return &Cleaner{logger: logger}
}

// Clean processes raw listings and returns cleaned records.
func (c *Cleaner) Clean(raw []*models.RawListing) []*models.Listing {
	seen := make(map[string]struct{})
	result := make([]*models.Listing, 0, len(raw))

	for _, r := range raw {
		url := strings.TrimSpace(r.URL)
		if url == "" {
			c.logger.Warn("[cleaner] Dropping listing with empty URL: %s", r.Title)
			continue
		}

		if _, dup := seen[url]; dup {
			c.logger.Debug("[cleaner] Duplicate URL skipped: %s", url)
			continue
		}
		seen[url] = struct{}{}

		listing := &models.Listing{
			Platform:    normalisePlatform(r.Platform),
			Title:       normaliseText(r.Title),
			Price:       c.parsePrice(r.RawPrice),
			Location:    normaliseText(r.Location),
			Rating:      c.parseRating(r.Rating),
			URL:         url,
			Description: normaliseText(r.Description),
			CreatedAt:   time.Now(),
		}

		result = append(result, listing)
	}

	c.logger.Info("[cleaner] Cleaned %d → %d listings (dropped %d)",
		len(raw), len(result), len(raw)-len(result))
	return result
}

// parsePrice extracts price and converts multi-night prices to per-night rate.
// Examples:
//   "$150 night" → 150
//   "$450 for 3 nights" → 150 (450/3)
//   "$1,200 total" with "2 nights" → 600
func (c *Cleaner) parsePrice(raw string) float64 {
	raw = strings.ToLower(raw)
	
	// Remove commas and extract first numeric value
	cleaned := strings.ReplaceAll(raw, ",", "")
	match := priceRegexp.FindString(cleaned)
	if match == "" {
		return 0
	}

	totalPrice, err := strconv.ParseFloat(match, 64)
	if err != nil {
		return 0
	}

	// Check if this is a multi-night price
	nightsMatch := nightsRegexp.FindStringSubmatch(raw)
	if len(nightsMatch) >= 2 {
		nights, err := strconv.Atoi(nightsMatch[1])
		if err == nil && nights > 1 {
			perNightPrice := totalPrice / float64(nights)
			c.logger.Debug("[cleaner] Multi-night price detected: $%.2f for %d nights = $%.2f/night",
				totalPrice, nights, perNightPrice)
			return perNightPrice
		}
	}

	// Single night price
	return totalPrice
}

// parseRating extracts a 0.0–5.0 numeric rating from a raw string.
func (c *Cleaner) parseRating(raw string) float64 {
	match := ratingRegexp.FindStringSubmatch(raw)
	if len(match) < 2 {
		return 0
	}
	val, err := strconv.ParseFloat(match[1], 64)
	if err != nil {
		return 0
	}
	if val < 0 || val > 5 {
		return 0
	}
	return val
}

// normaliseText strips leading/trailing whitespace and collapses internal whitespace.
func normaliseText(s string) string {
	s = strings.TrimSpace(s)
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r)
	})
	return strings.Join(fields, " ")
}

func normalisePlatform(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}