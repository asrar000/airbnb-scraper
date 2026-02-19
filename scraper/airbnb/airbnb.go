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
	startURL           = "https://www.airbnb.com/"
	platform           = "airbnb"
	listingsPerSection = 10
)

// cardInfo holds data scraped directly from a homepage listing card.
type cardInfo struct {
	URL    string `json:"url"`
	Title  string `json:"title"`
	Price  string `json:"price"`  // non-strikethrough price e.g. "$125 for 2 nights"
	Rating string `json:"rating"` // e.g. "4.88"
}

// section represents a named homepage section with full card data.
type section struct {
	Name  string
	Cards []cardInfo
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

// Scrape is the main entry point:
//  1. Opens airbnb.com, discovers all named sections
//  2. Reads price + rating directly from cards on homepage (no detail page needed for those)
//  3. Visits detail page only for title, location, description
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

	silentCtx, cancelSilent := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(string, ...interface{}) {}),
		chromedp.WithErrorf(func(string, ...interface{}) {}),
		chromedp.WithDebugf(func(string, ...interface{}) {}),
	)
	defer cancelSilent()
	allocCtx = silentCtx

	// ── Step 1: discover sections + card data ─────────────────────────────
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
		s.logger.Info("[airbnb]   Section %d: %q (%d cards)", i+1, sec.Name, len(sec.Cards))
	}

	// ── Step 2: process each section ──────────────────────────────────────
	totalSections := len(sections)
	for secIdx, sec := range sections {
		secNum := secIdx + 1
		s.printSectionBanner(secNum, totalSections, sec.Name, len(sec.Cards))

		if len(sec.Cards) == 0 {
			s.logger.Warn("[airbnb] Section %q has no cards — skipping", sec.Name)
			continue
		}

		cards := sec.Cards
		if len(cards) > listingsPerSection {
			cards = cards[:listingsPerSection]
		}

		sectionLocation := extractLocationFromSection(sec.Name)

		// Build RawListings directly from card data — price + rating already extracted
		var sectionListings []*models.RawListing
		for _, card := range cards {
			if !s.visitedURL.Add(card.URL) {
				s.logger.Debug("[airbnb] Duplicate URL skipped: %s", card.URL)
				continue
			}
			sectionListings = append(sectionListings, &models.RawListing{
				URL:       card.URL,
				Title:     card.Title,
				RawPrice:  card.Price,
				Rating:    card.Rating,
				Location:  sectionLocation,
				ScrapedAt: time.Now(),
				Platform:  platform,
			})
		}

		if len(sectionListings) == 0 {
			s.logger.Warn("[airbnb] Section %q yielded 0 new listings after dedup", sec.Name)
			s.printSectionDone(sec.Name)
			continue
		}

		// ── Step 3: visit detail pages for title, location, description only
		s.logger.Info("[airbnb]   Enriching %d listings (title/location/desc from detail pages)…", len(sectionListings))
		s.enrichListings(allocCtx, sectionListings)

		for i, l := range sectionListings {
			pricePreview := l.RawPrice
			if len(pricePreview) > 30 {
				pricePreview = pricePreview[:30]
			}
			s.logger.Info("[airbnb]   ✓ [%d/%d] %s | %s | price=%s | rating=%s",
				i+1, len(sectionListings),
				truncateStr(l.Title, 35),
				l.Location,
				pricePreview,
				l.Rating,
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

// ── Section + card discovery ─────────────────────────────────────────────────

func (s *Scraper) discoverSections(allocCtx context.Context) ([]section, error) {
	var sections []section

	err := s.retry.Do("discover-sections", func() error {
		ctx, cancel := chromedp.NewContext(allocCtx)
		defer cancel()
		ctx, cancelTimeout := context.WithTimeout(ctx, 90*time.Second)
		defer cancelTimeout()

		type jsSection struct {
			Name  string     `json:"name"`
			Cards []cardInfo `json:"cards"`
		}
		var jsSections []jsSection

		err := chromedp.Run(ctx,
			chromedp.Navigate(startURL),
			chromedp.Sleep(6*time.Second),
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

					// ── Extract price + rating from a single card anchor element ──
					function extractCard(a) {
						var url = a.href.split('?')[0];
						if (!url || globalSeen[url]) return null;

						// Walk up to find the card container
						var card = a;
						for (var up = 0; up < 8; up++) {
							if (!card.parentElement) break;
							card = card.parentElement;
							// Stop when we have a sizeable container with the listing info
							if (card.querySelectorAll('a[href*="/rooms/"]').length === 1 &&
							    card.innerText && card.innerText.length > 30) break;
						}

						var title  = '';
						var price  = '';
						var rating = '';

						// ── Rating ──
						// Appears as "4.88" or "★ 4.88 (3215)" or aria-label="Rated 4.88 out of 5"
						var ratingEl = card.querySelector('[aria-label*="out of 5"]') ||
						               card.querySelector('[aria-label*="Rated"]');
						if (ratingEl) {
							var rt = ratingEl.getAttribute('aria-label') || ratingEl.innerText || '';
							var rm = rt.match(/([1-5]\.\d{1,2})/);
							if (rm) rating = rm[1];
						}
						if (!rating) {
							// Scan text lines for standalone "4.xx" or "4.xx (NNN)"
							var lines = (card.innerText || '').split('\n');
							for (var li = 0; li < lines.length; li++) {
								var l = lines[li].trim();
								var rm2 = l.match(/^([1-5]\.\d{2})(?:\s*\(|$)/);
								if (rm2) { rating = rm2[1]; break; }
							}
						}

						// ── Price ──
						// Card shows: [strikethrough $142] $125 for 2 nights
						// We want the NON-strikethrough price.
						// Strategy: walk all child elements, collect $ amounts NOT inside <s>/<del>
						// and NOT having computed text-decoration:line-through
						var nights = 0;
						var cardText = card.innerText || '';
						var nm = cardText.match(/for\s+(\d+)\s*nights?/i);
						if (nm) nights = parseInt(nm[1]);

						var nonStruckAmounts = [];
						var allEls = card.querySelectorAll('*');
						for (var ei = 0; ei < allEls.length; ei++) {
							var el = allEls[ei];
							// Only consider leaf text nodes with a dollar sign
							if (el.children.length > 0) continue;
							var txt = (el.innerText || '').trim();
							if (!txt.match(/^\$\d/)) continue;

							// Check this element and up to 4 ancestors for strikethrough
							var struck = false;
							var check = el;
							for (var d = 0; d < 5; d++) {
								if (!check) break;
								var tag = (check.tagName || '').toLowerCase();
								if (tag === 's' || tag === 'del') { struck = true; break; }
								try {
									var cs = window.getComputedStyle(check);
									if (cs && cs.textDecorationLine &&
									    cs.textDecorationLine.includes('line-through')) {
										struck = true; break;
									}
								} catch(e) {}
								check = check.parentElement;
							}

							if (!struck) {
								var val = parseFloat(txt.replace(/[$,]/g, ''));
								if (val > 0 && val < 50000) nonStruckAmounts.push(val);
							}
						}

						if (nonStruckAmounts.length > 0) {
							// Take the smallest non-struck amount = current nightly/stay price
							var currentPrice = nonStruckAmounts.reduce(function(a, b) { return a < b ? a : b; });
							if (nights > 1) {
								price = '$' + currentPrice + ' for ' + nights + ' nights';
							} else if (nights === 1) {
								price = '$' + currentPrice + ' per night';
							} else {
								price = '$' + currentPrice;
							}
						}

						// ── Title ──
						var titleEl = card.querySelector('[data-testid="listing-card-title"]') ||
						              card.querySelector('span[class*="t1jojoys"]') ||
						              card.querySelector('div[id*="title"]');
						if (titleEl) {
							title = titleEl.innerText.trim();
						} else {
							// Fallback: first bold/strong text in card
							var boldEl = card.querySelector('strong, b, span[class*="title"]');
							if (boldEl) title = boldEl.innerText.trim();
						}

						globalSeen[url] = true;
						return { url: url, title: title, price: price, rating: rating };
					}

					function addSection(name, cards) {
						if (!name || cards.length === 0) return;
						name = name.trim().replace(/\s+/g, ' ');
						if (name.length < 4 || name.length > 120) return;
						if (/inspiration|airbnb your home|become a host|support|career|investor|privacy|cookie|news|blog|press|gift/i.test(name)) return;
						if (results.some(function(r) { return r.name === name; })) return;
						results.push({ name: name, cards: cards });
					}

					function collectCards(container) {
						var seen = {};
						var cards = [];
						container.querySelectorAll('a[href*="/rooms/"]').forEach(function(a) {
							var url = a.href.split('?')[0];
							if (seen[url]) return;
							seen[url] = true;
							var c = extractCard(a);
							if (c) cards.push(c);
						});
						return cards;
					}

					// Strategy 1: data-section-id containers
					document.querySelectorAll('[data-section-id]').forEach(function(el) {
						var cards = collectCards(el);
						if (cards.length === 0) return;
						var heading = el.querySelector('h2, h3, [role="heading"]');
						var name = heading ? (heading.innerText || '').trim() : '';
						if (!name) name = el.getAttribute('data-section-id') || '';
						addSection(name, cards);
					});

					// Strategy 2: walk headings upward
					if (results.length === 0) {
						document.querySelectorAll('h2, h3').forEach(function(h) {
							var name = (h.innerText || '').trim();
							if (!name) return;
							var el = h;
							for (var d = 0; d < 6; d++) {
								el = el.parentElement;
								if (!el) break;
								if (el.querySelectorAll('a[href*="/rooms/"]').length >= 2) {
									addSection(name, collectCards(el));
									break;
								}
							}
						});
					}

					// Strategy 3: bucket by nearest heading
					if (results.length === 0) {
						var buckets = {};
						document.querySelectorAll('a[href*="/rooms/"]').forEach(function(a) {
							var url = a.href.split('?')[0];
							if (!url || globalSeen[url]) return;
							var el = a;
							var label = 'Other';
							for (var d = 0; d < 10; d++) {
								el = el.parentElement;
								if (!el) break;
								var h = el.querySelector('h2, h3');
								if (h) { label = (h.innerText || '').trim() || label; break; }
							}
							if (!buckets[label]) buckets[label] = [];
							var c = extractCard(a);
							if (c) buckets[label].push(c);
						});
						Object.keys(buckets).forEach(function(k) { addSection(k, buckets[k]); });
					}

					return results;
				})()
			`, &jsSections),
		)

		if err != nil {
			return fmt.Errorf("chromedp discover sections: %w", err)
		}

		if len(jsSections) == 0 {
			var debugInfo string
			_ = chromedp.Run(ctx, chromedp.Evaluate(`
				(function() {
					var h = Array.from(document.querySelectorAll('h2,h3')).slice(0,10).map(function(e){ return e.innerText.trim(); }).join(' | ');
					var links = document.querySelectorAll('a[href*="/rooms/"]').length;
					var secs = document.querySelectorAll('[data-section-id]').length;
					return 'headings: ' + h + ' | room_links: ' + links + ' | data-section-id: ' + secs;
				})()
			`, &debugInfo))
			s.logger.Warn("[airbnb] Section discovery debug: %s", debugInfo)
		}

		for _, js := range jsSections {
			sections = append(sections, section{
				Name:  strings.TrimSpace(js.Name),
				Cards: js.Cards,
			})
		}
		return nil
	})

	return sections, err
}

// ── Detail page enrichment (title, location, description only) ───────────────

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
			// Title — detail page has full title
			if enriched.Title != "" && enriched.Title != "Property" {
				l.Title = enriched.Title
			}
			// Location — only overwrite card's section location if detail page has a better one
			if isGoodLocation(enriched.Location) && !isGoodLocation(l.Location) {
				l.Location = enriched.Location
			}
			// Rating — if card didn't capture it, use detail page fallback
			if l.Rating == "" && enriched.Rating != "" {
				l.Rating = enriched.Rating
			}
			// Price — NEVER overwrite, already set from card
			l.Description = enriched.Description
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

		type pageData struct {
			Title    string `json:"title"`
			Location string `json:"location"`
			Rating   string `json:"rating"`
			Desc     string `json:"desc"`
		}
		var data pageData

		err := chromedp.Run(ctx,
			chromedp.Navigate(url),
			chromedp.Sleep(4*time.Second),
			chromedp.Evaluate(`window.scrollTo(0, 0)`, nil),
			chromedp.Sleep(500*time.Millisecond),

			chromedp.Evaluate(`
				(function() {
					var result = { title: '', location: '', rating: '', desc: '' };

					// ── Title ──────────────────────────────────────────────────────
					// Real title is in h1[elementtiming="LCP-target"] above the photo grid.
					// It may differ from the card title (e.g. long descriptive names).
					var h1 = document.querySelector('h1[elementtiming="LCP-target"]') ||
					          document.querySelector('h1');
					if (h1) result.title = h1.innerText.trim();

					// ── Rating ─────────────────────────────────────────────────────
					// Strategy 1: the reviews anchor banner below the photo grid has
					// data-testid="pdp-reviews-highlight-banner-host-rating" and inside it
					// a span with aria-label="Rated X.X out of 5 stars."
					var reviewBanner = document.querySelector('[data-testid="pdp-reviews-highlight-banner-host-rating"]');
					if (reviewBanner) {
						var ratedSpan = reviewBanner.querySelector('[aria-label*="out of 5"]') ||
						                reviewBanner.querySelector('[aria-label*="Rated"]');
						if (ratedSpan) {
							var rl = ratedSpan.getAttribute('aria-label') || '';
							var rm0 = rl.match(/([1-5]\.[0-9]{1,2})/);
							if (rm0) result.rating = rm0[1];
						}
						// Also try the plain text number sibling div (aria-hidden="true">5.0</div>)
						if (!result.rating) {
							var numDiv = reviewBanner.querySelector('div[aria-hidden="true"]');
							if (numDiv) {
								var nd = numDiv.innerText.trim();
								if (/^[1-5]\.[0-9]/.test(nd)) result.rating = nd;
							}
						}
					}

					// Strategy 2: any [aria-label*="Rated X out of 5"] anywhere on page
					if (!result.rating) {
						var rEl = document.querySelector('[aria-label*="out of 5"]') ||
						          document.querySelector('[aria-label*="Rated"]');
						if (rEl) {
							var rt = rEl.getAttribute('aria-label') || rEl.innerText || '';
							var rm2 = rt.match(/([1-5]\.[0-9]{1,2})/);
							if (rm2) result.rating = rm2[1];
						}
					}

					// Strategy 3: scan body lines for "★ 4.8" or "4.8 · N reviews"
					if (!result.rating) {
						var bodyLines = document.body.innerText.split('\n');
						for (var li = 0; li < bodyLines.length; li++) {
							var line = bodyLines[li].trim();
							var rm3 = line.match(/★\s*([1-5]\.[0-9]{1,2})/);
							if (!rm3) rm3 = line.match(/^([1-5]\.[0-9]{1,2})\s*·/);
							if (rm3) { result.rating = rm3[1]; break; }
						}
					}

					// ── Location ───────────────────────────────────────────────────
					var h2s = document.querySelectorAll('h2');
					for (var i = 0; i < h2s.length; i++) {
						var txt = h2s[i].innerText.trim();
						var m = txt.match(/\bin\s+([A-Z][^,\n]{2,50}(?:,\s*[A-Z][^\n]{2,40})?)/);
						if (m && m[1] && m[1].length < 80) {
							result.location = m[1].trim();
							break;
						}
					}
					if (!result.location) {
						var bt = document.body.innerText;
						var nm = bt.match(/\d+\s*nights?\s+in\s+([^\n$\d]{3,60})/i);
						if (nm) result.location = nm[1].trim();
					}

					// ── Description ────────────────────────────────────────────────
					// Primary: [data-section-id="DESCRIPTION_DEFAULT"] — works for most listings.
					var descEl = document.querySelector('[data-section-id="DESCRIPTION_DEFAULT"]');
					if (descEl) {
						var dt = descEl.innerText
							.replace(/Some info has been automatically translated\.?\s*(Show original)?/gi, '')
							.replace(/Show more/gi, '')
							.trim();
						if (dt.length > 30) result.desc = dt.substring(0, 1000);
					}

					// Fallback 1: <main> paragraphs
					if (!result.desc || result.desc.length < 30) {
						var paras = document.querySelectorAll('main p');
						var parts = [];
						for (var j = 0; j < paras.length && parts.join(' ').length < 800; j++) {
							var pt = paras[j].innerText.trim();
							if (pt.length > 20) parts.push(pt);
						}
						if (parts.length) result.desc = parts.join(' ').substring(0, 1000);
					}

					// Fallback 2: only when still empty — find "Show more" button via
					// data-button-content="true" span, grab text BEFORE it in its container.
					// This catches listings where description is not in the standard section.
					if (!result.desc || result.desc.length < 30) {
						var showMoreBtn = null;
						var btns = document.querySelectorAll('button');
						for (var bi = 0; bi < btns.length; bi++) {
							var span = btns[bi].querySelector('[data-button-content="true"]');
							if (span && span.innerText.trim().toLowerCase() === 'show more') {
								showMoreBtn = btns[bi]; break;
							}
							if (!showMoreBtn && btns[bi].innerText.trim().toLowerCase() === 'show more') {
								showMoreBtn = btns[bi];
							}
						}
						if (showMoreBtn) {
							var container = showMoreBtn.parentElement;
							for (var up = 0; up < 6; up++) {
								if (!container) break;
								var cText = (container.innerText || '').trim();
								if (cText.length > 80 && cText.replace(/show more/gi, '').trim().length > 40) break;
								container = container.parentElement;
							}
							if (container) {
								var descParts = [];
								var walker = document.createTreeWalker(
									container, NodeFilter.SHOW_TEXT, null, false
								);
								var node;
								while ((node = walker.nextNode())) {
									if (showMoreBtn.contains(node)) break;
									var t = node.nodeValue.trim();
									if (t.length > 0) descParts.push(t);
								}
								var raw = descParts.join(' ').trim();
								raw = raw.replace(/Some info has been automatically translated\.?\s*(Show original)?/gi, '').trim();
								if (raw.length > 30) result.desc = raw.substring(0, 1000);
							}
						}
					}

					if (!result.desc) result.desc = 'Description not available';

					return result;
				})()
			`, &data),
		)
		if err != nil {
			return fmt.Errorf("detail page: %w", err)
		}

		listing.Title = data.Title
		listing.Location = data.Location
		listing.Rating = data.Rating
		listing.Description = data.Desc
		return nil
	})

	return listing, err
}

// ── Helpers ───────────────────────────────────────────────────────────────────

var junkLocationPhrases = []string{
	"where you'll be", "available next month", "add dates",
	"check out homes", "things to do", "inspiration",
}

func isGoodLocation(loc string) bool {
	loc = strings.TrimSpace(loc)
	if loc == "" || loc == "N/A" || loc == "Unknown" {
		return false
	}
	if strings.Contains(loc, "\n") || len(loc) >= 80 {
		return false
	}
	lower := strings.ToLower(loc)
	for _, j := range junkLocationPhrases {
		if strings.Contains(lower, j) {
			return false
		}
	}
	return true
}

func extractLocationFromSection(name string) string {
	name = strings.TrimSuffix(strings.TrimSpace(name), " ›")
	name = strings.TrimSuffix(name, "›")
	name = strings.TrimSpace(name)

	prefixes := []string{
		"Stay near ", "Stay in ", "Popular homes in ", "Homes in ",
		"Places to stay in ", "Guests also checked out ", "Check out homes in ",
		"Available next month in ", "Unique stays in ", "Things to do in ",
		"Explore homes in ", "Top-rated homes in ", "Vacation rentals in ",
	}
	lower := strings.ToLower(name)
	for _, p := range prefixes {
		if strings.HasPrefix(lower, strings.ToLower(p)) {
			return strings.TrimSpace(name[len(p):])
		}
	}
	return name
}

func (s *Scraper) printSectionBanner(current, total int, name string, cardCount int) {
	sep := strings.Repeat("─", 55)
	fmt.Printf("\n\033[1;34m%s\033[0m\n", sep)
	fmt.Printf("\033[1;34m  📍 Section [%d/%d]: %s\033[0m\n", current, total, name)
	fmt.Printf("\033[1;34m     Found %d cards — scraping up to %d\033[0m\n", cardCount, listingsPerSection)
	fmt.Printf("\033[1;34m%s\033[0m\n", sep)
}

func (s *Scraper) printSectionDone(name string) {
	fmt.Printf("\n\033[1;32m  ✅ Section done: %q — moving to next\033[0m\n\n", name)
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
		"/usr/bin/google-chrome-stable", "/usr/bin/google-chrome",
		"/usr/bin/chromium-browser", "/usr/bin/chromium",
		"/snap/bin/chromium", "/opt/google/chrome/google-chrome",
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}