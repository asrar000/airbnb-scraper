# 🏠 Airbnb Scraper (Go + Chromedp)

A production-style Airbnb scraping system built with **Go**, **Chromedp**, and **PostgreSQL** that collects listing data from Airbnb homepage sections and enriches them using detail pages.

This project demonstrates:

✅ Headless browser scraping  
✅ Worker pool concurrency  
✅ Retry & resilience system  
✅ Deduplication  
✅ Structured logging  
✅ Docker/Postgres integration  
✅ Clean scraper architecture  

---

## 🚀 Features

- Discover homepage sections automatically
- Extract listing **URL, price, rating, title**
- Visit detail pages to enrich:
  - Full title
  - Location
  - Description
- Concurrency-controlled scraping
- Automatic retry on failures
- URL deduplication
- Rate-limited scraping (anti-ban friendly)

---

## 🧠 Tech Stack

- **Go**
- **Chromedp (Chrome DevTools Protocol)**
- **Worker Pool (custom implementation)**
- **PostgreSQL**
- **Docker**

---

## 📁 Project Structure

```
airbnb-scraper/
│
├── airbnb/           # Scraper implementation
├── config/           # Configuration
├── models/           # Data models
├── utils/            # Worker pool, logger, retry, helpers
├── db/               # Database logic
├── main.go           # Entry point
├── go.mod
└── README.md
```

---

## ⚙️ Requirements

- Go **1.23+**
- Google Chrome / Chromium
- Docker (optional but recommended)

---

## 🔧 Installation

### 1. Clone

```bash
git clone <your-repo>
cd airbnb-scraper
```

### 2. Install dependencies

```bash
go mod tidy
```

---

## 🌐 Chrome Setup

Ensure Chrome exists:

```bash
which google-chrome-stable
```

If needed:

```bash
export CHROME_BIN=/usr/bin/google-chrome-stable
```

---

## 🐳 PostgreSQL (Docker)

```bash
docker run -d \
  --name airbnb_postgres \
  -p 5432:5432 \
  -e POSTGRES_USER=scraper \
  -e POSTGRES_PASSWORD=scraper123 \
  -e POSTGRES_DB=rental_db \
  postgres:16-alpine
```

---

## ▶️ Run Scraper

```bash
go run main.go
```

Example log:

```
[airbnb] Starting scrape — 10 listings per section
[airbnb] Loading homepage to discover sections…
[airbnb] Found 6 sections
```

---

## ⚙️ Configuration

Key config options:

| Option | Description |
|------|-------------|
| MaxConcurrency | Number of parallel detail page scrapes |
| RateLimitMs | Delay between sections |
| MaxRetries | Retry attempts |
| Pages | Pages to scrape |
| ListingsPerPage | Listings per section |

---

## 🧪 What This Project Demonstrates

This project is designed as a **portfolio-level scraping system** showcasing:

- Real production scraper architecture
- Browser automation reliability
- Concurrency patterns in Go
- Retry and resilience engineering
- Clean separation of concerns

Perfect for:

✅ Data engineering portfolio  
✅ Backend engineering portfolio  
✅ Systems engineering interviews  
✅ Scraping architecture demonstration  

---

## ⚠️ Notes

- Airbnb UI changes may break selectors
- Scraping should respect website policies
- Use proper rate limiting to avoid blocks

---

## 📌 Future Improvements

- Pagination support
- Proxy rotation
- CAPTCHA detection
- Kubernetes deployment
- Real-time streaming pipeline

---

## 👨‍💻 Author

**Asrar Ahmed**

If you found this useful, feel free to ⭐ the repo.
