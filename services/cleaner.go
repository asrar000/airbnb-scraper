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
	priceRegexp  = regexp.MustCompile(`[\d,]+(?:\.\d+)?`)
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

func (c *Cleaner) parsePrice(raw string) float64 {
	cleaned := strings.ReplaceAll(raw, ",", "")
	match := priceRegexp.FindString(cleaned)
	if match == "" {
		return 0
	}
	val, err := strconv.ParseFloat(match, 64)
	if err != nil {
		return 0
	}
	return val
}

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
