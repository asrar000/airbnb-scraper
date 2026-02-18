package airbnb

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"airbnb-scraper/config"
	"airbnb-scraper/models"
	"airbnb-scraper/utils"
)

const (
	startURL = "https://www.airbnb.com/"
	platform = "airbnb"
)

type Scraper struct {
	cfg        *config.Config
	logger     *utils.Logger
	pool       *utils.WorkerPool
	visitedURL *utils.URLSet
	retry      *utils.RetryConfig

	mu       sync.Mutex
	listings []*models.RawListing
}

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

	silentCtx, cancelSilent := chromedp.NewContext(allocCtx, chromedp.WithLogf(func(string, ...interface{}) {}))
	defer cancelSilent()
	allocCtx = silentCtx

	searchURL, err := s.findPopularHomesLink(allocCtx)
	if err != nil {
		return nil, fmt.Errorf("could not find popular homes section: %w", err)
	}

	s.logger.Info("[airbnb] Found popular homes URL: %s", searchURL)

	currentURL := searchURL
	for page := 1; page <= s.cfg.PagesToScrape; page++ {
		s.logger.Info("[airbnb] Scraping page %d", page)

		pageListings, nextURL, err := s.scrapePage(allocCtx, currentURL, page)
		if err != nil {
			s.logger.Error("[airbnb] Page %d failed: %v", page, err)
			break
		}

		if len(pageListings) == 0 {
			s.logger.Warn("[airbnb] Page %d returned 0 listings — stopping", page)
			break
		}

		s.enrichListings(allocCtx, pageListings)

		s.mu.Lock()
		s.listings = append(s.listings, pageListings...)
		s.mu.Unlock()

		s.logger.Info("[airbnb] Page %d done — total collected: %d listings", page, len(s.listings))

		if page >= s.cfg.PagesToScrape {
			s.logger.Info("[airbnb] Reached page limit (%d pages)", s.cfg.PagesToScrape)
			break
		}

		if nextURL == "" {
			s.logger.Warn("[airbnb] No next page URL found — stopping pagination")
			break
		}

		s.logger.Info("[airbnb] Moving to page %d — URL: %s", page+1, nextURL)
		currentURL = nextURL
		time.Sleep(time.Duration(s.cfg.RateLimitMs) * time.Millisecond)
	}

	s.logger.Info("[airbnb] Scrape complete — total raw listings: %d", len(s.listings))
	return s.listings, nil
}

