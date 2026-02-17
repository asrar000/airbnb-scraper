package airbnb

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"airbnb-scraper/config"
	"airbnb-scraper/models"
	"airbnb-scraper/utils"
)

const (
	// bangkokURL is the fixed starting search URL — Bangkok listings available next month.
	bangkokURL = "https://www.airbnb.com/s/Bangkok/homes?place_id=ChIJ82ENKDJgHTERIEjiXbIAAQE&refinement_paths%5B%5D=%2Fhomes&date_picker_type=FLEXIBLE_DATES&flexible_trip_lengths%5B%5D=WEEKEND_TRIP&flexible_trip_dates%5B%5D=march&search_type=HOMEPAGE_CAROUSEL_CLICK"
	platform   = "airbnb"
	// Airbnb paginates by incrementing items_offset by 18 per page
	pageSize = 18
)

// Scraper orchestrates the Airbnb scraping process.
type Scraper struct {
	cfg        *config.Config
	logger     *utils.Logger
	pool       *utils.WorkerPool
	visitedURL *utils.URLSet
	retry      *utils.RetryConfig

	mu       sync.Mutex
	listings []*models.RawListing
}

// New creates a ready-to-use Airbnb Scraper.
func New(cfg *config.Config, logger *utils.Logger) *Scraper {
	return &Scraper{
		cfg:        cfg,
		logger:     logger,
		pool:       utils.NewWorkerPool(cfg.MaxConcurrency, cfg.RateLimitMs),
		visitedURL: utils.NewURLSet(),
		retry: &utils.RetryConfig{
			MaxAttempts: cfg.MaxRetries,
			BaseDelay:   2 * time.Second,
			Logger:      logger,
		},
		listings: make([]*models.RawListing, 0),
	}
}

// Scrape is the entry point that drives pagination and detail-page scraping.
func (s *Scraper) Scrape() ([]*models.RawListing, error) {
	s.logger.Info("[airbnb] Starting scrape — target: %d pages, %d listings/page",
		s.cfg.PagesToScrape, s.cfg.ListingsPerPage)

	chromeBin := findChromeBinary()
	s.logger.Info("[airbnb] Using browser binary: %s", chromeBin)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-setuid-sandbox", true),
		chromedp.UserAgent("Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "+
			"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
	)
	if chromeBin != "" {
		opts = append(opts, chromedp.ExecPath(chromeBin))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()

	// Suppress "could not unmarshal event" / "unknown PrivateNetworkRequestPolicy" noise.
	silentCtx, cancelSilent := chromedp.NewContext(allocCtx, chromedp.WithLogf(func(string, ...interface{}) {}))
	defer cancelSilent()
	allocCtx = silentCtx

	// Scrape page by page — pagination is handled by bumping items_offset in the URL.
	for page := 1; page <= s.cfg.PagesToScrape; page++ {
		pageURL := buildPageURL(bangkokURL, page)
		s.logger.Info("[airbnb] Scraping page %d — URL: %s", page, pageURL)

		pageListings, err := s.scrapePage(allocCtx, pageURL, page)
		if err != nil {
			s.logger.Error("[airbnb] Page %d failed: %v", page, err)
			break
		}

		if len(pageListings) == 0 {
			s.logger.Warn("[airbnb] Page %d returned 0 listings — stopping pagination", page)
			break
		}

		// Visit each listing's detail page concurrently to get the description.
		s.enrichListings(allocCtx, pageListings)

		s.mu.Lock()
		s.listings = append(s.listings, pageListings...)
		s.mu.Unlock()

		s.logger.Info("[airbnb] Page %d done — collected %d listings so far", page, len(s.listings))

		// Polite delay between pages
		if page < s.cfg.PagesToScrape {
			time.Sleep(time.Duration(s.cfg.RateLimitMs) * time.Millisecond)
		}
	}

	s.logger.Info("[airbnb] Scrape complete — total raw listings: %d", len(s.listings))
	return s.listings, nil
}

// buildPageURL constructs the URL for a given page number by setting items_offset.
// Airbnb uses items_offset = (page-1) * pageSize to paginate search results.
func buildPageURL(base string, page int) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}

	q := u.Query()
	// Remove any existing cursor/offset params to avoid conflicts
	q.Del("cursor")
	q.Del("items_offset")
	q.Del("section_offset")

	offset := (page - 1) * pageSize
	q.Set("items_offset", strconv.Itoa(offset))
	q.Set("section_offset", "0")

	u.RawQuery = q.Encode()
	return u.String()
}

