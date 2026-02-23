#!/usr/bin/env bash
# ============================================================
#  Airbnb Scraper — Demo Runner
#  Usage: bash run_demo.sh
# ============================================================

set -euo pipefail

# ── Colours ─────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[1;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
RESET='\033[0m'

print_header() {
  echo ""
  echo -e "${BOLD}${BLUE}══════════════════════════════════════════════════════${RESET}"
  echo -e "${BOLD}${BLUE}  $1${RESET}"
  echo -e "${BOLD}${BLUE}══════════════════════════════════════════════════════${RESET}"
}

print_step() {
  echo -e "\n${CYAN}▶  $1${RESET}"
}

print_ok() {
  echo -e "${GREEN}✔  $1${RESET}"
}

print_err() {
  echo -e "${RED}✘  $1${RESET}"
}

# ── Locate project root (the directory containing this script) ─
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$SCRIPT_DIR"

print_header "Airbnb Scraper — Demo"
echo -e "  Project root : ${BOLD}$PROJECT_ROOT${RESET}"
echo -e "  Date         : $(date '+%Y-%m-%d %H:%M:%S')"

# ── 1. Check Go ──────────────────────────────────────────────
print_step "Checking Go installation"
if ! command -v go &>/dev/null; then
  print_err "Go is not installed or not in PATH."
  echo "  Install from https://go.dev/dl/"
  exit 1
fi
GO_VER=$(go version)
print_ok "Found: $GO_VER"

# ── 2. Check Chrome / Chromium ───────────────────────────────
print_step "Checking Chrome / Chromium"
CHROME_FOUND=""
for candidate in \
    "${CHROME_BIN:-}" \
    google-chrome-stable google-chrome chromium chromium-browser \
    /usr/bin/google-chrome-stable /usr/bin/google-chrome \
    /usr/bin/chromium-browser /usr/bin/chromium \
    /snap/bin/chromium /opt/google/chrome/google-chrome; do
  [ -z "$candidate" ] && continue
  if command -v "$candidate" &>/dev/null || [ -x "$candidate" ]; then
    CHROME_FOUND="$candidate"
    break
  fi
done

if [ -z "$CHROME_FOUND" ]; then
  print_err "Chrome / Chromium not found."
  echo "  Install with:  sudo apt-get install -y chromium-browser"
  echo "  Or set CHROME_BIN=/path/to/chrome before running this script."
  exit 1
fi
print_ok "Found browser: $CHROME_FOUND"

# ── 3. Verify go.mod present ─────────────────────────────────
print_step "Verifying go.mod"
if [ ! -f "$PROJECT_ROOT/go.mod" ]; then
  print_err "go.mod not found in $PROJECT_ROOT"
  echo "  Run this script from the project root directory."
  exit 1
fi
MODULE=$(grep '^module' "$PROJECT_ROOT/go.mod" | awk '{print $2}')
print_ok "Module: $MODULE"

# ── 4. Download dependencies ─────────────────────────────────
print_step "Downloading Go dependencies (go mod tidy)"
cd "$PROJECT_ROOT"
go mod tidy 2>&1 | sed 's/^/  /'
print_ok "Dependencies ready"

# ── 5. Build ─────────────────────────────────────────────────
print_step "Building project"
BUILD_OUTPUT="$PROJECT_ROOT/airbnb-scraper-demo"
if go build -o "$BUILD_OUTPUT" . 2>&1 | sed 's/^/  /'; then
  print_ok "Build successful → $BUILD_OUTPUT"
else
  print_err "Build failed. Fix compilation errors above and re-run."
  exit 1
fi

# ── 6. Database check (optional) ────────────────────────────
print_step "Checking PostgreSQL connection (optional)"
PGHOST="${DB_HOST:-localhost}"
PGPORT="${DB_PORT:-5432}"
PGUSER="${DB_USER:-postgres}"
PGPASS="${DB_PASSWORD:-}"
PGDB="${DB_NAME:-airbnb}"

PG_OK=false
if command -v psql &>/dev/null; then
  if PGPASSWORD="$PGPASS" psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDB" -c '\q' &>/dev/null 2>&1; then
    print_ok "PostgreSQL reachable at $PGHOST:$PGPORT/$PGDB"
    PG_OK=true
  else
    echo -e "  ${YELLOW}⚠  PostgreSQL not reachable — scraper will still run and write CSV${RESET}"
  fi
else
  echo -e "  ${YELLOW}⚠  psql not in PATH — skipping DB check (CSV output will still work)${RESET}"
fi

# ── 7. Prepare output directory ──────────────────────────────
print_step "Preparing output directory"
OUTPUT_DIR="$PROJECT_ROOT/output"
mkdir -p "$OUTPUT_DIR"
print_ok "Output directory: $OUTPUT_DIR"

# ── 8. Run the scraper ───────────────────────────────────────
print_header "Running Airbnb Scraper"
echo ""
echo -e "  ${YELLOW}This will open a headless Chrome browser and scrape live Airbnb data."
echo -e "  Expect it to take a few minutes depending on network speed.${RESET}"
echo ""

SCRAPER_EXIT=0
"$BUILD_OUTPUT" 2>&1 | tee /tmp/airbnb_scraper_run.log || SCRAPER_EXIT=$?

# ── 9. Results summary ───────────────────────────────────────
print_header "Results"

CSV_FILE="$OUTPUT_DIR/raw_listings.csv"
if [ -f "$CSV_FILE" ]; then
  ROW_COUNT=$(( $(wc -l < "$CSV_FILE") - 1 ))   # subtract header
  print_ok "CSV written: $CSV_FILE  ($ROW_COUNT listings)"
  echo ""
  echo -e "  ${BOLD}Preview (first 5 rows):${RESET}"
  echo ""
  # Pretty-print first 5 data rows using column if available
  if command -v column &>/dev/null; then
    head -6 "$CSV_FILE" | column -t -s ',' | head -6 | sed 's/^/    /'
  else
    head -6 "$CSV_FILE" | sed 's/^/    /'
  fi
else
  echo -e "  ${YELLOW}⚠  CSV not found at $CSV_FILE${RESET}"
  echo "     The scraper may have exited before writing. Check logs above."
fi

# Print insights if binary produces them separately
INSIGHTS_BINARY="$PROJECT_ROOT/insights"
if [ -x "$INSIGHTS_BINARY" ] && [ -f "$CSV_FILE" ]; then
  echo ""
  print_step "Generating insights"
  "$INSIGHTS_BINARY" 2>&1 | sed 's/^/  /'
fi

echo ""
if [ $SCRAPER_EXIT -eq 0 ]; then
  print_ok "Scraper finished successfully."
else
  print_err "Scraper exited with code $SCRAPER_EXIT — see logs above."
fi

echo ""
echo -e "  ${BOLD}Full run log saved to:${RESET} /tmp/airbnb_scraper_run.log"
echo ""
