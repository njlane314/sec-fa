package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func cmdSecWatch(args []string) error {
	fs := newFlagSet("watch")
	dbPath := fs.String("db", "", "")
	cik := fs.String("cik", "", "")
	userAgent := fs.String("user-agent", defaultUserAgent, "")
	limit := fs.Int("limit", 40, "")
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
	cik10 := normalizeCIK(*cik)
	body, err := httpGet(fmt.Sprintf("https://data.sec.gov/submissions/CIK%s.json", cik10), *userAgent)
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
		if accession == "" || (form != "10-K" && form != "10-Q" && form != "10-K/A" && form != "10-Q/A") {
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
	dbPath := fs.String("db", "", "")
	rawRoot := fs.String("raw-root", "raw", "")
	userAgent := fs.String("user-agent", defaultUserAgent, "")
	accessionFilter := fs.String("accession", "", "")
	limit := fs.Int("limit", 20, "")
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
	query := `SELECT accession_number, cik, primary_document FROM filings WHERE raw_index_uri IS NULL OR raw_primary_uri IS NULL`
	var qargs []any
	if *accessionFilter != "" {
		query = `SELECT accession_number, cik, primary_document FROM filings WHERE accession_number=?`
		qargs = append(qargs, *accessionFilter)
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
		indexData, err := httpGet(indexURL, *userAgent)
		if err != nil {
			return err
		}
		indexPath := filepath.Join(dir, "index.json")
		if err := os.WriteFile(indexPath, indexData, 0o644); err != nil {
			return err
		}
		primaryPath := ""
		combined := sha256Hex(indexData)
		if primary.Valid && primary.String != "" {
			primaryURL := archiveURL(cik10, accession, primary.String)
			primaryData, err := httpGet(primaryURL, *userAgent)
			if err != nil {
				return err
			}
			primaryPath = filepath.Join(dir, primary.String)
			if err := os.WriteFile(primaryPath, primaryData, 0o644); err != nil {
				return err
			}
			combined = sha256Hex(append(indexData, primaryData...))
		}
		if _, err := tx.Exec(`UPDATE filings SET raw_index_uri=?, raw_primary_uri=?, raw_sha256=?, ingested_at=? WHERE accession_number=?`, indexPath, primaryPath, combined, utcNow(), accession); err != nil {
			return err
		}
		event, err := appendEvent(tx, "filing_fetched", map[string]any{"accession_number": accession, "cik": cik10, "raw_index_uri": indexPath, "raw_primary_uri": primaryPath, "raw_sha256": combined})
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
