package services

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"airbnb-scraper/models"
	"airbnb-scraper/utils"
)

// InsightService computes analytics over the cleaned dataset.
type InsightService struct {
	logger *utils.Logger
}

// NewInsightService creates an InsightService.
func NewInsightService(logger *utils.Logger) *InsightService {
	return &InsightService{logger: logger}
}

// Generate computes the InsightReport from the cleaned listings.
func (s *InsightService) Generate(listings []*models.Listing) *models.InsightReport {
	report := &models.InsightReport{
		ListingsByLocation: make(map[string]int),
	}

	if len(listings) == 0 {
		s.logger.Warn("[insights] No listings to analyse")
		return report
	}

	report.TotalListings = len(listings)

	var (
		totalPrice float64
		minPrice   = math.MaxFloat64
		maxPrice   = -math.MaxFloat64
		rated      []*models.Listing
	)

	for _, l := range listings {
		if strings.ToLower(l.Platform) == "airbnb" {
			report.AirbnbListings++
		}

		if l.Price > 0 {
			totalPrice += l.Price
			if l.Price < minPrice {
				minPrice = l.Price
				report.MinPrice = l.Price
			}
			if l.Price > maxPrice {
				maxPrice = l.Price
				report.MostExpensive = l
				report.MaxPrice = l.Price
			}
		}

		if l.Rating > 0 {
			rated = append(rated, l)
		}

		loc := normaliseLocation(l.Location)
		if loc != "" {
			report.ListingsByLocation[loc]++
		}
	}

	pricedCount := countWithPrice(listings)
	if pricedCount > 0 {
		report.AveragePrice = round2(totalPrice / float64(pricedCount))
	}

	if minPrice == math.MaxFloat64 {
		report.MinPrice = 0
	}

	sort.Slice(rated, func(i, j int) bool {
		return rated[i].Rating > rated[j].Rating
	})
	top := 5
	if len(rated) < top {
		top = len(rated)
	}
	report.TopRated = rated[:top]

	return report
}

// Print renders the InsightReport to the terminal.
func (s *InsightService) Print(report *models.InsightReport) {
	sep := strings.Repeat("─", 50)
	fmt.Printf("\n\033[1;36m%s\033[0m\n", sep)
	fmt.Printf("\033[1;36m   🏠  Vacation Rental Market Insights\033[0m\n")
	fmt.Printf("\033[1;36m%s\033[0m\n\n", sep)

	fmt.Printf("  Total Listings Scraped  : \033[1m%d\033[0m\n", report.TotalListings)
	fmt.Printf("  Airbnb Listings         : \033[1m%d\033[0m\n", report.AirbnbListings)
	fmt.Printf("\n")
	fmt.Printf("  Average Price           : \033[1m$%.2f\033[0m\n", report.AveragePrice)
	fmt.Printf("  Minimum Price           : \033[1m$%.2f\033[0m\n", report.MinPrice)
	fmt.Printf("  Maximum Price           : \033[1m$%.2f\033[0m\n", report.MaxPrice)

	if report.MostExpensive != nil {
		fmt.Printf("\n  \033[1;33mMost Expensive Property:\033[0m\n")
		fmt.Printf("    Title    : %s\n", report.MostExpensive.Title)
		fmt.Printf("    Price    : $%.2f\n", report.MostExpensive.Price)
		fmt.Printf("    Location : %s\n", report.MostExpensive.Location)
		fmt.Printf("    URL      : %s\n", report.MostExpensive.URL)
	}

	if len(report.ListingsByLocation) > 0 {
		fmt.Printf("\n  \033[1;33mListings per Location:\033[0m\n")
		type locCount struct {
			loc   string
			count int
		}
		locs := make([]locCount, 0, len(report.ListingsByLocation))
		for k, v := range report.ListingsByLocation {
			locs = append(locs, locCount{k, v})
		}
		sort.Slice(locs, func(i, j int) bool {
			return locs[i].count > locs[j].count
		})
		for _, lc := range locs {
			fmt.Printf("    %-30s : %d\n", lc.loc, lc.count)
		}
	}

	if len(report.TopRated) > 0 {
		fmt.Printf("\n  \033[1;33mTop %d Highest Rated Properties:\033[0m\n", len(report.TopRated))
		for i, l := range report.TopRated {
			fmt.Printf("    %d. %-40s %.2f ⭐\n", i+1, truncate(l.Title, 40), l.Rating)
		}
	}

	fmt.Printf("\n\033[1;36m%s\033[0m\n\n", sep)
}

func countWithPrice(listings []*models.Listing) int {
	n := 0
	for _, l := range listings {
		if l.Price > 0 {
			n++
		}
	}
	return n
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}

func normaliseLocation(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "N/A" {
		return ""
	}
	for _, sep := range []string{"\n", ","} {
		if idx := strings.Index(raw, sep); idx > 0 {
			raw = raw[:idx]
		}
	}
	return strings.TrimSpace(raw)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
