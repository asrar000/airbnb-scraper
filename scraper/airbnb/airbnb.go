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
	startURL            = "https://www.airbnb.com/"
	platform            = "airbnb"
	listingsPerSection  = 4
)

// section represents a named homepage section and the listing URLs inside it.
type section struct {
	Name string
	URLs []string
}

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

// Scrape is the main entry point. It:
//  1. Opens airbnb.com
//  2. Discovers all named sections (e.g. "Stay near Wat Saket…", "Stay in Bang Rak…")
//  3. For each section scrapes up to listingsPerSection listings
//  4. Enriches each listing from its detail page
func (s *Scraper) Scrape() ([]*models.RawListing, error) {
	s.logger.Info("[airbnb] Starting scrape — %d listings per section", listingsPerSection)

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

	// Suppress ALL chromedp/CDP log noise (cookiePart errors, PrivateNetworkRequestPolicy, etc.)
	silentCtx, cancelSilent := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(string, ...interface{}) {}),
		chromedp.WithErrorf(func(string, ...interface{}) {}),
		chromedp.WithDebugf(func(string, ...interface{}) {}),
	)
	defer cancelSilent()
	allocCtx = silentCtx

	// ── Step 1: discover all sections on the homepage ─────────────────────
	s.logger.Info("[airbnb] Loading homepage to discover sections…")
	sections, err := s.discoverSections(allocCtx)
	if err != nil {
		return nil, fmt.Errorf("could not discover homepage sections: %w", err)
	}

	if len(sections) == 0 {
		return nil, fmt.Errorf("no sections found on homepage")
	}

	s.logger.Info("[airbnb] Found %d sections on homepage", len(sections))
	for i, sec := range sections {
		s.logger.Info("[airbnb]   Section %d: %q (%d listing URLs)", i+1, sec.Name, len(sec.URLs))
	}

	// ── Step 2: process each section ──────────────────────────────────────
	totalSections := len(sections)
	for secIdx, sec := range sections {
		secNum := secIdx + 1
		s.printSectionBanner(secNum, totalSections, sec.Name, len(sec.URLs))

		if len(sec.URLs) == 0 {
			s.logger.Warn("[airbnb] Section %q has no listings — skipping", sec.Name)
			continue
		}

		// Limit to listingsPerSection per section
		urls := sec.URLs
		if len(urls) > listingsPerSection {
			urls = urls[:listingsPerSection]
		}

		// Scrape basic card info for each URL in the section
		var sectionListings []*models.RawListing
		sectionLocation := extractLocationFromSection(sec.Name)
		for i, u := range urls {
			if !s.visitedURL.Add(u) {
				s.logger.Debug("[airbnb] Duplicate URL skipped: %s", u)
				continue
			}
			s.logger.Info("[airbnb]   [%d/%d] Fetching card: %s", i+1, len(urls), u)
			sectionListings = append(sectionListings, &models.RawListing{
				URL:       u,
				ScrapedAt: time.Now(),
				Platform:  platform,
				Location:  sectionLocation, // extracted clean location from section name
			})
		}

		if len(sectionListings) == 0 {
			s.logger.Warn("[airbnb] Section %q yielded 0 new listings after dedup", sec.Name)
			s.printSectionDone(sec.Name)
			continue
		}

		// ── Step 3: enrich from detail pages ──────────────────────────────
		s.logger.Info("[airbnb]   Enriching %d listings from detail pages…", len(sectionListings))
		s.enrichListings(allocCtx, sectionListings)

		// Print each enriched listing
		for i, l := range sectionListings {
			s.logger.Info("[airbnb]   ✓ [%d/%d] %s | %s | %s",
				i+1, len(sectionListings),
				truncateStr(l.Title, 40),
				l.Location,
				l.RawPrice[:min(len(l.RawPrice), 30)],
			)
		}

		s.mu.Lock()
		s.listings = append(s.listings, sectionListings...)
		total := len(s.listings)
		s.mu.Unlock()

		s.printSectionDone(sec.Name)
		s.logger.Info("[airbnb] Running total: %d listings", total)

		time.Sleep(time.Duration(s.cfg.RateLimitMs) * time.Millisecond)
	}

	s.logger.Info("[airbnb] ══════════════════════════════════════════")
	s.logger.Info("[airbnb] Scrape complete — total raw listings: %d", len(s.listings))
	s.logger.Info("[airbnb] ══════════════════════════════════════════")
	return s.listings, nil
}

