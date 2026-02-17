package airbnb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"airbnb-scraper/config"
	"airbnb-scraper/models"
	"airbnb-scraper/utils"
)

const (
	airbnbBase    = "https://www.airbnb.com"
	startURL      = "https://www.airbnb.com/"
	platform      = "airbnb"
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

	// Suppress chromedp internal log noise (harmless version-mismatch warnings).
	// chromedp.NewContext accepts a chromedp.WithLogf option — we create a child
	// context with a no-op logger so "could not unmarshal event" spam is silenced.
	silentCtx, cancelSilent := chromedp.NewContext(allocCtx, chromedp.WithLogf(func(string, ...interface{}) {}))
	defer cancelSilent()
	allocCtx = silentCtx

	// Navigate to the home page and find the "Available next month" section URL
	firstPageURL, err := s.findAvailableNextMonthURL(allocCtx)
	if err != nil {
		return nil, fmt.Errorf("could not locate 'Available next month' section: %w", err)
	}

	s.logger.Info("[airbnb] Found section URL: %s", firstPageURL)

	// Scrape page by page
	currentURL := firstPageURL
	for page := 1; page <= s.cfg.PagesToScrape; page++ {
		s.logger.Info("[airbnb] Scraping page %d — URL: %s", page, currentURL)

		pageListings, nextURL, err := s.scrapePage(allocCtx, currentURL, page)
		if err != nil {
			s.logger.Error("[airbnb] Page %d failed: %v", page, err)
			break
		}

		// Enrich the first cfg.ListingsPerPage listings with detail-page data
		s.enrichListings(allocCtx, pageListings)

		s.mu.Lock()
		s.listings = append(s.listings, pageListings...)
		s.mu.Unlock()

		s.logger.Info("[airbnb] Page %d done — collected %d listings so far",
			page, len(s.listings))

		if nextURL == "" || page == s.cfg.PagesToScrape {
			break
		}
		currentURL = nextURL

		// Polite delay between pages
		time.Sleep(time.Duration(s.cfg.RateLimitMs) * time.Millisecond)
	}

	s.logger.Info("[airbnb] Scrape complete — total raw listings: %d", len(s.listings))
	return s.listings, nil
}

// findAvailableNextMonthURL opens the Airbnb home page, locates the
// "Available next month in …" section, and returns its link.
func (s *Scraper) findAvailableNextMonthURL(allocCtx context.Context) (string, error) {
	var sectionURL string

	err := s.retry.Do("find-available-next-month", func() error {
		ctx, cancel := chromedp.NewContext(allocCtx)
		defer cancel()

		ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
		defer cancelTimeout()

		var links []string

		err := chromedp.Run(ctx,
			chromedp.Navigate(startURL),
			chromedp.Sleep(5*time.Second),

			// Extract all anchor hrefs that look like search results
			chromedp.Evaluate(`
				(function() {
					var results = [];
					// Look for section heading containing "Available next month"
					var headings = document.querySelectorAll('h2, h3, section h1, [data-section-id]');
					for (var i = 0; i < headings.length; i++) {
						if (headings[i].textContent && headings[i].textContent.toLowerCase().includes('available next month')) {
							// Find nearby links in the parent section
							var section = headings[i].closest('section') || headings[i].parentElement;
							if (section) {
								var anchors = section.querySelectorAll('a[href*="/s/"]');
								for (var j = 0; j < anchors.length; j++) {
									results.push(anchors[j].href);
								}
								// Also look for "Show all" or section link
								var sectionLink = section.querySelector('a[href*="search"]');
								if (sectionLink) results.unshift(sectionLink.href);
							}
						}
					}
					// Fallback: find any "See all" button near "available next month"
					var allLinks = document.querySelectorAll('a');
					for (var k = 0; k < allLinks.length; k++) {
						var txt = allLinks[k].textContent.toLowerCase();
						var href = allLinks[k].href || '';
						if ((txt.includes('show all') || txt.includes('see all')) && href.includes('/s/')) {
							results.push(href);
						}
					}
					return results;
				})()
			`, &links),
		)
		if err != nil {
			return fmt.Errorf("chromedp navigate/evaluate: %w", err)
		}

		// Deduplicate and pick the first valid search URL
		seen := make(map[string]bool)
		for _, l := range links {
			if !seen[l] && strings.Contains(l, "/s/") {
				seen[l] = true
				sectionURL = l
				return nil
			}
		}

		// Fallback: try to get any listing URL and derive a search URL
		if sectionURL == "" {
			var fallback string
			_ = chromedp.Run(ctx,
				chromedp.Evaluate(`
					(function(){
						var a = document.querySelector('a[href*="/s/"]');
						return a ? a.href : '';
					})()
				`, &fallback),
			)
			if fallback != "" {
				sectionURL = fallback
				return nil
			}

			// Last resort fallback - use a Bangkok search
			sectionURL = "https://www.airbnb.com/s/Bangkok/homes"
			s.logger.Warn("[airbnb] Could not locate section link on homepage, using fallback: %s", sectionURL)
			return nil
		}

		return nil
	})

	return sectionURL, err
}

