package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func cmdSecWatch(args []string) error {
	fs := newFlagSet("watch")
	dbPath := fs.String("db", defaultDBPath, "")
	cik := fs.String("cik", "", "")
	userAgent := fs.String("user-agent", "", "")
	limit := fs.Int("limit", 40, "")
	forms := fs.String("forms", defaultWatchForms, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *cik == "" {
		return fail(2, "--cik is required")
	}
	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return err
	}
	cik10 := normalizeCIK(*cik)
	body, err := httpGet(fmt.Sprintf("https://data.sec.gov/submissions/CIK%s.json", cik10), effectiveUserAgent(*userAgent))
	if err != nil {
		return err
	}
	var payload struct {
		Filings struct {
			Recent map[string][]any `json:"recent"`
		} `json:"filings"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	get := func(key string, i int) string {
		values := payload.Filings.Recent[key]
		if i >= len(values) || values[i] == nil {
			return ""
		}
		return fmt.Sprint(values[i])
	}
	allowedForms := parseFormSet(*forms)
	accessions := payload.Filings.Recent["accessionNumber"]
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	emitted := 0
	for i := 0; i < len(accessions) && emitted < *limit; i++ {
		accession := get("accessionNumber", i)
		form := get("form", i)
		if accession == "" || !formAllowed(allowedForms, form) {
			continue
		}
		primary := get("primaryDocument", i)
		sourceURL := archiveURL(cik10, accession, primary)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO filings(accession_number, cik, form, filing_date, accepted_at, primary_document, source_url, ingested_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, accession, cik10, form, get("filingDate", i), get("acceptanceDateTime", i), primary, sourceURL, utcNow()); err != nil {
			return err
		}
		event, err := appendEvent(tx, "filing_seen", map[string]any{"cik": cik10, "accession_number": accession, "form": form, "filing_date": get("filingDate", i), "accepted_at": get("acceptanceDateTime", i), "primary_document": primary, "source_url": sourceURL})
		if err != nil {
			return err
		}
		if err := jsonLine(event); err != nil {
			return err
		}
		emitted++
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "filing_seen emitted=%d\n", emitted)
	return nil
}

func cmdSecFetch(args []string) error {
	fs := newFlagSet("pull")
	dbPath := fs.String("db", defaultDBPath, "")
	rawRoot := fs.String("raw-root", "raw", "")
	userAgent := fs.String("user-agent", "", "")
	accessionFilter := fs.String("accession", "", "")
	cikFilter := fs.String("cik", "", "")
	forms := fs.String("forms", "", "")
	limit := fs.Int("limit", 20, "")
	sleepS := fs.Float64("sleep-s", 0.15, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return err
	}
	query := `SELECT accession_number, cik, primary_document FROM filings WHERE (raw_index_uri IS NULL OR (coalesce(primary_document, '') != '' AND raw_primary_uri IS NULL))`
	var qargs []any
	if *accessionFilter != "" {
		query = `SELECT accession_number, cik, primary_document FROM filings WHERE accession_number=?`
		qargs = append(qargs, *accessionFilter)
	} else if *cikFilter != "" {
		query += ` AND cik=?`
		qargs = append(qargs, normalizeCIK(*cikFilter))
	}
	if clause, formArgs := sqlFormFilterClause("form", *forms); clause != "" {
		query += clause
		qargs = append(qargs, formArgs...)
	}
	query += ` ORDER BY filing_date DESC LIMIT ?`
	qargs = append(qargs, *limit)
	rows, err := db.Query(query, qargs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	fetched := 0
	for rows.Next() {
		var accession, cik string
		var primary sql.NullString
		if err := rows.Scan(&accession, &cik, &primary); err != nil {
			return err
		}
		cik10 := normalizeCIK(cik)
		accNoDash := strings.ReplaceAll(accession, "-", "")
		dir := filepath.Join(*rawRoot, cik10, accNoDash)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		indexURL := archiveURL(cik10, accession, "")
		indexData, err := httpGet(indexURL, effectiveUserAgent(*userAgent))
		if err != nil {
			return err
		}
		indexPath := filepath.Join(dir, "index.json")
		if err := os.WriteFile(indexPath, indexData, 0o644); err != nil {
			return err
		}
		primaryPath := ""
		hashes := []string{sha256Hex(indexData)}
		names, err := packageArtifactNames(indexData, nullableSQLString(primary))
		if err != nil {
			return err
		}
		for _, name := range names {
			path, err := archiveLocalPath(dir, name)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			data, err := httpGet(archiveURL(cik10, accession, name), effectiveUserAgent(*userAgent))
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return err
			}
			hashes = append(hashes, sha256Hex(data))
			if primary.Valid && name == primary.String {
				primaryPath = path
			}
			if *sleepS > 0 {
				time.Sleep(time.Duration(*sleepS * float64(time.Second)))
			}
		}
		combined := sha256Hex([]byte(strings.Join(hashes, "\n")))
		if _, err := tx.Exec(`UPDATE filings SET raw_index_uri=?, raw_primary_uri=?, raw_sha256=?, ingested_at=? WHERE accession_number=?`, indexPath, primaryPath, combined, utcNow(), accession); err != nil {
			return err
		}
		event, err := appendEvent(tx, "filing_fetched", map[string]any{
			"accession_number": accession, "cik": cik10, "raw_index_uri": indexPath,
			"raw_primary_uri": primaryPath, "raw_sha256": combined, "package_file_count": len(names) + 1,
		})
		if err != nil {
			return err
		}
		if err := jsonLine(event); err != nil {
			return err
		}
		fetched++
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "filing_fetched emitted=%d\n", fetched)
	return nil
}

func parseFormSet(forms string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(forms, ",") {
		form := strings.ToUpper(strings.TrimSpace(part))
		if form == "" {
			continue
		}
		if form == "*" || form == "ALL" {
			out["*"] = true
			continue
		}
		out[form] = true
	}
	return out
}

func formAllowed(forms map[string]bool, form string) bool {
	if forms["*"] {
		return true
	}
	return forms[strings.ToUpper(strings.TrimSpace(form))]
}

func sqlFormFilterClause(column, forms string) (string, []any) {
	allowedForms := parseFormSet(forms)
	if len(allowedForms) == 0 || allowedForms["*"] {
		return "", nil
	}
	values := make([]string, 0, len(allowedForms))
	for form := range allowedForms {
		if form != "*" {
			values = append(values, form)
		}
	}
	sort.Strings(values)
	if len(values) == 0 {
		return "", nil
	}
	placeholders := make([]string, len(values))
	args := make([]any, 0, len(values))
	for i, form := range values {
		placeholders[i] = "?"
		args = append(args, form)
	}
	return " AND upper(" + column + ") IN (" + strings.Join(placeholders, ",") + ")", args
}

func effectiveUserAgent(userAgent string) string {
	if strings.TrimSpace(userAgent) != "" {
		return strings.TrimSpace(userAgent)
	}
	if env := strings.TrimSpace(os.Getenv("SEC_USER_AGENT")); env != "" {
		return env
	}
	return defaultUserAgent
}

func packageArtifactNames(indexData []byte, primary string) ([]string, error) {
	var idx struct {
		Directory struct {
			Item []struct {
				Name string `json:"name"`
			} `json:"item"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(indexData, &idx); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, item := range idx.Directory.Item {
		name := item.Name
		if !isPackageArtifactName(name, primary) {
			continue
		}
		seen[name] = true
	}
	var out []string
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func isPackageArtifactName(name, primary string) bool {
	if name == "" || filepath.IsAbs(name) || strings.Contains(filepath.Clean(name), "..") {
		return false
	}
	lower := strings.ToLower(name)
	if name == primary {
		return true
	}
	if strings.HasSuffix(lower, ".xml") || strings.HasSuffix(lower, ".xsd") {
		return true
	}
	if strings.HasSuffix(lower, ".htm") || strings.HasSuffix(lower, ".html") {
		return primary == ""
	}
	return false
}

func archiveLocalPath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(filepath.Clean(name), "..") {
		return "", fail(2, "unsafe SEC archive item name: %s", name)
	}
	return filepath.Join(root, filepath.Clean(name)), nil
}

func nullableSQLString(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func httpGet(url, userAgent string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fail(2, "GET %s failed: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}
func archiveURL(cik10, accession, doc string) string {
	cikInt, _ := strconv.Atoi(cik10)
	accNoDash := strings.ReplaceAll(accession, "-", "")
	base := fmt.Sprintf(secArchiveBaseURL, cikInt, accNoDash)
	if doc == "" {
		return base + "/index.json"
	}
	return base + "/" + doc
}

func sourceDocumentID(cik, url, hash string) string {
	return stableID("doc", map[string]any{"cik": cik, "url": url, "sha256": hash})
}
