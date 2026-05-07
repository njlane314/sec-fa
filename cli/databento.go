package main

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultDatabentoBaseURL = "https://hist.databento.com/v0"
	defaultDatabentoDataset = "EQUS.MINI"
	defaultDatabentoSchema  = "ohlcv-1d"
)

type databentoSecurityMasterRow struct {
	Symbol            string
	CIK               string
	IssuerName        string
	SecurityType      string
	ListingStatus     string
	ListingCountry    string
	TradingCurrency   string
	GICS              string
	SharesOutstanding float64
	Raw               map[string]any
}

type databentoBarRow struct {
	Symbol string
	Time   string
	Close  float64
	Volume float64
	Raw    map[string]any
}

type databentoSecuritySeedRow struct {
	Symbol            string
	CIK               string
	IssuerName        string
	PriceUSD          float64
	ADVUSD            float64
	Investable        bool
	SharesOutstanding float64
	MarketCapUSD      float64
	LatestBarTime     string
	BarCount          int
}

type databentoClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func cmdDatabento(args []string) error {
	fs := newFlagSet("databento")
	dbPath := fs.String("db", defaultDBPath, "")
	outPath := fs.String("out", "", "")
	symbolsRaw := fs.String("symbols", "", "")
	dataset := fs.String("dataset", defaultDatabentoDataset, "")
	schema := fs.String("schema", defaultDatabentoSchema, "")
	stypeIn := fs.String("stype-in", "raw_symbol", "")
	baseURL := fs.String("base-url", defaultDatabentoBaseURL, "")
	start := fs.String("start", "", "")
	end := fs.String("end", "", "")
	lookbackDays := fs.Int("lookback-days", 7, "")
	advWindowDays := fs.Int("adv-window-days", 5, "")
	countries := fs.String("countries", "US", "")
	securityTypes := fs.String("security-types", "EQS", "")
	defaultInvestable := fs.Int("default-investable", 1, "")
	importDB := fs.Bool("import-db", false, "")
	maxSymbols := fs.Int("max-symbols", 100, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *maxSymbols <= 0 {
		return fail(2, "--max-symbols must be positive")
	}
	if *lookbackDays <= 0 {
		return fail(2, "--lookback-days must be positive")
	}
	if *advWindowDays <= 0 {
		return fail(2, "--adv-window-days must be positive")
	}
	symbols, err := parseDatabentoSymbols(*symbolsRaw, *maxSymbols)
	if err != nil {
		return err
	}
	rangeStart, rangeEnd := databentoTimeRange(*start, *end, *lookbackDays)
	key := strings.TrimSpace(os.Getenv("DATABENTO_API_KEY"))
	if key == "" {
		return fail(2, "DATABENTO_API_KEY is not set")
	}
	client := databentoClient{
		baseURL: strings.TrimRight(*baseURL, "/"),
		apiKey:  key,
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
	securityRows, err := client.fetchSecurityMaster(symbols, *stypeIn, *countries, *securityTypes)
	if err != nil {
		return err
	}
	barRows, err := client.fetchOHLCV(symbols, *dataset, *schema, *stypeIn, rangeStart, rangeEnd)
	if err != nil {
		return err
	}
	seeds, err := buildDatabentoSecuritySeeds(symbols, securityRows, barRows, *advWindowDays, *defaultInvestable != 0)
	if err != nil {
		return err
	}
	if *outPath != "" {
		if err := writeDatabentoSeedCSV(*outPath, seeds, *dataset, *schema); err != nil {
			return err
		}
	} else if !*importDB {
		if err := writeDatabentoSeedCSV("-", seeds, *dataset, *schema); err != nil {
			return err
		}
		return nil
	}

	var importEvent map[string]any
	if *importDB {
		batch := databentoSecuritySeedBatch(seeds)
		db, err := openDB(*dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		if err := ensureDB(db); err != nil {
			return err
		}
		importEvent, err = importSecuritySeeds(db, batch, "databento:"+*dataset+":"+*schema)
		if err != nil {
			return err
		}
	}
	return jsonLine(map[string]any{
		"source":             "databento",
		"dataset":            *dataset,
		"schema":             *schema,
		"symbols":            symbols,
		"start":              rangeStart,
		"end":                rangeEnd,
		"security_rows":      len(securityRows),
		"bar_rows":           len(barRows),
		"seed_rows":          len(seeds),
		"out":                nullableEmptyString(*outPath),
		"imported":           *importDB,
		"import_event":       nullableMap(importEvent),
		"api_key_env":        "DATABENTO_API_KEY",
		"adv_window_days":    *advWindowDays,
		"default_investable": *defaultInvestable != 0,
	})
}

func parseDatabentoSymbols(raw string, maxSymbols int) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fail(2, "--symbols is required")
	}
	seen := map[string]bool{}
	var symbols []string
	for _, part := range strings.Split(raw, ",") {
		symbol := strings.ToUpper(strings.TrimSpace(part))
		if symbol == "" {
			continue
		}
		if symbol == "ALL_SYMBOLS" {
			return nil, fail(2, "ALL_SYMBOLS is not allowed; pass an explicit limited symbol list")
		}
		if seen[symbol] {
			continue
		}
		seen[symbol] = true
		symbols = append(symbols, symbol)
	}
	if len(symbols) == 0 {
		return nil, fail(2, "--symbols contains no valid symbols")
	}
	if len(symbols) > maxSymbols {
		return nil, fail(2, "--symbols has %d symbols, over --max-symbols %d", len(symbols), maxSymbols)
	}
	return symbols, nil
}

func databentoTimeRange(start, end string, lookbackDays int) (string, string) {
	if strings.TrimSpace(end) == "" {
		end = time.Now().UTC().Format("2006-01-02")
	}
	if strings.TrimSpace(start) == "" {
		if parsed, err := time.Parse("2006-01-02", end); err == nil {
			start = parsed.AddDate(0, 0, -lookbackDays).Format("2006-01-02")
		} else {
			start = time.Now().UTC().AddDate(0, 0, -lookbackDays).Format("2006-01-02")
		}
	}
	return strings.TrimSpace(start), strings.TrimSpace(end)
}

func (c databentoClient) fetchSecurityMaster(symbols []string, stypeIn, countries, securityTypes string) ([]databentoSecurityMasterRow, error) {
	values := url.Values{}
	values.Set("symbols", strings.Join(symbols, ","))
	values.Set("stype_in", stypeIn)
	values.Set("compression", "none")
	if strings.TrimSpace(countries) != "" {
		values.Set("countries", countries)
	}
	if strings.TrimSpace(securityTypes) != "" {
		values.Set("security_types", securityTypes)
	}
	body, err := c.postForm("security_master.get_last", values)
	if err != nil {
		return nil, err
	}
	return parseDatabentoSecurityMasterJSONL(body)
}

func (c databentoClient) fetchOHLCV(symbols []string, dataset, schema, stypeIn, start, end string) ([]databentoBarRow, error) {
	values := url.Values{}
	values.Set("dataset", dataset)
	values.Set("symbols", strings.Join(symbols, ","))
	values.Set("schema", schema)
	values.Set("start", start)
	values.Set("end", end)
	values.Set("stype_in", stypeIn)
	values.Set("stype_out", "raw_symbol")
	values.Set("encoding", "json")
	values.Set("compression", "none")
	values.Set("pretty_px", "true")
	values.Set("pretty_ts", "true")
	values.Set("map_symbols", "true")
	body, err := c.postForm("timeseries.get_range", values)
	if err != nil {
		return nil, err
	}
	return parseDatabentoBarsJSONL(body)
}

func (c databentoClient) postForm(method string, values url.Values) ([]byte, error) {
	if c.httpClient == nil {
		c.httpClient = http.DefaultClient
	}
	endpoint := c.baseURL + "/" + method
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.apiKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json, application/x-ndjson, text/plain")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if len(body) > 512 {
			body = body[:512]
		}
		return nil, fail(2, "databento %s returned HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if readErr != nil {
		return nil, readErr
	}
	return body, nil
}