// scrapePage loads a search-results page and extracts up to ListingsPerPage cards.
func (s *Scraper) scrapePage(allocCtx context.Context, pageURL string, pageNum int) ([]*models.RawListing, error) {
	var rawListings []*models.RawListing

	err := s.retry.Do(fmt.Sprintf("scrape-page-%d", pageNum), func() error {
		ctx, cancel := chromedp.NewContext(allocCtx)
		defer cancel()

		ctx, cancelTimeout := context.WithTimeout(ctx, 90*time.Second)
		defer cancelTimeout()

		type cardData struct {
			Title    string `json:"title"`
			Price    string `json:"price"`
			Location string `json:"location"`
			Rating   string `json:"rating"`
			URL      string `json:"url"`
		}

		var cards []cardData

		err := chromedp.Run(ctx,
			chromedp.Navigate(pageURL),
			chromedp.Sleep(6*time.Second),

			// Scroll to trigger lazy-loaded cards
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight / 2)`, nil),
			chromedp.Sleep(2*time.Second),
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
			chromedp.Sleep(2*time.Second),

			// Extract listing cards — tries multiple selector strategies
			chromedp.Evaluate(`
				(function() {
					var results = [];
					var limit = `+fmt.Sprintf("%d", s.cfg.ListingsPerPage)+`;

					// Strategy 1: data-testid wrappers (most reliable when present)
					var cardSelectors = [
						'[data-testid="listing-card-wrapper"]',
						'[itemprop="itemListElement"]',
						'div[data-check-in]',
						'div[class*="listingCard"]',
						'div[class*="StayCard"]'
					];

					var cards = [];
					for (var si = 0; si < cardSelectors.length; si++) {
						cards = document.querySelectorAll(cardSelectors[si]);
						if (cards.length > 0) break;
					}

					// Strategy 2: fallback — collect from room anchor tags directly
					if (cards.length === 0) {
						var seen = {};
						var anchors = document.querySelectorAll('a[href*="/rooms/"]');
						for (var ai = 0; ai < anchors.length && results.length < limit; ai++) {
							var a = anchors[ai];
							var href = a.href || '';
							if (!href || seen[href]) continue;
							seen[href] = true;

							var container = a.closest('div') || a.parentElement;
							var text = container ? container.innerText : a.innerText;
							var lines = text.split('\n').map(function(l){ return l.trim(); }).filter(Boolean);

							results.push({
								title:    lines[0] || a.title || 'N/A',
								price:    lines.find(function(l){ return l.includes('$') || l.includes('฿'); }) || 'N/A',
								location: lines[1] || 'Bangkok',
								rating:   lines.find(function(l){ return /^\d\.\d+/.test(l); }) || '',
								url:      href
							});
						}
						return results;
					}

					// Strategy 1: parse structured cards
					var seen = {};
					for (var i = 0; i < cards.length && results.length < limit; i++) {
						var card = cards[i];

						var titleEl = card.querySelector(
							'[data-testid="listing-card-title"], [class*="title"], [class*="Title"], h2, h3'
						);
						var title = titleEl ? titleEl.innerText.trim() : 'N/A';

						var priceEl = card.querySelector(
							'[class*="price"], [class*="Price"], [data-testid*="price"], span[aria-label*="per night"]'
						);
						var price = priceEl ? priceEl.innerText.trim() : '';
						if (!price) {
							var allSpans = card.querySelectorAll('span');
							for (var sp = 0; sp < allSpans.length; sp++) {
								var t = allSpans[sp].innerText;
								if (t.includes('$') || t.includes('฿')) { price = t.trim(); break; }
							}
						}

						var locEl = card.querySelector(
							'[class*="subtitle"], [class*="location"], [class*="Location"], [class*="description"]'
						);
						var location = locEl ? locEl.innerText.trim() : 'Bangkok';

						var ratingEl = card.querySelector(
							'[aria-label*="rating"], [class*="rating"], [class*="Rating"]'
						);
						var rating = ratingEl ? ratingEl.innerText.trim() : '';
						if (!rating) {
							var rspans = card.querySelectorAll('span');
							for (var rs = 0; rs < rspans.length; rs++) {
								if (/^[4-5]\.\d+/.test(rspans[rs].innerText.trim())) {
									rating = rspans[rs].innerText.trim(); break;
								}
							}
						}

						var linkEl = card.querySelector('a[href*="/rooms/"]') || card.querySelector('a');
						var url = linkEl ? linkEl.href : '';

						if (!url || seen[url]) continue;
						seen[url] = true;

						results.push({
							title:    title || 'N/A',
							price:    price || 'N/A',
							location: location || 'Bangkok',
							rating:   rating,
							url:      url
						});
					}

					return results;
				})()
			`, &cards),
		)

		if err != nil {
			return fmt.Errorf("chromedp page scrape: %w", err)
		}

		s.logger.Debug("[airbnb] Page %d — found %d cards from DOM", pageNum, len(cards))

		for _, c := range cards {
			if c.URL == "" {
				continue
			}
			if !s.visitedURL.Add(c.URL) {
				s.logger.Debug("[airbnb] Skipping duplicate URL: %s", c.URL)
				continue
			}
			rawListings = append(rawListings, &models.RawListing{
				Title:     c.Title,
				RawPrice:  c.Price,
				Location:  c.Location,
				Rating:    c.Rating,
				URL:       c.URL,
				ScrapedAt: time.Now(),
				Platform:  platform,
			})
		}

		return nil
	})

	return rawListings, err
}

// enrichListings visits each listing's detail page concurrently to get the description.
func (s *Scraper) enrichListings(allocCtx context.Context, listings []*models.RawListing) {
	for _, listing := range listings {
		l := listing
		if l.URL == "" {
			continue
		}
		s.pool.Submit(func() {
			desc, err := s.scrapeDetailPage(allocCtx, l.URL)
			if err != nil {
				s.logger.Warn("[airbnb] Detail page failed for %s: %v", l.URL, err)
				return
			}
			l.Description = desc
			s.logger.Debug("[airbnb] Enriched: %s", l.Title)
		})
	}
	s.pool.Wait()
}

// scrapeDetailPage visits a single listing page and extracts its description.
func (s *Scraper) scrapeDetailPage(allocCtx context.Context, url string) (string, error) {
	var description string

	err := s.retry.Do("detail-page", func() error {
		ctx, cancel := chromedp.NewContext(allocCtx)
		defer cancel()

		ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
		defer cancelTimeout()

		return chromedp.Run(ctx,
			chromedp.Navigate(url),
			chromedp.Sleep(4*time.Second),
			chromedp.Evaluate(`
				(function() {
					var selectors = [
						'[data-testid="listing-description-text"]',
						'[class*="description"]',
						'[class*="Description"]',
						'section[aria-label*="description" i] span',
						'div[data-section-id="DESCRIPTION_MODAL_DEFAULT"] span',
						'[data-plugin-in-point-id="DESCRIPTION_DEFAULT"] span'
					];
					for (var i = 0; i < selectors.length; i++) {
						var el = document.querySelector(selectors[i]);
						if (el && el.innerText.trim().length > 30) {
							return el.innerText.trim().substring(0, 500);
						}
					}
					var paragraphs = document.querySelectorAll('main p, article p');
					var texts = [];
					for (var j = 0; j < paragraphs.length && texts.join(' ').length < 400; j++) {
						var t = paragraphs[j].innerText.trim();
						if (t.length > 20) texts.push(t);
					}
					return texts.join(' ').substring(0, 500) || 'No description available';
				})()
			`, &description),
		)
	})

	return description, err
}

// findChromeBinary locates the Chrome/Chromium binary.
// Priority: CHROME_BIN env var → PATH lookup → known absolute paths → chromedp auto-detect.
func findChromeBinary() string {
	if bin := os.Getenv("CHROME_BIN"); bin != "" {
		return bin
	}

	names := []string{
		"google-chrome-stable",
		"google-chrome",
		"chromium",
		"chromium-browser",
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}

	absolutePaths := []string{
		"/usr/bin/google-chrome-stable",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"/snap/bin/chromium",
		"/opt/google/chrome/google-chrome",
		"/usr/local/bin/google-chrome",
	}
	for _, p := range absolutePaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}