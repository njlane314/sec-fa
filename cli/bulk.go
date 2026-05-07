package main

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type securitySeed struct {
	Row        int
	CIK        string
	Symbol     string
	PriceUSD   float64
	ADVUSD     float64
	Investable bool
}

type securitySeedBatch struct {
	Seeds            []securitySeed
	InputRows        int
	DuplicateCIKRows int
}

type filingIngestGroup struct {
	Name  string
	Forms string
	Limit int
}

func cmdImportSecurities(args []string) error {
	fs := newFlagSet("import-securities")
	dbPath := fs.String("db", defaultDBPath, "")
	csvPath := fs.String("csv", "", "")
	defaultInvestable := fs.Int("default-investable", 1, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	_, event, err := importSecuritySeedFile(*dbPath, *csvPath, *defaultInvestable != 0)
	if err != nil {
		return err
	}
	return jsonLine(event)
}

func cmdIngestUniverse(args []string) error {
	fs := newFlagSet("ingest-universe")
	dbPath := fs.String("db", defaultDBPath, "")
	csvPath := fs.String("csv", "", "")
	rawRoot := fs.String("raw-root", "raw", "")
	forms := fs.String("forms", defaultFundamentalForms, "")
	userAgent := fs.String("user-agent", "", "")
	defaultInvestable := fs.Int("default-investable", 1, "")
	limitPerCIK := fs.Int("limit-per-cik", 1, "")
	annualLimitPerCIK := fs.Int("annual-limit-per-cik", 1, "")
	quarterlyLimitPerCIK := fs.Int("quarterly-limit-per-cik", 4, "")
	sleepS := fs.Float64("sleep-s", 0.2, "")
	pullSleepS := fs.Float64("pull-sleep-s", 0.15, "")
	continueOnError := fs.Bool("continue-on-error", false, "")
	universeName := fs.String("universe-name", "", "")
	minADV := fs.Float64("min-adv-usd", 0, "")
	minFacts := fs.Int("min-fact-count", 2, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if strings.TrimSpace(*forms) == "" {
		return fail(2, "--forms must not be empty")
	}
	if *limitPerCIK < 0 || *annualLimitPerCIK < 0 || *quarterlyLimitPerCIK < 0 {
		return fail(2, "filing limits must be non-negative")
	}
	if *sleepS < 0 || *pullSleepS < 0 {
		return fail(2, "sleep intervals must be non-negative")
	}
	ingestGroups := filingIngestGroups(*forms, *annualLimitPerCIK, *quarterlyLimitPerCIK, *limitPerCIK)
	if len(ingestGroups) == 0 {
		return fail(2, "at least one filing limit must be positive for requested forms")
	}

	batch, importEvent, err := importSecuritySeedFile(*dbPath, *csvPath, *defaultInvestable != 0)
	if err != nil {
		return err
	}
	if err := jsonLine(importEvent); err != nil {
		return err
	}

	var batchErrors []string
	watched := 0
	fetched := 0
	parsed := 0
	watchPasses := 0
	pullPasses := 0
	for i, seed := range batch.Seeds {
		issuerOK := true
		issuerFetched := true
		parsedAccessions := map[string]bool{}
		for _, group := range ingestGroups {
			if !issuerOK {
				break
			}
			if err := cmdSecWatch(secWatchArgs(*dbPath, seed.CIK, group.Forms, group.Limit, *userAgent)); err != nil {
				issuerOK = false
				issuerFetched = false
				if err := recordIssuerBatchError(&batchErrors, *continueOnError, "watch "+group.Name, seed, err); err != nil {
					return err
				}
			} else {
				watchPasses++
			}
			if !issuerOK {
				break
			}
			if err := cmdSecFetch(secPullArgs(*dbPath, *rawRoot, seed.CIK, group.Forms, group.Limit, *pullSleepS, *userAgent)); err != nil {
				issuerOK = false
				issuerFetched = false
				if err := recordIssuerBatchError(&batchErrors, *continueOnError, "pull "+group.Name, seed, err); err != nil {
					return err
				}
			} else {
				pullPasses++
			}
			if !issuerOK {
				break
			}
			groupParsed, err := parseFetchedFilingsForGroup(*dbPath, *rawRoot, seed, group, parsedAccessions, *continueOnError, &batchErrors)
			if err != nil {
				return err
			}
			parsed += groupParsed
		}
		if issuerOK {
			watched++
		}
		if issuerFetched {
			fetched++
		}
		if *sleepS > 0 && i+1 < len(batch.Seeds) {
			time.Sleep(time.Duration(*sleepS * float64(time.Second)))
		}
	}

	builtUniverse := false
	if strings.TrimSpace(*universeName) != "" {
		if err := cmdUniverseBuild(secUniverseArgs(*dbPath, *universeName, *minADV, *minFacts)); err != nil {
			if *continueOnError {
				msg := fmt.Sprintf("universe %s: %v", *universeName, err)
				batchErrors = append(batchErrors, msg)
				fmt.Fprintf(os.Stderr, "batch_error %s\n", msg)
			} else {
				return fail(1, "universe %s: %v", *universeName, err)
			}
		} else {
			builtUniverse = true
		}
	}

	status := "completed"
	if len(batchErrors) > 0 {
		status = "completed_with_errors"
	}
	event, err := appendBatchEvent(*dbPath, "universe_ingest_completed", map[string]any{
		"status":                       status,
		"csv":                          *csvPath,
		"raw_root":                     *rawRoot,
		"forms":                        *forms,
		"ingest_groups":                ingestGroupPayloads(ingestGroups),
		"input_rows":                   batch.InputRows,
		"security_count":               len(batch.Seeds),
		"duplicate_cik_rows_collapsed": batch.DuplicateCIKRows,
		"limit_per_cik":                *limitPerCIK,
		"annual_limit_per_cik":         *annualLimitPerCIK,
		"quarterly_limit_per_cik":      *quarterlyLimitPerCIK,
		"watched":                      watched,
		"pulled":                       fetched,
		"watch_passes":                 watchPasses,
		"pull_passes":                  pullPasses,
		"parsed":                       parsed,
		"universe_name":                nullableEmptyString(*universeName),
		"universe_built":               builtUniverse,
		"errors":                       batchErrors,
	})
	if err != nil {
		return err
	}
	return jsonLine(event)
}

func filingIngestGroups(forms string, annualLimit, quarterlyLimit, fallbackLimit int) []filingIngestGroup {
	allowed := parseFormSet(forms)
	if len(allowed) == 0 {
		return nil
	}
	if allowed["*"] {
		limit := fallbackLimit
		if limit == 0 {
			limit = annualLimit + quarterlyLimit
		}
		if limit <= 0 {
			return nil
		}
		return []filingIngestGroup{{Name: "all", Forms: "*", Limit: limit}}
	}

	annualForms := selectedFormsCSV(allowed, []string{"10-K", "10-K/A"})
	quarterlyForms := selectedFormsCSV(allowed, []string{"10-Q", "10-Q/A"})
	covered := parseFormSet(strings.Join([]string{annualForms, quarterlyForms}, ","))
	otherForms := make([]string, 0)
	for form := range allowed {
		if !covered[form] {
			otherForms = append(otherForms, form)
		}
	}
	sort.Strings(otherForms)

	groups := make([]filingIngestGroup, 0, 3)
	if annualForms != "" && annualLimit > 0 {
		groups = append(groups, filingIngestGroup{Name: "annual", Forms: annualForms, Limit: annualLimit})
	}
	if quarterlyForms != "" && quarterlyLimit > 0 {
		groups = append(groups, filingIngestGroup{Name: "quarterly", Forms: quarterlyForms, Limit: quarterlyLimit})
	}
	if len(otherForms) > 0 && fallbackLimit > 0 {
		groups = append(groups, filingIngestGroup{Name: "other", Forms: strings.Join(otherForms, ","), Limit: fallbackLimit})
	}
	return groups
}

func selectedFormsCSV(allowed map[string]bool, candidates []string) string {
	out := make([]string, 0, len(candidates))
	for _, form := range candidates {
		if allowed[form] {
			out = append(out, form)
		}
	}
	return strings.Join(out, ",")
}

func ingestGroupPayloads(groups []filingIngestGroup) []map[string]any {
	out := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		out = append(out, map[string]any{"name": group.Name, "forms": group.Forms, "limit": group.Limit})
	}
	return out
}

func parseFetchedFilingsForGroup(dbPath, rawRoot string, seed securitySeed, group filingIngestGroup, parsedAccessions map[string]bool, continueOnError bool, errorsOut *[]string) (int, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return 0, err
	}
	filings, err := fetchedFilingMetadataList(db, seed.CIK, group.Forms, group.Limit)
	if err != nil {
		return 0, err
	}
	parsed := 0
	for _, filing := range filings {
		if parsedAccessions[filing.Accession] {
			continue
		}
		if err := cmdXBRLParse(secXBRLAccessionArgs(dbPath, rawRoot, filing.Accession)); err != nil {
			if err := recordIssuerBatchError(errorsOut, continueOnError, "xbrl "+group.Name+" "+filing.Accession, seed, err); err != nil {
				return parsed, err
			}
			continue
		}
		parsedAccessions[filing.Accession] = true
		parsed++
	}
	return parsed, nil
}