// ── Section discovery ────────────────────────────────────────────────────────

// discoverSections navigates to the Airbnb homepage and returns all named
// listing sections together with the room URLs found inside each.
func (s *Scraper) discoverSections(allocCtx context.Context) ([]section, error) {
	var sections []section

	err := s.retry.Do("discover-sections", func() error {
		ctx, cancel := chromedp.NewContext(allocCtx)
		defer cancel()

		ctx, cancelTimeout := context.WithTimeout(ctx, 90*time.Second)
		defer cancelTimeout()

		type jsSection struct {
			Name string   `json:"name"`
			URLs []string `json:"urls"`
		}
		var jsSections []jsSection

		err := chromedp.Run(ctx,
			chromedp.Navigate(startURL),
			chromedp.Sleep(6*time.Second),

			// Scroll to load lazy sections
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight * 0.3)`, nil),
			chromedp.Sleep(2*time.Second),
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight * 0.6)`, nil),
			chromedp.Sleep(2*time.Second),
			chromedp.Evaluate(`window.scrollTo(0, document.body.scrollHeight)`, nil),
			chromedp.Sleep(3*time.Second),

			chromedp.Evaluate(`
				(function() {
					var results = [];
					var globalSeen = {};

					function collectURLs(container) {
						var urls = [];
						var seen = {};
						var links = container.querySelectorAll('a[href*="/rooms/"]');
						links.forEach(function(a) {
							var clean = a.href.split('?')[0];
							if (clean && !seen[clean] && !globalSeen[clean]) {
								seen[clean] = true;
								urls.push(clean);
							}
						});
						return urls;
					}

					function addSection(name, urls) {
						if (!name || urls.length === 0) return;
						name = name.trim().replace(/\s+/g, ' ');
						if (name.length < 4 || name.length > 120) return;
						// Skip non-listing sections
						if (/inspiration|airbnb your home|become a host|support|career|investor|privacy|cookie|news|blog|press|gift/i.test(name)) return;
						// Avoid duplicates
						var dup = results.some(function(r) { return r.name === name; });
						if (dup) return;
						urls.forEach(function(u) { globalSeen[u] = true; });
						results.push({ name: name, urls: urls });
					}

					// Strategy 1: data-section-id containers (Airbnb's primary structure)
					var sectionEls = document.querySelectorAll('[data-section-id]');
					sectionEls.forEach(function(el) {
						var urls = collectURLs(el);
						if (urls.length === 0) return;
						// Find heading within this section
						var heading = el.querySelector('h2, h3, [role="heading"], div[class*="title"]');
						var name = heading ? (heading.innerText || heading.textContent || '').trim() : '';
						// Fallback: use data-section-id value
						if (!name) name = el.getAttribute('data-section-id') || '';
						addSection(name, urls);
					});

					// Strategy 2: Walk all headings and find nearby room links (broader net)
					if (results.length === 0) {
						var headings = document.querySelectorAll('h2, h3');
						headings.forEach(function(h) {
							var name = (h.innerText || h.textContent || '').trim();
							if (!name) return;

							// Search upward for a container with room links
							var el = h;
							for (var depth = 0; depth < 6; depth++) {
								el = el.parentElement;
								if (!el) break;
								var links = el.querySelectorAll('a[href*="/rooms/"]');
								if (links.length >= 2) {
									var urls = collectURLs(el);
									addSection(name, urls);
									break;
								}
							}
						});
					}

					// Strategy 3: Group all room links by their nearest named ancestor
					if (results.length === 0) {
						var allLinks = document.querySelectorAll('a[href*="/rooms/"]');
						var buckets = {};
						allLinks.forEach(function(a) {
							var clean = a.href.split('?')[0];
							if (!clean || globalSeen[clean]) return;
							// Walk up to find a named parent
							var el = a;
							var label = 'Other';
							for (var d = 0; d < 10; d++) {
								el = el.parentElement;
								if (!el) break;
								var h = el.querySelector('h2, h3');
								if (h) { label = (h.innerText || '').trim() || label; break; }
							}
							if (!buckets[label]) buckets[label] = [];
							if (buckets[label].indexOf(clean) === -1) {
								buckets[label].push(clean);
								globalSeen[clean] = true;
							}
						});
						Object.keys(buckets).forEach(function(k) {
							addSection(k, buckets[k]);
						});
					}

					return results;
				})()
			`, &jsSections),
		)

		if err != nil {
			return fmt.Errorf("chromedp discover sections: %w", err)
		}

		if len(jsSections) == 0 {
			// Debug: log what headings and room links exist on the page
			var debugInfo string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`
				(function() {
					var h = Array.from(document.querySelectorAll('h2,h3')).slice(0,10).map(function(e){ return e.innerText.trim(); }).join(' | ');
					var links = document.querySelectorAll('a[href*="/rooms/"]').length;
					var sections = document.querySelectorAll('[data-section-id]').length;
					return 'headings: ' + h + ' | room_links: ' + links + ' | data-section-id els: ' + sections;
				})()
			`, &debugInfo))
			s.logger.Warn("[airbnb] Section discovery debug: %s", debugInfo)
		}

		for _, js := range jsSections {
			sections = append(sections, section{
				Name: strings.TrimSpace(js.Name),
				URLs: js.URLs,
			})
		}
		return nil
	})

	return sections, err
}