func (s *Scraper) findPopularHomesLink(allocCtx context.Context) (string, error) {
	var sectionURL string

	err := s.retry.Do("find-popular-homes", func() error {
		ctx, cancel := chromedp.NewContext(allocCtx)
		defer cancel()

		ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
		defer cancelTimeout()

		var foundURL string

		err := chromedp.Run(ctx,
			chromedp.Navigate(startURL),
			chromedp.Sleep(5*time.Second),

			chromedp.Evaluate(`
				(function() {
					var headings = document.querySelectorAll('h2, h3, div[role="heading"]');
					for (var i = 0; i < headings.length; i++) {
						var text = headings[i].textContent || '';
						if (text.toLowerCase().includes('popular homes in')) {
							var section = headings[i].closest('section') || 
							              headings[i].closest('div[data-section-id]') ||
							              headings[i].parentElement;
							
							if (section) {
								var showAllLink = section.querySelector('a[aria-label*="Show all"]') ||
								                  section.querySelector('a[href*="/s/"]');
								if (showAllLink && showAllLink.href) {
									return showAllLink.href;
								}
								
								var match = text.match(/popular homes in ([^<]+)/i);
								if (match && match[1]) {
									var location = match[1].trim();
									return 'https://www.airbnb.com/s/' + encodeURIComponent(location) + '/homes';
								}
							}
						}
					}
					
					var sections = document.querySelectorAll('section');
					for (var j = 0; j < sections.length; j++) {
						var links = sections[j].querySelectorAll('a[href*="/rooms/"]');
						if (links.length >= 3) {
							var allLink = sections[j].querySelector('a[href*="/s/"]');
							if (allLink) return allLink.href;
						}
					}
					
					return '';
				})()
			`, &foundURL),
		)

		if err != nil {
			return fmt.Errorf("chromedp evaluate: %w", err)
		}

		if foundURL == "" {
			s.logger.Warn("[airbnb] Could not find popular homes section, using Bangkok fallback")
			foundURL = "https://www.airbnb.com/s/Bangkok/homes"
		}

		sectionURL = foundURL
		return nil
	})

	return sectionURL, err
}

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
			chromedp.Sleep(7*time.Second),

			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight / 3)`, nil),
			chromedp.Sleep(2*time.Second),
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight / 2)`, nil),
			chromedp.Sleep(2*time.Second),
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
			chromedp.Sleep(3*time.Second),

			chromedp.Evaluate(`
				(function() {
					var results = [];
					var limit = `+fmt.Sprintf("%d", s.cfg.ListingsPerPage)+`;
					
					var cardSelectors = [
						'[data-testid="card-container"]',
						'[itemprop="itemListElement"]',
						'div[data-testid="listing-card-wrapper"]',
						'div[class*="cy5jw6o"]'
					];
					
					var cards = [];
					for (var si = 0; si < cardSelectors.length; si++) {
						cards = document.querySelectorAll(cardSelectors[si]);
						if (cards.length > 0) break;
					}
					
					if (cards.length === 0) {
						var roomLinks = document.querySelectorAll('a[href*="/rooms/"]');
						var seen = {};
						for (var ri = 0; ri < roomLinks.length && results.length < limit; ri++) {
							var link = roomLinks[ri];
							var href = link.href;
							if (!href || seen[href]) continue;
							seen[href] = true;
							
							var cardDiv = link.closest('[role="group"]') || 
							              link.closest('div[class*="g1qv1ctd"]') ||
							              link.closest('div');
							
							var innerText = cardDiv ? cardDiv.innerText : link.innerText;
							var lines = innerText.split('\n').map(function(l){return l.trim();}).filter(Boolean);
							
							results.push({
								title:    lines[0] || 'Property',
								price:    lines.find(function(l){return l.match(/\$|฿|€|£/);}) || 'N/A',
								location: lines[1] || 'N/A',
								rating:   lines.find(function(l){return l.match(/^\d\.\d+/);}) || '',
								url:      href
							});
						}
						return results;
					}
					
					var seen = {};
					for (var i = 0; i < cards.length && results.length < limit; i++) {
						var card = cards[i];
						
						var titleEl = card.querySelector('[data-testid="listing-card-title"]') ||
						              card.querySelector('div[id*="title"]') ||
						              card.querySelector('[class*="t1jojoys"]');
						var title = titleEl ? titleEl.innerText.trim() : 'Property';
						
						var priceSpans = card.querySelectorAll('span');
						var price = '';
						for (var ps = 0; ps < priceSpans.length; ps++) {
							var txt = priceSpans[ps].innerText;
							if (txt.match(/\$|฿|€|£/) && txt.match(/\d/)) {
								price = txt.trim();
								break;
							}
						}
						
						var locEl = card.querySelector('[data-testid="listing-card-subtitle"]') ||
						            card.querySelector('span[class*="t6mzqp7"]');
						var location = locEl ? locEl.innerText.trim() : 'N/A';
						
						var ratingEl = card.querySelector('[aria-label*="rating"]') ||
						               card.querySelector('span[class*="r4a59j5"]');
						var rating = '';
						if (ratingEl) {
							var ratingText = ratingEl.innerText || ratingEl.getAttribute('aria-label') || '';
							var ratingMatch = ratingText.match(/(\d\.\d+)/);
							rating = ratingMatch ? ratingMatch[1] : '';
						}
						
						var linkEl = card.querySelector('a[href*="/rooms/"]');
						var url = linkEl ? linkEl.href : '';
						
						if (!url || seen[url]) continue;
						seen[url] = true;
						
						results.push({
							title:    title,
							price:    price || 'N/A',
							location: location,
							rating:   rating,
							url:      url
						});
					}
					
					return results;
				})()
			`, &cards),

			chromedp.Evaluate(`
				(function() {
					var nextBtns = [
						document.querySelector('a[aria-label="Next"]'),
						document.querySelector('a[aria-label="next"]'),
						document.querySelector('[data-testid="pagination-next-button"]'),
						document.querySelector('a[aria-label="Next page"]')
					];
					
					for (var i = 0; i < nextBtns.length; i++) {
						if (nextBtns[i] && nextBtns[i].href) {
							return nextBtns[i].href;
						}
					}
					
					var navLinks = document.querySelectorAll('nav a, div[role="navigation"] a');
					for (var j = 0; j < navLinks.length; j++) {
						var text = navLinks[j].innerText.toLowerCase();
						var ariaLabel = (navLinks[j].getAttribute('aria-label') || '').toLowerCase();
						if (text === 'next' || text === '>' || ariaLabel.includes('next')) {
							return navLinks[j].href;
						}
					}
					
					var allLinks = document.querySelectorAll('a[href]');
					for (var k = 0; k < allLinks.length; k++) {
						var href = allLinks[k].href;
						if (href.includes('items_offset=') || href.includes('cursor=')) {
							var currentOffset = window.location.href.match(/items_offset=(\d+)/);
							var linkOffset = href.match(/items_offset=(\d+)/);
							if (linkOffset && (!currentOffset || parseInt(linkOffset[1]) > parseInt(currentOffset[1] || '0'))) {
								return href;
							}
						}
					}
					
					return '';
				})()
			`, &nextPageURL),
		)

		if err != nil {
			return fmt.Errorf("chromedp page scrape: %w", err)
		}

		s.logger.Debug("[airbnb] Page %d — found %d cards", pageNum, len(cards))

		for _, c := range cards {
			if c.URL == "" {
				continue
			}
			if !s.visitedURL.Add(c.URL) {
				s.logger.Debug("[airbnb] Skipping duplicate: %s", c.URL)
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

func (s *Scraper) enrichListings(allocCtx context.Context, listings []*models.RawListing) {
	for _, listing := range listings {
		l := listing
		if l.URL == "" {
			continue
		}

		s.pool.Submit(func() {
			enriched, err := s.scrapeDetailPage(allocCtx, l.URL)
			if err != nil {
				s.logger.Warn("[airbnb] Detail page failed for %s: %v", l.URL, err)
				return
			}

			if enriched.Title != "" && enriched.Title != "N/A" && enriched.Title != "Property" {
				l.Title = enriched.Title
			}
			if enriched.RawPrice != "" && enriched.RawPrice != "N/A" {
				l.RawPrice = enriched.RawPrice
			}
			if enriched.Location != "" && enriched.Location != "N/A" {
				l.Location = enriched.Location
			}
			if enriched.Rating != "" {
				l.Rating = enriched.Rating
			}
			l.Description = enriched.Description

			s.logger.Debug("[airbnb] Enriched: %s", l.Title)
		})
	}
	s.pool.Wait()
}

func (s *Scraper) scrapeDetailPage(allocCtx context.Context, url string) (*models.RawListing, error) {
	listing := &models.RawListing{URL: url, Platform: platform}

	err := s.retry.Do("detail-page", func() error {
		ctx, cancel := chromedp.NewContext(allocCtx)
		defer cancel()

		ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
		defer cancelTimeout()

		type detailData struct {
			Title       string `json:"title"`
			Price       string `json:"price"`
			Location    string `json:"location"`
			Rating      string `json:"rating"`
			Description string `json:"description"`
		}

		var details detailData

		err := chromedp.Run(ctx,
			chromedp.Navigate(url),
			chromedp.Sleep(5*time.Second),

			chromedp.Evaluate(`
				(function() {
					var result = {
						title: '',
						price: '',
						location: '',
						rating: '',
						description: ''
					};
					
					var titleEl = document.querySelector('h1[class*="hpipapi"]') ||
					              document.querySelector('h1') ||
					              document.querySelector('[data-section-id="TITLE_DEFAULT"] h1');
					if (titleEl) result.title = titleEl.innerText.trim();
					
					var priceEls = document.querySelectorAll('span');
					for (var p = 0; p < priceEls.length; p++) {
						var txt = priceEls[p].innerText;
						if (txt.match(/\$|฿|€|£/) && txt.match(/\d/) && txt.length < 50) {
							result.price = txt.trim();
							break;
						}
					}
					
					var locEl = document.querySelector('[data-section-id="LOCATION_DEFAULT"] h2') ||
					            document.querySelector('button[aria-label*="location"] span') ||
					            document.querySelector('div[class*="l7n4lsf"] span');
					if (locEl) result.location = locEl.innerText.trim();
					
					var ratingEl = document.querySelector('button[aria-label*="rating"]') ||
					               document.querySelector('span[class*="r1dxllyb"]') ||
					               document.querySelector('[data-testid="pdp-reviews-highlight-banner"] span');
					if (ratingEl) {
						var ratingText = ratingEl.innerText || ratingEl.getAttribute('aria-label') || '';
						var ratingMatch = ratingText.match(/(\d\.\d+)/);
						result.rating = ratingMatch ? ratingMatch[1] : '';
					}
					
					var descSelectors = [
						'[data-section-id="DESCRIPTION_DEFAULT"] span',
						'div[class*="ll4r2nl"] div[class*="lgx66tx"] span',
						'[data-plugin-in-point-id="DESCRIPTION_DEFAULT"] span'
					];
					for (var i = 0; i < descSelectors.length; i++) {
						var descEl = document.querySelector(descSelectors[i]);
						if (descEl && descEl.innerText.length > 30) {
							result.description = descEl.innerText.trim().substring(0, 500);
							break;
						}
					}
					
					if (!result.description) {
						var paras = document.querySelectorAll('main p');
						var texts = [];
						for (var j = 0; j < paras.length && texts.join(' ').length < 400; j++) {
							var t = paras[j].innerText.trim();
							if (t.length > 20) texts.push(t);
						}
						result.description = texts.join(' ').substring(0, 500) || 'No description available';
					}
					
					return result;
				})()
			`, &details),
		)

		if err != nil {
			return fmt.Errorf("chromedp detail extract: %w", err)
		}

		listing.Title = details.Title
		listing.RawPrice = details.Price
		listing.Location = details.Location
		listing.Rating = details.Rating
		listing.Description = details.Description

		return nil
	})

	return listing, err
}

func findChromeBinary() string {
	if bin := os.Getenv("CHROME_BIN"); bin != "" {
		return bin
	}

	names := []string{"google-chrome-stable", "google-chrome", "chromium", "chromium-browser"}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}

	paths := []string{
		"/usr/bin/google-chrome-stable",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"/snap/bin/chromium",
		"/opt/google/chrome/google-chrome",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return ""
}