func importSecuritySeedFile(dbPath, csvPath string, defaultInvestable bool) (securitySeedBatch, map[string]any, error) {
	batch, err := readSecuritySeedBatch(csvPath, defaultInvestable)
	if err != nil {
		return securitySeedBatch{}, nil, err
	}
	db, err := openDB(dbPath)
	if err != nil {
		return securitySeedBatch{}, nil, err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return securitySeedBatch{}, nil, err
	}
	event, err := importSecuritySeeds(db, batch, csvPath)
	if err != nil {
		return securitySeedBatch{}, nil, err
	}
	return batch, event, nil
}

func readSecuritySeedBatch(path string, defaultInvestable bool) (securitySeedBatch, error) {
	if strings.TrimSpace(path) == "" {
		return securitySeedBatch{}, fail(2, "--csv is required")
	}
	var input *os.File
	var err error
	if path == "-" {
		input = os.Stdin
	} else {
		input, err = os.Open(path)
		if err != nil {
			return securitySeedBatch{}, err
		}
		defer input.Close()
	}

	reader := csv.NewReader(input)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	header, err := reader.Read()
	if err == io.EOF {
		return securitySeedBatch{}, fail(2, "CSV %s is empty", path)
	}
	if err != nil {
		return securitySeedBatch{}, err
	}
	index := securityCSVHeaderIndex(header)
	if !csvHasAny(index, "cik", "cik_str", "central_index_key") {
		return securitySeedBatch{}, fail(2, "CSV %s missing CIK header", path)
	}
	if !csvHasAny(index, "symbol", "ticker") {
		return securitySeedBatch{}, fail(2, "CSV %s missing symbol/ticker header", path)
	}
	if !csvHasAny(index, "price_usd", "price", "last_price") {
		return securitySeedBatch{}, fail(2, "CSV %s missing price_usd header", path)
	}
	if !csvHasAny(index, "adv_usd", "dollar_volume_usd", "avg_daily_value_usd", "average_daily_value_usd") {
		return securitySeedBatch{}, fail(2, "CSV %s missing adv_usd header", path)
	}

	batch := securitySeedBatch{}
	seenCIK := map[string]int{}
	for row := 2; ; row++ {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return securitySeedBatch{}, fail(2, "CSV %s row %d: %v", path, row, err)
		}
		if csvRecordEmpty(record) {
			continue
		}
		batch.InputRows++
		seed, err := parseSecuritySeedRow(path, row, record, index, defaultInvestable)
		if err != nil {
			return securitySeedBatch{}, err
		}
		if prior, ok := seenCIK[seed.CIK]; ok {
			batch.DuplicateCIKRows++
			if seed.ADVUSD > batch.Seeds[prior].ADVUSD {
				batch.Seeds[prior] = seed
			}
			continue
		}
		seenCIK[seed.CIK] = len(batch.Seeds)
		batch.Seeds = append(batch.Seeds, seed)
	}
	if len(batch.Seeds) == 0 {
		return securitySeedBatch{}, fail(2, "CSV %s contains no security rows", path)
	}
	return batch, nil
}

