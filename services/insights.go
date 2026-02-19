package services

import (
	"fmt"
	"sort"
	"strings"

	"airbnb-scraper/models"
	"airbnb-scraper/utils"
)

type InsightService struct {
	logger *utils.Logger
}

func NewInsightService(logger *utils.Logger) *InsightService {
	return &InsightService{logger: logger}
}

func (s *InsightService) Generate(listings []*models.Listing) *models.InsightReport {
	report := &models.InsightReport{
		ListingsByLocation: make(map[string]int),
	}

	if len(listings) == 0 {
		return report
	}

	report.TotalListings = len(listings)

	var priceListings []*models.Listing
	var ratedListings []*models.Listing

	for _, l := range listings {
		if l.Platform == "airbnb" {
			report.AirbnbListings++
		}
		if l.Price > 0 {
			priceListings = append(priceListings, l)
		}
		if l.Rating > 0 {
			ratedListings = append(ratedListings, l)
		}
		if l.Location != "" {
			report.ListingsByLocation[l.Location]++
		}
	}

	// Price stats (only listings with price > 0)
	if len(priceListings) > 0 {
		report.MinPrice = priceListings[0].Price
		report.MaxPrice = priceListings[0].Price
		var total float64
		for _, l := range priceListings {
			total += l.Price
			if l.Price < report.MinPrice {
				report.MinPrice = l.Price
			}
			if l.Price > report.MaxPrice {
				report.MaxPrice = l.Price
				report.MostExpensive = l
			}
		}
		report.AveragePrice = round2(total / float64(len(priceListings)))
		report.MinPrice = round2(report.MinPrice)
		report.MaxPrice = round2(report.MaxPrice)
	}

	// Top 5 by rating
	sort.Slice(ratedListings, func(i, j int) bool {
		return ratedListings[i].Rating > ratedListings[j].Rating
	})
	if len(ratedListings) > 5 {
		report.TopRated = ratedListings[:5]
	} else {
		report.TopRated = ratedListings
	}

	return report
}

func (s *InsightService) Print(r *models.InsightReport) {
	sep := strings.Repeat("═", 54)
	thin := strings.Repeat("─", 54)

	fmt.Printf("\n\033[1;35m%s\033[0m\n", sep)
	fmt.Printf("\033[1;35m  📊 AIRBNB SCRAPE INSIGHTS\033[0m\n")
	fmt.Printf("\033[1;35m%s\033[0m\n\n", sep)

	// Overview
	fmt.Printf("\033[1;33m  Overview\033[0m\n")
	fmt.Printf("  %s\n", thin)
	fmt.Printf("  Total listings scraped : \033[1m%d\033[0m\n", r.TotalListings)
	fmt.Printf("  Airbnb listings        : \033[1m%d\033[0m\n", r.AirbnbListings)
	fmt.Println()

	// Price Stats
	fmt.Printf("\033[1;33m  Price Statistics (per night)\033[0m\n")
	fmt.Printf("  %s\n", thin)
	if r.AveragePrice > 0 {
		fmt.Printf("  Average price : \033[1;32m$%.2f\033[0m\n", r.AveragePrice)
		fmt.Printf("  Minimum price : \033[1;32m$%.2f\033[0m\n", r.MinPrice)
		fmt.Printf("  Maximum price : \033[1;32m$%.2f\033[0m\n", r.MaxPrice)
	} else {
		fmt.Printf("  No price data available\n")
	}
	fmt.Println()

	// Most Expensive
	if r.MostExpensive != nil {
		fmt.Printf("\033[1;33m  Most Expensive Listing\033[0m\n")
		fmt.Printf("  %s\n", thin)
		fmt.Printf("  %s\n", truncate(r.MostExpensive.Title, 50))
		fmt.Printf("  Location : %s\n", r.MostExpensive.Location)
		fmt.Printf("  Price    : \033[1;31m$%.2f/night\033[0m\n", r.MostExpensive.Price)
		fmt.Println()
	}

	// ── TOP 5 HIGHEST RATED ──────────────────────────────────────────────
	fmt.Printf("\033[1;33m  Top 5 Highest Rated Properties\033[0m\n")
	fmt.Printf("  %s\n", thin)
	if len(r.TopRated) == 0 {
		fmt.Printf("  No rated listings found\n")
	} else {
		for i, l := range r.TopRated {
			title := truncate(l.Title, 38)
			fmt.Printf("  \033[1m%d.\033[0m %-40s \033[1;32m%.2f ★\033[0m\n",
				i+1, title, l.Rating)
		}
	}
	fmt.Println()

	// Listings by Location
	fmt.Printf("\033[1;33m  Listings by Location\033[0m\n")
	fmt.Printf("  %s\n", thin)
	if len(r.ListingsByLocation) == 0 {
		fmt.Printf("  No location data\n")
	} else {
		// Sort locations by count descending
		type locCount struct {
			loc   string
			count int
		}
		var locs []locCount
		for loc, cnt := range r.ListingsByLocation {
			if loc != "" {
				locs = append(locs, locCount{loc, cnt})
			}
		}
		sort.Slice(locs, func(i, j int) bool {
			return locs[i].count > locs[j].count
		})
		for _, lc := range locs {
			bar := strings.Repeat("█", lc.count)
			fmt.Printf("  %-30s %s (%d)\n", truncate(lc.loc, 28), bar, lc.count)
		}
	}

	fmt.Printf("\n\033[1;35m%s\033[0m\n\n", sep)
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}