func parseDatabentoSecurityMasterJSONL(body []byte) ([]databentoSecurityMasterRow, error) {
	var out []databentoSecurityMasterRow
	err := scanJSONLines(body, func(raw map[string]any) error {
		rawCIK := stringField(raw, "cik")
		row := databentoSecurityMasterRow{
			Symbol:            strings.ToUpper(stringField(raw, "symbol")),
			CIK:               normalizeCIK(rawCIK),
			IssuerName:        stringField(raw, "issuer_name"),
			SecurityType:      stringField(raw, "security_type"),
			ListingStatus:     stringField(raw, "listing_status"),
			ListingCountry:    stringField(raw, "listing_country"),
			TradingCurrency:   stringField(raw, "trading_currency"),
			GICS:              stringField(raw, "gics"),
			SharesOutstanding: floatField(raw, "shares_outstanding"),
			Raw:               raw,
		}
		if row.Symbol != "" && containsDigit(rawCIK) {
			out = append(out, row)
		}
		return nil
	})
	return out, err
}

func parseDatabentoBarsJSONL(body []byte) ([]databentoBarRow, error) {
	var out []databentoBarRow
	err := scanJSONLines(body, func(raw map[string]any) error {
		row := databentoBarRow{
			Symbol: strings.ToUpper(stringField(raw, "symbol")),
			Time:   databentoRecordTime(raw),
			Close:  floatField(raw, "close"),
			Volume: floatField(raw, "volume"),
			Raw:    raw,
		}
		if row.Symbol != "" && row.Close > 0 && row.Volume >= 0 {
			out = append(out, row)
		}
		return nil
	})
	return out, err
}