func parseSecuritySeedRow(path string, row int, record []string, index map[string]int, defaultInvestable bool) (securitySeed, error) {
	rawCIK := csvValue(record, index, "cik", "cik_str", "central_index_key")
	if !containsDigit(rawCIK) {
		return securitySeed{}, fail(2, "CSV %s row %d: cik is required", path, row)
	}
	cik := normalizeCIK(rawCIK)
	securityID, err := strconv.ParseInt(cik, 10, 64)
	if err != nil || securityID <= 0 {
		return securitySeed{}, fail(2, "CSV %s row %d: invalid cik %q", path, row, rawCIK)
	}
	symbol := strings.ToUpper(csvValue(record, index, "symbol", "ticker"))
	if symbol == "" {
		return securitySeed{}, fail(2, "CSV %s row %d: symbol/ticker is required", path, row)
	}
	price, err := parseCSVFloat(path, row, "price_usd", csvValue(record, index, "price_usd", "price", "last_price"))
	if err != nil {
		return securitySeed{}, err
	}
	adv, err := parseCSVFloat(path, row, "adv_usd", csvValue(record, index, "adv_usd", "dollar_volume_usd", "avg_daily_value_usd", "average_daily_value_usd"))
	if err != nil {
		return securitySeed{}, err
	}
	investable := defaultInvestable
	if raw := csvValue(record, index, "investable", "investable_flag", "is_investable"); raw != "" {
		investable, err = parseCSVBool(path, row, "investable", raw)
		if err != nil {
			return securitySeed{}, err
		}
	}
	return securitySeed{Row: row, CIK: cik, Symbol: symbol, PriceUSD: price, ADVUSD: adv, Investable: investable}, nil
}

