package services

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"airbnb-scraper/models"
	"airbnb-scraper/utils"
)

var (
	// Matches "$122", "$1,200", "$122.50"
	priceRegexp = regexp.MustCompile(`\$\s*(\d+(?:,\d{3})*(?:\.\d{2})?)`)

	// Matches "X night" or "X nights" for multi-night total price
	nightsRegexp = regexp.MustCompile(`(\d+)\s*nights?`)

	// Per-night price patterns: "$122 / night", "$122/night", "$122 per night", "$122 night"
	perNightRegexp = regexp.MustCompile(`\$\s*(\d+(?:,\d{3})*(?:\.\d{2})?)\s*(?:/\s*night|per\s+night|\bnight\b)`)

	// "X nights in Location" total pricing block — e.g. "$244 for 2 nights"
	totalForNightsRegexp = regexp.MustCompile(`\$\s*(\d+(?:,\d{3})*(?:\.\d{2})?)\s+for\s+(\d+)\s*nights?`)

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
			Location:    c.parseLocation(r.Location, r.RawPrice),
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

// parsePrice handles the structured price strings produced by the scraper:
//   "$66 for 2 nights"  → 66/2 = $33/night
//   "$73 per night"     → $73/night
//   "$45 for 1 night"   → $45/night
// Falls back to regex extraction for any other format.
func (c *Cleaner) parsePrice(raw string) float64 {
	if raw == "" || raw == "N/A" {
		return 0
	}

	preview := raw
	if len(preview) > 150 {
		preview = preview[:150]
	}
	c.logger.Debug("[cleaner] parsePrice input: %q", preview)

	// Strategy 1: "$X for N nights" — divide total by nights
	if m := totalForNightsRegexp.FindStringSubmatch(raw); len(m) > 2 {
		total := parseDollarAmount(m[1])
		nights, _ := strconv.Atoi(m[2])
		if total > 0 && nights > 0 {
			perNight := math.Round((total/float64(nights))*100) / 100
			c.logger.Debug("[cleaner] $%.2f / %d nights = $%.2f/night", total, nights, perNight)
			return perNight
		}
	}

	// Strategy 2: explicit per-night label
	if m := perNightRegexp.FindStringSubmatch(raw); len(m) > 1 {
		val := parseDollarAmount(m[1])
		if val > 0 {
			c.logger.Debug("[cleaner] Per-night: $%.2f", val)
			return val
		}
	}

	// Strategy 3: first dollar amount on the line (last resort)
	matches := priceRegexp.FindAllStringSubmatch(raw, -1)
	for _, m := range matches {
		if len(m) > 1 {
			val := parseDollarAmount(m[1])
			if val > 0 && val < 10000 {
				c.logger.Debug("[cleaner] Fallback price: $%.2f", val)
				return val
			}
		}
	}

	return 0
}

// parseLocation uses the pre-set section location if it's meaningful,
// otherwise tries to extract it from the raw page text.
func (c *Cleaner) parseLocation(location, rawPageText string) string {
	// Strip known bad prefixes from section names that slipped through
	badPrefixes := []string{
		"Check out homes in ",
		"Available next month in ",
		"Things to do in ",
		"Explore homes in ",
		"Stay near ",
		"Stay in ",
		"Popular homes in ",
		"Homes in ",
		"Guests also checked out ",
	}
	junkPhrases := []string{
		"where you'll be", "add dates", "inspiration",
	}
	isJunk := func(s string) bool {
		lower := strings.ToLower(s)
		for _, j := range junkPhrases {
			if strings.Contains(lower, j) {
				return true
			}
		}
		return false
	}
	stripPrefix := func(s string) string {
		lower := strings.ToLower(s)
		for _, p := range badPrefixes {
			if strings.HasPrefix(lower, strings.ToLower(p)) {
				return strings.TrimSpace(s[len(p):])
			}
		}
		return s
	}

	loc := strings.TrimSpace(location)
	loc = stripPrefix(loc)

	// Keep it if clean
	if loc != "" && loc != "N/A" && loc != "Unknown" &&
		!strings.Contains(loc, "\n") && len(loc) < 80 && !isJunk(loc) {
		return normaliseText(loc)
	}

	// Fallback: extract from page body
	if rawPageText != "" {
		re := regexp.MustCompile(`\d+\s*nights?\s+in\s+([^\n$\d]{3,60})`)
		if m := re.FindStringSubmatch(rawPageText); len(m) > 1 {
			extracted := strings.TrimSpace(m[1])
			if extracted != "" && !isJunk(extracted) {
				return normaliseText(extracted)
			}
		}
	}

	return normaliseText(loc)
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

// ── Helpers ──────────────────────────────────────────────────────────────────

func parseDollarAmount(s string) float64 {
	s = strings.ReplaceAll(s, ",", "")
	val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
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