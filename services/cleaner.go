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
	// Extract all price patterns like $122, $104, etc.
	priceRegexp = regexp.MustCompile(`\$\s*(\d+(?:,\d{3})*(?:\.\d{2})?)`)
	// nightsRegexp captures "X nights" or "X night" patterns
	nightsRegexp = regexp.MustCompile(`(\d+)\s*nights?`)
	// ratingRegexp captures a numeric rating in the 0.0–5.0 range
	ratingRegexp = regexp.MustCompile(`\b([0-5](?:\.\d{1,2})?)\b`)
)

type Cleaner struct {
	logger *utils.Logger
}

func NewCleaner(logger *utils.Logger) *Cleaner {
	return &Cleaner{logger: logger}
}

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

// parsePrice handles:
// "$122 $104 for 2 nights" → $104 (lowest visible price) / 2 nights = $52/night
// "$71 for 2 nights" → $71 / 2 = $35.5/night
func (c *Cleaner) parsePrice(raw string) float64 {
	if raw == "" || raw == "N/A" {
		return 0
	}

	// Extract all dollar amounts
	matches := priceRegexp.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return 0
	}

	var prices []float64
	for _, match := range matches {
		if len(match) > 1 {
			// Remove commas from numbers like 1,200
			numStr := strings.ReplaceAll(match[1], ",", "")
			val, err := strconv.ParseFloat(numStr, 64)
			if err == nil && val > 0 {
				prices = append(prices, val)
			}
		}
	}

	if len(prices) == 0 {
		return 0
	}

	// If multiple prices (strikethrough + current), take the LOWEST (current price)
	finalPrice := prices[0]
	for _, p := range prices {
		if p < finalPrice {
			finalPrice = p
		}
	}

	// Check for multi-night pricing
	nightsMatch := nightsRegexp.FindStringSubmatch(raw)
	if len(nightsMatch) >= 2 {
		nights, err := strconv.Atoi(nightsMatch[1])
		if err == nil && nights > 1 {
			perNightPrice := finalPrice / float64(nights)
			c.logger.Debug("[cleaner] Price: $%.2f for %d nights = $%.2f/night",
				finalPrice, nights, perNightPrice)
			return perNightPrice
		}
	}

	c.logger.Debug("[cleaner] Single night price: $%.2f", finalPrice)
	return finalPrice
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
	if s == "" || s == "N/A" {
		return ""
	}
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r)
	})
	return strings.Join(fields, " ")
}

func normalisePlatform(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}