// ── Detail page enrichment ───────────────────────────────────────────────────

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

			// Protect the clean section-derived location — only overwrite if it's bad
			// and the enriched location is genuinely better
			badLocation := func(loc string) bool {
				junk := []string{
					"where you'll be", "available next month", "add dates",
					"check out homes", "things to do", "inspiration",
				}
				lower := strings.ToLower(loc)
				for _, b := range junk {
					if strings.Contains(lower, b) {
						return true
					}
				}
				return false
			}
			currentLocBad := l.Location == "" || l.Location == "Unknown" || badLocation(l.Location)
			enrichedLocGood := enriched.Location != "" && enriched.Location != "N/A" && !badLocation(enriched.Location)
			if currentLocBad && enrichedLocGood {
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

	// Use check-in 7 days from now, check-out 9 days from now (2 nights)
	// This ensures prices are always shown
	checkIn := time.Now().AddDate(0, 0, 7)
	checkOut := time.Now().AddDate(0, 0, 9)
	checkInStr := checkIn.Format("1/2/2006")   // Airbnb date input format: M/D/YYYY
	checkOutStr := checkOut.Format("1/2/2006")

	err := s.retry.Do("detail-page", func() error {
		ctx, cancel := chromedp.NewContext(allocCtx)
		defer cancel()

		ctx, cancelTimeout := context.WithTimeout(ctx, 90*time.Second)
		defer cancelTimeout()

		type detailData struct {
			Title       string `json:"title"`
			Price       string `json:"price"`
			NeedsDates  bool   `json:"needsDates"`
			Location    string `json:"location"`
			Rating      string `json:"rating"`
			Description string `json:"description"`
		}

		var details detailData

		// Step 1: navigate and do initial check
		err := chromedp.Run(ctx,
			chromedp.Navigate(url),
			chromedp.Sleep(5*time.Second),

			// Scroll UP to top first — booking widget with price is near the top right
			chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
			chromedp.Sleep(1*time.Second),

			// Check if dates need to be entered and grab initial data
			chromedp.Evaluate(`
				(function() {
					var result = {
						title: '', price: '', needsDates: false,
						location: '', rating: '', description: ''
					};

					var h1 = document.querySelector('h1');
					if (h1) result.title = h1.innerText.trim();

					// Check if page is asking for dates
					var bodyText = document.body.innerText;
					result.needsDates = (
						bodyText.toLowerCase().includes('add dates for prices') ||
						bodyText.toLowerCase().includes('enter dates') ||
						bodyText.toLowerCase().includes('add dates to see the total price') ||
						bodyText.toLowerCase().includes('add your travel dates')
					);

					return result;
				})()
			`, &details),
		)
		if err != nil {
			return fmt.Errorf("chromedp navigate: %w", err)
		}

		// Step 2: if dates needed, enter them via the booking widget
		if details.NeedsDates {
			s.logger.Debug("[airbnb] Entering dates for %s (check-in: %s, check-out: %s)", url, checkInStr, checkOutStr)

			_ = chromedp.Run(ctx,
				// Scroll to top to make sure booking sidebar is visible
				chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
				chromedp.Sleep(1*time.Second),

				// Click the check-in field in the booking sidebar to open date picker
				chromedp.Evaluate(`
					(function() {
						var selectors = [
							'[data-testid="structured-search-input-field-split-dates-0"]',
							'[data-testid="change-dates-checkIn"]',
							'div[data-testid*="checkin"]',
							'div[aria-label*="Check-in"]',
							'div[aria-label*="check-in"]',
							'div[class*="checkin"] input',
						];
						for (var i = 0; i < selectors.length; i++) {
							var el = document.querySelector(selectors[i]);
							if (el) { el.click(); return 'clicked: ' + selectors[i]; }
						}
						// Last resort: find booking panel and click first date area
						var panel = document.querySelector('[data-section-id="BOOK_IT_SIDEBAR"]') ||
						            document.querySelector('[data-plugin-in-point-id="BOOK_IT_SIDEBAR"]');
						if (panel) {
							var inputs = panel.querySelectorAll('input, div[role="button"], button');
							for (var j = 0; j < inputs.length; j++) {
								var label = (inputs[j].getAttribute('aria-label') || inputs[j].innerText || '').toLowerCase();
								if (label.includes('check-in') || label.includes('checkin') || label.includes('dates')) {
									inputs[j].click();
									return 'clicked panel input: ' + label;
								}
							}
						}
						return 'no check-in found';
					})()
				`, nil),
				chromedp.Sleep(2*time.Second),

				// Type check-in date using keyboard
				chromedp.KeyEvent(checkInStr),
				chromedp.Sleep(1*time.Second),
				chromedp.KeyEvent("\t"), // Tab to check-out
				chromedp.Sleep(500*time.Millisecond),
				chromedp.KeyEvent(checkOutStr),
				chromedp.Sleep(1*time.Second),
				chromedp.KeyEvent("\r"), // Enter to confirm
				chromedp.Sleep(3*time.Second),

				// Scroll back to top so sidebar price is visible
				chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
				chromedp.Sleep(2*time.Second),
			)
		}

		// Step 3: scroll to top, wait for booking widget to show price, then extract
		var priceResult string
		err = chromedp.Run(ctx,
			chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
			chromedp.Sleep(2*time.Second),

			chromedp.Evaluate(`
				(function() {
					// The booking sidebar is sticky on the right side
					// Price appears ABOVE the calendar widget like: [$73] $66 For 2 nights
					var panelSelectors = [
						'[data-section-id="BOOK_IT_SIDEBAR"]',
						'[data-plugin-in-point-id="BOOK_IT_SIDEBAR"]',
						'[data-section-id="BOOK_IT_FLOATING_FOOTER"]',
						'[data-testid="booking-panel"]',
						'div[class*="bookItSidebar"]',
						'div[class*="book-it"]',
					];

					for (var pi = 0; pi < panelSelectors.length; pi++) {
						var panel = document.querySelector(panelSelectors[pi]);
						if (!panel) continue;

						var text = (panel.innerText || '').trim();
						if (!text || !text.includes('$')) continue;

						// Parse all lines from the panel
						var lines = text.split('\n')
							.map(function(l) { return l.trim(); })
							.filter(function(l) { return l.length > 0; });

						var currentPrice = 0;
						var nights = 0;

						// Find "For N nights" line to get night count
						for (var li = 0; li < lines.length; li++) {
							var nm = lines[li].match(/[Ff]or\s+(\d+)\s*nights?/);
							if (nm) { nights = parseInt(nm[1]); break; }
						}

						// Collect all dollar amounts in order
						// The LAST dollar amount before "For N nights" is the current price
						// (strikethrough original comes first, discounted comes last)
						var amounts = [];
						for (var li = 0; li < lines.length; li++) {
							var line = lines[li];
							// Skip lines that are clearly not price lines
							if (line.toLowerCase().includes('cleaning fee')) continue;
							if (line.toLowerCase().includes('service fee')) continue;
							if (line.toLowerCase().includes('taxes')) continue;
							if (line.toLowerCase().includes('total')) continue;

							var m = line.match(/\$\s*(\d[\d,]*(?:\.\d{2})?)/);
							if (m) {
								var val = parseFloat(m[1].replace(/,/g, ''));
								if (val > 0 && val < 100000) amounts.push(val);
							}
						}

						if (amounts.length === 0) continue;

						// When multiple prices: first=original(strikethrough), last=current(discounted)
						// When single price: that IS the current price
						currentPrice = amounts[amounts.length - 1];

						if (currentPrice > 0) {
							if (nights > 0) {
								return '$' + currentPrice + ' for ' + nights + ' nights';
							}
							return '$' + currentPrice + ' per night';
						}
					}

					// Fallback: scan visible page for price near "night" keyword
					// Look for pattern like "$66\nnight" or "$66 / night"
					var allLines = document.body.innerText.split('\n');
					for (var i = 0; i < allLines.length - 1; i++) {
						var line = allLines[i].trim();
						var nextLine = allLines[i+1].trim().toLowerCase();
						if (line.match(/^\$\d+$/) && nextLine === 'night') {
							return line + ' per night';
						}
						if (line.match(/\$\d+/) && (nextLine.includes('night') || line.toLowerCase().includes('night'))) {
							return line;
						}
					}

					return '';
				})()
			`, &priceResult),
		)
		if err != nil {
			return fmt.Errorf("chromedp price extract: %w", err)
		}

		// Step 4: extract remaining fields
		var restData detailData
		err = chromedp.Run(ctx,
			// Expand description
			chromedp.Evaluate(`
				(function() {
					var buttons = document.querySelectorAll('button');
					for (var i = 0; i < buttons.length; i++) {
						var text = buttons[i].innerText.toLowerCase();
						if (text.includes('show more') || text.includes('read more')) {
							buttons[i].click();
							return true;
						}
					}
					return false;
				})()
			`, nil),
			chromedp.Sleep(1*time.Second),

			chromedp.Evaluate(`
				(function() {
					var result = { title: '', price: '', needsDates: false, location: '', rating: '', description: '' };

					// Location strategy 1: subtitle below images
					// e.g. "Entire rental unit in Khet Suan Luang, Thailand"
					// e.g. "Private room in Bang Rak, Thailand"
					var subtitleEls = document.querySelectorAll('h2, [data-section-id="OVERVIEW_DEFAULT"] h2, div[class*="t1veemd9"], div[data-testid*="subtitle"]');
					for (var si = 0; si < subtitleEls.length; si++) {
						var st = (subtitleEls[si].innerText || '').trim();
						var inMatch = st.match(/\bin\s+([^,\n]+(?:,\s*[^\n]+)?)/i);
						if (inMatch && inMatch[1] && inMatch[1].length < 80) {
							result.location = inMatch[1].trim();
							break;
						}
					}

					// Location strategy 2: "X nights in [Location]" pattern
					if (!result.location) {
						var allText = document.body.innerText;
						var nightsMatch = allText.match(/\d+\s*nights?\s+in\s+([^\n$]{3,60})/i);
						if (nightsMatch) result.location = nightsMatch[1].trim();
					}

					// Location strategy 3: LOCATION_DEFAULT section
					if (!result.location) {
						var locSelectors = [
							'[data-section-id="LOCATION_DEFAULT"] h2',
							'button[aria-label*="location"]'
						];
						for (var ls = 0; ls < locSelectors.length; ls++) {
							var el = document.querySelector(locSelectors[ls]);
							if (el) { result.location = el.innerText.trim(); break; }
						}
					}

					// Rating
					var ratingEl = document.querySelector('[aria-label*="rating"]');
					if (ratingEl) {
						var rt = ratingEl.getAttribute('aria-label') || ratingEl.innerText || '';
						var rm = rt.match(/(\d\.\d+)/);
						result.rating = rm ? rm[1] : '';
					}

					// Description
					var descSelectors = [
						'[data-section-id="DESCRIPTION_DEFAULT"]',
						'div[class*="ll4r2nl"]'
					];
					for (var i = 0; i < descSelectors.length; i++) {
						var descEl = document.querySelector(descSelectors[i]);
						if (descEl) {
							var t = descEl.innerText.trim();
							if (t.length > 30) { result.description = t.substring(0, 1000); break; }
						}
					}
					if (!result.description || result.description.length < 30) {
						var paras = document.querySelectorAll('main p');
						var texts = [];
						for (var j = 0; j < paras.length && texts.join(' ').length < 800; j++) {
							var pt = paras[j].innerText.trim();
							if (pt.length > 20) texts.push(pt);
						}
						if (texts.length > 0) result.description = texts.join(' ').substring(0, 1000);
					}
					if (!result.description) result.description = 'Description not available';

					return result;
				})()
			`, &restData),
		)
		if err != nil {
			return fmt.Errorf("chromedp rest extract: %w", err)
		}

		listing.Title = details.Title
		listing.RawPrice = priceResult
		listing.Location = restData.Location
		listing.Rating = restData.Rating
		listing.Description = restData.Description

		s.logger.Debug("[airbnb] Price extracted: %q for %s", priceResult, url)
		return nil
	})

	return listing, err
}