func importSecuritySeeds(db *sql.DB, batch securitySeedBatch, source string) (map[string]any, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := utcNow()
	for _, seed := range batch.Seeds {
		securityID, _ := strconv.ParseInt(seed.CIK, 10, 64)
		if _, err := tx.Exec(`INSERT INTO securities(security_id, cik, symbol, investable, price_usd, adv_usd, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(security_id) DO UPDATE SET cik=excluded.cik, symbol=excluded.symbol,
investable=excluded.investable, price_usd=excluded.price_usd, adv_usd=excluded.adv_usd, updated_at=excluded.updated_at`,
			securityID, seed.CIK, seed.Symbol, boolInt(seed.Investable), seed.PriceUSD, seed.ADVUSD, now); err != nil {
			return nil, err
		}
	}
	event, err := appendEvent(tx, "security_master_imported", map[string]any{
		"source":                       source,
		"input_rows":                   batch.InputRows,
		"security_count":               len(batch.Seeds),
		"duplicate_cik_rows_collapsed": batch.DuplicateCIKRows,
		"dedupe_policy":                "same_cik_keeps_highest_adv_usd",
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

func appendBatchEvent(dbPath, eventType string, payload map[string]any) (map[string]any, error) {
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	event, err := appendEvent(tx, eventType, payload)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return event, nil
}

func secWatchArgs(dbPath, cik, forms string, limit int, userAgent string) []string {
	args := []string{"--db", dbPath, "--cik", cik, "--forms", forms, "--limit", strconv.Itoa(limit)}
	if strings.TrimSpace(userAgent) != "" {
		args = append(args, "--user-agent", userAgent)
	}
	return args
}

func secPullArgs(dbPath, rawRoot, cik, forms string, limit int, sleepS float64, userAgent string) []string {
	args := []string{
		"--db", dbPath,
		"--raw-root", rawRoot,
		"--cik", cik,
		"--forms", forms,
		"--limit", strconv.Itoa(limit),
		"--sleep-s", formatFloatFlag(sleepS),
	}
	if strings.TrimSpace(userAgent) != "" {
		args = append(args, "--user-agent", userAgent)
	}
	return args
}

func secXBRLLatestArgs(dbPath, rawRoot, cik, forms string) []string {
	return []string{"--db", dbPath, "--raw-root", rawRoot, "--latest", "--cik", cik, "--forms", forms}
}

func secXBRLAccessionArgs(dbPath, rawRoot, accession string) []string {
	return []string{"--db", dbPath, "--raw-root", rawRoot, "--accession", accession}
}

func secUniverseArgs(dbPath, name string, minADV float64, minFacts int) []string {
	return []string{"--db", dbPath, "--name", name, "--min-adv-usd", formatFloatFlag(minADV), "--min-fact-count", strconv.Itoa(minFacts)}
}

func recordIssuerBatchError(errorsOut *[]string, continueOnError bool, stage string, seed securitySeed, err error) error {
	if err == nil {
		return nil
	}
	msg := fmt.Sprintf("%s %s %s: %v", stage, seed.Symbol, seed.CIK, err)
	if !continueOnError {
		return fail(1, "%s", msg)
	}
	*errorsOut = append(*errorsOut, msg)
	fmt.Fprintf(os.Stderr, "batch_error %s\n", msg)
	return nil
}

func securityCSVHeaderIndex(header []string) map[string]int {
	out := map[string]int{}
	for i, h := range header {
		key := normalizeCSVHeader(h)
		if key != "" {
			out[key] = i
		}
	}
	return out
}

func normalizeCSVHeader(value string) string {
	key := strings.ToLower(strings.TrimSpace(value))
	key = strings.NewReplacer("-", "_", " ", "_", ".", "_").Replace(key)
	for strings.Contains(key, "__") {
		key = strings.ReplaceAll(key, "__", "_")
	}
	return strings.Trim(key, "_")
}

func csvHasAny(index map[string]int, names ...string) bool {
	for _, name := range names {
		if _, ok := index[name]; ok {
			return true
		}
	}
	return false
}

func csvValue(record []string, index map[string]int, names ...string) string {
	for _, name := range names {
		i, ok := index[name]
		if !ok || i >= len(record) {
			continue
		}
		value := strings.TrimSpace(record[i])
		if value != "" {
			return value
		}
	}
	return ""
}

func csvRecordEmpty(record []string) bool {
	for _, cell := range record {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

func parseCSVFloat(path string, row int, name, value string) (float64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fail(2, "CSV %s row %d: %s is required", path, row, name)
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fail(2, "CSV %s row %d: invalid %s %q", path, row, name, value)
	}
	if v < 0 {
		return 0, fail(2, "CSV %s row %d: %s must be non-negative", path, row, name)
	}
	return v, nil
}

func parseCSVBool(path string, row int, name, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "t", "yes", "y", "investable":
		return true, nil
	case "0", "false", "f", "no", "n", "not_investable", "not-investable":
		return false, nil
	default:
		return false, fail(2, "CSV %s row %d: invalid %s %q", path, row, name, value)
	}
}

func containsDigit(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func formatFloatFlag(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func nullableEmptyString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
