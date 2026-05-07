package main

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCmdDatabentoWritesSeedCSV(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "test-key" || pass != "" {
			t.Fatalf("unexpected Databento auth")
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/security_master.get_last":
			if got := r.Form.Get("symbols"); got != "AAPL,MSFT" {
				t.Fatalf("unexpected symbols %q", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"symbol":"AAPL","cik":320193,"issuer_name":"Apple Inc.","security_type":"EQS","listing_country":"US","trading_currency":"USD","shares_outstanding":15000000000}` + "\n"))
			_, _ = w.Write([]byte(`{"symbol":"MSFT","cik":"789019","issuer_name":"Microsoft Corporation","security_type":"EQS","listing_country":"US","trading_currency":"USD","shares_outstanding":"7400000000"}` + "\n"))
		case "/timeseries.get_range":
			if got := r.Form.Get("dataset"); got != defaultDatabentoDataset {
				t.Fatalf("unexpected dataset %q", got)
			}
			if got := r.Form.Get("encoding"); got != "json" {
				t.Fatalf("unexpected encoding %q", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"symbol":"AAPL","ts_event":"2026-05-04T00:00:00Z","close":"100.00","volume":10}` + "\n"))
			_, _ = w.Write([]byte(`{"symbol":"AAPL","ts_event":"2026-05-05T00:00:00Z","close":"110.00","volume":20}` + "\n"))
			_, _ = w.Write([]byte(`{"symbol":"MSFT","ts_event":"2026-05-05T00:00:00Z","close":"200.00","volume":30}` + "\n"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	t.Setenv("DATABENTO_API_KEY", "test-key")
	out := filepath.Join(t.TempDir(), "seed.csv")
	if err := cmdDatabento([]string{
		"--base-url", server.URL,
		"--symbols", "AAPL,MSFT",
		"--start", "2026-05-01",
		"--end", "2026-05-06",
		"--adv-window-days", "2",
		"--out", out,
	}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("expected header plus 2 rows, got %d", len(records))
	}
	if records[1][0] != "0000320193" || records[1][1] != "AAPL" || records[1][2] != "110" {
		t.Fatalf("unexpected AAPL row: %#v", records[1])
	}
	if records[1][3] != "1600" {
		t.Fatalf("unexpected AAPL ADV: %#v", records[1])
	}
	if records[2][0] != "0000789019" || records[2][1] != "MSFT" || records[2][3] != "6000" {
		t.Fatalf("unexpected MSFT row: %#v", records[2])
	}
}

func TestParseDatabentoSymbolsRejectsAllSymbols(t *testing.T) {
	if _, err := parseDatabentoSymbols("AAPL,ALL_SYMBOLS", 10); err == nil {
		t.Fatal("expected ALL_SYMBOLS to be rejected")
	}
}