// ── Terminal progress helpers ────────────────────────────────────────────────

func (s *Scraper) printSectionBanner(current, total int, name string, urlCount int) {
	sep := strings.Repeat("─", 55)
	fmt.Printf("\n\033[1;34m%s\033[0m\n", sep)
	fmt.Printf("\033[1;34m  📍 Section [%d/%d]: %s\033[0m\n", current, total, name)
	fmt.Printf("\033[1;34m     Found %d listing URLs — scraping up to %d\033[0m\n", urlCount, listingsPerSection)
	fmt.Printf("\033[1;34m%s\033[0m\n", sep)
}

func (s *Scraper) printSectionDone(name string) {
	fmt.Printf("\n\033[1;32m  ✅ Section done: %q — moving to next\033[0m\n\n", name)
}

// ── Utilities ────────────────────────────────────────────────────────────────

// extractLocationFromSection strips the section title prefix to get the bare location.
// Examples:
//   "Stay near Wat Saket Ratchaworamahawihan" → "Wat Saket Ratchaworamahawihan"
//   "Stay in Bang Rak"                        → "Bang Rak"
//   "Popular homes in Amphoe Bang Phli"       → "Amphoe Bang Phli"
//   "Guests also checked out Bang Kapi"       → "Bang Kapi"
//   "Homes in Amphoe Pak Kret"                → "Amphoe Pak Kret"
//   "Check out homes in Johor Bahru District" → "Johor Bahru District"
//   "Available next month in Sydney"          → "Sydney"
//   "Things to do in Tokyo"                   → "Tokyo"
func extractLocationFromSection(name string) string {
	// Strip trailing arrow if present
	name = strings.TrimSuffix(strings.TrimSpace(name), " ›")
	name = strings.TrimSuffix(name, "›")
	name = strings.TrimSpace(name)

	prefixes := []string{
		"Stay near ",
		"Stay in ",
		"Popular homes in ",
		"Homes in ",
		"Places to stay in ",
		"Guests also checked out ",
		"Check out homes in ",
		"Available next month in ",
		"Unique stays in ",
		"Things to do in ",
		"Explore homes in ",
		"Top-rated homes in ",
		"Vacation rentals in ",
	}

	lower := strings.ToLower(name)
	for _, p := range prefixes {
		if strings.HasPrefix(lower, strings.ToLower(p)) {
			return strings.TrimSpace(name[len(p):])
		}
	}
	return name
}

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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