// scrapePage loads a search-results page and extracts up to ListingsPerPage cards.
// It returns the raw listings and the URL of the next page (if any).
func (s *Scraper) scrapePage(allocCtx context.Context, pageURL string, pageNum int) ([]*models.RawListing, string, error) {
	var rawListings []*models.RawListing
	var nextURL string

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
		var nextPageURL string

		err := chromedp.Run(ctx,
			chromedp.Navigate(pageURL),
			chromedp.Sleep(6*time.Second),

			// Scroll down to trigger lazy loading
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight / 2)`, nil),
			chromedp.Sleep(2*time.Second),
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
			chromedp.Sleep(2*time.Second),

			// Extract listing cards
			chromedp.Evaluate(`
				(function() {
					var results = [];
					var limit = `+fmt.Sprintf("%d", s.cfg.ListingsPerPage)+`;

					// Try multiple Airbnb card selectors (their DOM changes frequently)
					var cardSelectors = [
						'[data-testid="listing-card-wrapper"]',
						'[itemprop="itemListElement"]',
						'div[data-check-in]',
						'div[class*="listingCard"]',
						'div[class*="StayCard"]',
						'div[class*="roomCard"]'
					];

					var cards = [];
					for (var si = 0; si < cardSelectors.length; si++) {
						cards = document.querySelectorAll(cardSelectors[si]);
						if (cards.length > 0) break;
					}

					// Final fallback: any anchor with /rooms/ in href
					if (cards.length === 0) {
						var anchors = document.querySelectorAll('a[href*="/rooms/"]');
						for (var ai = 0; ai < anchors.length && results.length < limit; ai++) {
							var a = anchors[ai];
							var href = a.href || '';
							if (!href) continue;

							// Walk up to find price/title siblings
							var container = a.closest('div') || a.parentElement;
							var text = container ? container.innerText : a.innerText;
							var lines = text.split('\n').map(function(l){ return l.trim(); }).filter(Boolean);

							results.push({
								title:    lines[0] || a.title || a.innerText || 'N/A',
								price:    lines.find(function(l){ return l.includes('$') || l.includes('฿'); }) || 'N/A',
								location: lines[1] || 'N/A',
								rating:   lines.find(function(l){ return /^\d\.\d+/.test(l); }) || '',
								url:      href
							});
						}
						return results;
					}

					// Parse structured cards
					for (var i = 0; i < Math.min(cards.length, limit); i++) {
						var card = cards[i];

						// Title
						var titleEl = card.querySelector('[data-testid="listing-card-title"], [class*="title"], [class*="Title"], [class*="name"], [class*="Name"], h2, h3');
						var title = titleEl ? titleEl.innerText.trim() : '';

						// Price
						var priceEl = card.querySelector('[class*="price"], [class*="Price"], [data-testid*="price"], span[aria-label*="per night"]');
						var price = priceEl ? priceEl.innerText.trim() : '';
						if (!price) {
							var allSpans = card.querySelectorAll('span');
							for (var s = 0; s < allSpans.length; s++) {
								if (allSpans[s].innerText.includes('$') || allSpans[s].innerText.includes('฿')) {
									price = allSpans[s].innerText.trim();
									break;
								}
							}
						}

						// Location / subtitle
						var locEl = card.querySelector('[class*="subtitle"], [class*="location"], [class*="Location"], [class*="description"], span[class*="stay"]');
						var location = locEl ? locEl.innerText.trim() : '';

						// Rating
						var ratingEl = card.querySelector('[aria-label*="rating"], [class*="rating"], [class*="Rating"], span[class*="review"]');
						var rating = ratingEl ? ratingEl.innerText.trim() : '';
						if (!rating) {
							var spans = card.querySelectorAll('span');
							for (var rs = 0; rs < spans.length; rs++) {
								if (/^4\.\d|^5\.0/.test(spans[rs].innerText.trim())) {
									rating = spans[rs].innerText.trim();
									break;
								}
							}
						}

						// URL
						var linkEl = card.querySelector('a[href*="/rooms/"]') || card.querySelector('a');
						var url = linkEl ? linkEl.href : '';

						if (url || title) {
							results.push({ title: title || 'N/A', price: price || 'N/A', location: location || 'N/A', rating: rating, url: url });
						}
					}

					return results;
				})()
			`, &cards),

			// Find next-page button
			chromedp.Evaluate(`
				(function() {
					var nextSelectors = [
						'a[aria-label="Next"]',
						'[data-testid="pagination-next"]',
						'a[href*="items_offset"]',
						'button[aria-label*="next" i]'
					];
					for (var i = 0; i < nextSelectors.length; i++) {
						var el = document.querySelector(nextSelectors[i]);
						if (el) {
							return el.href || el.getAttribute('data-href') || '';
						}
					}
					// Look for next page link in pagination
					var paginationLinks = document.querySelectorAll('a[href*="cursor"], a[href*="offset"]');
					for (var j = 0; j < paginationLinks.length; j++) {
						var text = paginationLinks[j].innerText.toLowerCase();
						if (text.includes('next') || text === '>') {
							return paginationLinks[j].href;
						}
					}
					return '';
				})()
			`, &nextPageURL),
		)

		if err != nil {
			return fmt.Errorf("chromedp page scrape: %w", err)
		}

		s.logger.Debug("[airbnb] Page %d — found %d cards from DOM", pageNum, len(cards))

		for _, c := range cards {
			if c.URL == "" {
				continue
			}

			// Skip duplicates
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

		nextURL = nextPageURL
		return nil
	})

	return rawListings, nextURL, err
}

// enrichListings visits each listing's detail page to fill in the Description field.
// It uses the WorkerPool for concurrent detail-page fetching.
func (s *Scraper) enrichListings(allocCtx context.Context, listings []*models.RawListing) {
	for _, listing := range listings {
		l := listing // capture loop variable
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

// scrapeDetailPage visits a single listing page and extracts its description / metadata.
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
					// Try known description selectors
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

					// Fallback: collect all <p> text from main content
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

// findChromeBinary searches common locations for a Google Chrome or Chromium
// binary and returns its path. Returns "" if nothing is found, in which case
// chromedp falls back to its own built-in discovery logic.
// No sudo or Chromium install is needed — if you have Google Chrome installed
// normally, this will find it automatically.
func findChromeBinary() string {
	// Respect an explicit override via environment variable
	if bin := os.Getenv("CHROME_BIN"); bin != "" {
		return bin
	}

	// Common binary names to try with PATH lookup first
	names := []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}

	// Absolute paths to check (covers most Ubuntu / Debian layouts)
	absolutePaths := []string{
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"/snap/bin/chromium",
		"/opt/google/chrome/google-chrome",
		"/opt/google/chrome-beta/google-chrome-beta",
		"/usr/local/bin/google-chrome",
	}
	for _, p := range absolutePaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return "" // let chromedp auto-detect
}