func scanJSONLines(body []byte, visit func(map[string]any) error) error {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(line, &raw); err != nil {
			return err
		}
		if err := visit(raw); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func databentoRecordTime(raw map[string]any) string {
	for _, key := range []string{"ts_event", "ts_recv", "ts_record", "ts_effective"} {
		if value := stringField(raw, key); value != "" {
			return value
		}
	}
	if nested, ok := raw["hd"].(map[string]any); ok {
		return stringField(nested, "ts_event")
	}
	return ""
}

func buildDatabentoSecuritySeeds(symbols []string, securityRows []databentoSecurityMasterRow, barRows []databentoBarRow, advWindowDays int, investable bool) ([]databentoSecuritySeedRow, error) {
	securityBySymbol := map[string]databentoSecurityMasterRow{}
	for _, row := range securityRows {
		if existing, ok := securityBySymbol[row.Symbol]; ok && existing.SharesOutstanding >= row.SharesOutstanding {
			continue
		}
		securityBySymbol[row.Symbol] = row
	}
	barsBySymbol := map[string][]databentoBarRow{}
	for _, row := range barRows {
		barsBySymbol[row.Symbol] = append(barsBySymbol[row.Symbol], row)
	}
	var missing []string
	var out []databentoSecuritySeedRow
	for _, symbol := range symbols {
		security, ok := securityBySymbol[symbol]
		if !ok {
			missing = append(missing, symbol+":security_master")
			continue
		}
		bars := barsBySymbol[symbol]
		if len(bars) == 0 {
			missing = append(missing, symbol+":ohlcv")
			continue
		}
		sort.SliceStable(bars, func(i, j int) bool { return bars[i].Time < bars[j].Time })
		windowStart := len(bars) - advWindowDays
		if windowStart < 0 {
			windowStart = 0
		}
		var dollarVolumeSum float64
		var dollarVolumeCount int
		for _, bar := range bars[windowStart:] {
			if finiteNonNegative(bar.Close) && finiteNonNegative(bar.Volume) {
				dollarVolumeSum += bar.Close * bar.Volume
				dollarVolumeCount++
			}
		}
		latest := bars[len(bars)-1]
		adv := 0.0
		if dollarVolumeCount > 0 {
			adv = dollarVolumeSum / float64(dollarVolumeCount)
		}
		marketCap := 0.0
		if security.SharesOutstanding > 0 {
			marketCap = latest.Close * security.SharesOutstanding
		}
		out = append(out, databentoSecuritySeedRow{
			Symbol:            symbol,
			CIK:               security.CIK,
			IssuerName:        security.IssuerName,
			PriceUSD:          latest.Close,
			ADVUSD:            adv,
			Investable:        investable,
			SharesOutstanding: security.SharesOutstanding,
			MarketCapUSD:      marketCap,
			LatestBarTime:     latest.Time,
			BarCount:          len(bars),
		})
	}
	if len(missing) > 0 {
		return nil, fail(2, "databento response missing required rows: %s", strings.Join(missing, ","))
	}
	return out, nil
}

func writeDatabentoSeedCSV(path string, rows []databentoSecuritySeedRow, dataset, schema string) error {
	var w io.Writer
	var file *os.File
	if path == "-" {
		w = os.Stdout
	} else {
		var err error
		file, err = os.Create(path)
		if err != nil {
			return err
		}
		defer file.Close()
		w = file
	}
	writer := csv.NewWriter(w)
	defer writer.Flush()
	if err := writer.Write([]string{"cik", "ticker", "price_usd", "adv_usd", "investable", "company", "shares_outstanding", "market_cap_usd", "databento_dataset", "databento_schema", "databento_bar_count", "databento_latest_ts"}); err != nil {
		return err
	}
	for _, row := range rows {
		record := []string{
			row.CIK,
			row.Symbol,
			formatCSVFloat(row.PriceUSD),
			formatCSVFloat(row.ADVUSD),
			strconv.Itoa(boolInt(row.Investable)),
			row.IssuerName,
			formatCSVFloat(row.SharesOutstanding),
			formatCSVFloat(row.MarketCapUSD),
			dataset,
			schema,
			strconv.Itoa(row.BarCount),
			row.LatestBarTime,
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	return writer.Error()
}

func databentoSecuritySeedBatch(rows []databentoSecuritySeedRow) securitySeedBatch {
	batch := securitySeedBatch{InputRows: len(rows)}
	seenCIK := map[string]int{}
	for _, row := range rows {
		seed := securitySeed{
			CIK:        row.CIK,
			Symbol:     row.Symbol,
			PriceUSD:   row.PriceUSD,
			ADVUSD:     row.ADVUSD,
			Investable: row.Investable,
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
	return batch
}

func stringField(raw map[string]any, key string) string {
	value, ok := raw[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		if math.Trunc(typed) == typed {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func floatField(raw map[string]any, key string) float64 {
	value, ok := raw[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		if finiteNonNegative(typed) || typed < 0 {
			return typed
		}
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			return parsed
		}
	}
	return 0
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func formatCSVFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func nullableMap(value map[string]any) any {
	if value == nil {
		return nil
	}
	return value
}
