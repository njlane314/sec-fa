package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type periodInfo struct {
	RawStartDate        sql.NullString
	RawEndDate          sql.NullString
	RawInstantDate      sql.NullString
	StartDateInclusive  sql.NullString
	EndDateExclusive    sql.NullString
	DurationDays        int
	PeriodKind          string
	PeriodSemantics     string
	FiscalYear          sql.NullInt64
	FiscalPeriod        sql.NullString
	FiscalPeriodOrdinal sql.NullInt64
	PeriodLengthClass   string
}

type contextInfo struct {
	ID               string
	EntityIdentifier string
	Period           periodInfo
	DimensionsHash   string
	DimensionsJSON   string
	SegmentJSON      sql.NullString
	ScenarioJSON     sql.NullString
	ScopeClass       string
	AxisCount        int
}

type unitInfo struct {
	ID              string
	Signature       string
	NumeratorJSON   sql.NullString
	DenominatorJSON sql.NullString
}

type rawFact struct {
	SourceDocID      string
	SecurityID       int64
	CIK              string
	Accession        string
	Taxonomy         string
	ConceptQName     string
	ConceptLocalName string
	Context          contextInfo
	ContextID        string
	RawContextID     string
	Unit             *unitInfo
	UnitID           sql.NullString
	ValueDecimal     sql.NullFloat64
	ValueText        sql.NullString
	DecimalsAttr     sql.NullString
	PrecisionAttr    sql.NullString
	IsNil            int
	Form             string
	FiledAt          string
	AcceptedAt       string
	AvailableAt      string
	DimensionsHash   string
	DimensionalScope string
	DimensionsJSON   string
}

type filingFiscalMetadata struct {
	FiscalYearFocus      sql.NullInt64
	FiscalPeriodFocus    sql.NullString
	FiscalPeriodOrdinal  sql.NullInt64
	DocumentPeriodEnd    sql.NullString
	CurrentFiscalYearEnd sql.NullString
}

func cmdXBRLParse(args []string) error {
	fs := newFlagSet("xbrl")
	dbPath := fs.String("db", defaultDBPath, "")
	accession := fs.String("accession", "", "")
	cik := fs.String("cik", "", "")
	symbol := fs.String("symbol", "", "")
	form := fs.String("form", "", "")
	filedAt := fs.String("filed-at", "", "")
	acceptedAt := fs.String("accepted-at", "", "")
	rawRoot := fs.String("raw-root", "raw", "")
	packageRoot := fs.String("package-root", "", "")
	userAgent := fs.String("user-agent", "", "")
	latest := fs.Bool("latest", false, "")
	forms := fs.String("forms", "", "")
	sleepS := fs.Float64("sleep-s", 0, "")
	strict := fs.Bool("strict", false, "")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	_ = symbol
	_ = userAgent
	_ = sleepS
	_ = strict
	db, err := openDB(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDB(db); err != nil {
		return err
	}
	var meta filingMetadata
	var found bool
	if *accession == "" {
		if !*latest {
			return fail(2, "--accession is required unless --latest is set")
		}
		meta, found, err = latestFetchedFilingMetadata(db, *cik, *forms)
		if err != nil {
			return err
		}
		if !found {
			return fail(2, "no fetched filing found; run watch and pull first or pass --accession")
		}
		*accession = meta.Accession
	} else {
		meta, found, err = lookupFilingMetadata(db, *accession)
		if err != nil {
			return err
		}
	}
	if *cik == "" && found {
		*cik = meta.CIK
	}
	if *cik == "" {
		return fail(2, "--cik is required when accession metadata is not present; run watch first or pass --cik")
	}
	cik10 := normalizeCIK(*cik)
	if *form == "" && found {
		*form = meta.Form
	}
	if *form == "" {
		*form = "10-Q"
	}
	if *filedAt == "" && found && meta.FilingDate.Valid {
		*filedAt = meta.FilingDate.String
	}
	if *acceptedAt == "" && found && meta.AcceptedAt.Valid {
		*acceptedAt = meta.AcceptedAt.String
	}
	securityID, _ := strconv.ParseInt(cik10, 10, 64)
	if *acceptedAt == "" {
		*acceptedAt = utcNow()
	}
	if *filedAt == "" {
		*filedAt = strings.Split(*acceptedAt, "T")[0]
	}
	root := *packageRoot
	if root == "" {
		root = filepath.Join(*rawRoot, cik10, strings.ReplaceAll(*accession, "-", ""))
	}
	files, err := loadPackageFiles(root, cik10, *accession, *rawRoot, meta.PrimaryDocumentString())
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO filings(accession_number, cik, form, filing_date, accepted_at, source_url, ingested_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, *accession, cik10, *form, *filedAt, *acceptedAt, archiveURL(cik10, *accession, ""), utcNow()); err != nil {
		return err
	}

	indexDocID := ""
	documentsStored := 0
	rawInserted := 0
	selectedInserted := 0
	parseErrors := []string{}
	contexts := map[string]contextInfo{}
	units := map[string]unitInfo{}

	for _, f := range files {
		docID := sourceDocumentID(cik10, f.URL, f.Hash)
		if f.Name == "index.json" {
			indexDocID = docID
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO source_documents(doc_id, cik, accession_number, form, filed_at, source_url, local_path, sha256, document_kind, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			docID, cik10, *accession, *form, *filedAt, f.URL, f.Path, f.Hash, f.Kind, utcNow()); err != nil {
			return err
		}
		documentsStored++
		if f.Kind != "inline_xbrl" && f.Kind != "xbrl_instance" {
			continue
		}
		root, err := parseXMLTree(f.Data)
		if err != nil {
			parseErrors = append(parseErrors, fmt.Sprintf("%s: %v", f.Name, err))
			continue
		}
		docContexts := parseContexts(root)
		fiscalMeta := parseFilingFiscalMetadata(root)
		applyFilingFiscalMetadata(docContexts, fiscalMeta)
		for id, ctx := range docContexts {
			contexts[id] = ctx
		}
		docUnits := parseUnits(root)
		for id, unit := range docUnits {
			units[id] = unit
		}
		for _, ctx := range docContexts {
			if err := insertContext(tx, cik10, *accession, ctx); err != nil {
				return err
			}
		}
		for _, unit := range docUnits {
			if err := insertUnit(tx, cik10, *accession, unit); err != nil {
				return err
			}
		}
		var facts []rawFact
		if f.Kind == "inline_xbrl" {
			facts = parseInlineFacts(root, docID, securityID, cik10, *accession, *form, *filedAt, *acceptedAt, docContexts, docUnits)
		} else {
			facts = parseClassicFacts(root, docID, securityID, cik10, *accession, *form, *filedAt, *acceptedAt, docContexts, docUnits)
		}
		for _, fact := range facts {
			inserted, err := insertRawFact(tx, fact)
			if err != nil {
				return err
			}
			if inserted {
				rawInserted++
			}
			ok, err := insertCanonicalObservation(tx, fact)
			if err != nil {
				return err
			}
			if ok {
				selectedInserted++
			}
		}
		fmt.Fprintf(os.Stderr, "parsed %s: kind=%s facts=%d\n", f.Name, f.Kind, len(facts))
	}
	event, err := appendEvent(tx, "xbrl_package_parsed", map[string]any{
		"accession_number": *accession, "cik": cik10, "security_id": securityID, "form": *form,
		"index_doc_id": indexDocID, "documents_stored": documentsStored, "raw_facts_inserted": rawInserted,
		"canonical_candidates": selectedInserted, "canonical_selected_inserted": selectedInserted,
		"derived_quarter_observations_inserted": 0, "canonical_exceptions_inserted": 0,
		"resolver_version": resolverVersion, "parse_errors": parseErrors,
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "xbrl packages processed documents=%d raw_inserted=%d selected_inserted=%d derived_inserted=0 exceptions_inserted=0 parse_errors=%d\n",
		documentsStored-1, rawInserted, selectedInserted, len(parseErrors))
	_ = contexts
	_ = units
	return jsonLine(event)
}

type filingMetadata struct {
	Accession       string
	CIK             string
	Form            string
	FilingDate      sql.NullString
	AcceptedAt      sql.NullString
	PrimaryDocument sql.NullString
}

func (m filingMetadata) PrimaryDocumentString() string {
	if m.PrimaryDocument.Valid {
		return m.PrimaryDocument.String
	}
	return ""
}

func lookupFilingMetadata(db *sql.DB, accession string) (filingMetadata, bool, error) {
	var meta filingMetadata
	err := db.QueryRow(`SELECT accession_number, cik, form, filing_date, accepted_at, primary_document
FROM filings WHERE accession_number=?`, accession).Scan(
		&meta.Accession, &meta.CIK, &meta.Form, &meta.FilingDate, &meta.AcceptedAt, &meta.PrimaryDocument)
	if err == sql.ErrNoRows {
		return filingMetadata{}, false, nil
	}
	if err != nil {
		return filingMetadata{}, false, err
	}
	return meta, true, nil
}

func latestFetchedFilingMetadata(db *sql.DB, cik, forms string) (filingMetadata, bool, error) {
	query := `SELECT accession_number, cik, form, filing_date, accepted_at, primary_document
FROM filings WHERE raw_index_uri IS NOT NULL`
	var args []any
	if cik != "" {
		query += ` AND cik=?`
		args = append(args, normalizeCIK(cik))
	}
	if clause, formArgs := sqlFormFilterClause("form", forms); clause != "" {
		query += clause
		args = append(args, formArgs...)
	}
	query += ` ORDER BY filing_date DESC, accepted_at DESC LIMIT 1`
	var meta filingMetadata
	err := db.QueryRow(query, args...).Scan(
		&meta.Accession, &meta.CIK, &meta.Form, &meta.FilingDate, &meta.AcceptedAt, &meta.PrimaryDocument)
	if err == sql.ErrNoRows {
		return filingMetadata{}, false, nil
	}
	if err != nil {
		return filingMetadata{}, false, err
	}
	return meta, true, nil
}

func fetchedFilingMetadataList(db *sql.DB, cik, forms string, limit int) ([]filingMetadata, error) {
	if limit <= 0 {
		return nil, nil
	}
	query := `SELECT accession_number, cik, form, filing_date, accepted_at, primary_document
FROM filings WHERE raw_index_uri IS NOT NULL`
	var args []any
	if cik != "" {
		query += ` AND cik=?`
		args = append(args, normalizeCIK(cik))
	}
	if clause, formArgs := sqlFormFilterClause("form", forms); clause != "" {
		query += clause
		args = append(args, formArgs...)
	}
	query += ` ORDER BY filing_date DESC, accepted_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []filingMetadata
	for rows.Next() {
		var meta filingMetadata
		if err := rows.Scan(&meta.Accession, &meta.CIK, &meta.Form, &meta.FilingDate, &meta.AcceptedAt, &meta.PrimaryDocument); err != nil {
			return nil, err
		}
		out = append(out, meta)
	}
	return out, rows.Err()
}

type packageFile struct {
	Name string
	Path string
	URL  string
	Hash string
	Kind string
	Data []byte
}

func loadPackageFiles(packageRoot, cik, accession, rawRoot, primary string) ([]packageFile, error) {
	var out []packageFile
	if packageRoot == "" {
		return nil, fail(2, "xbrl requires a fetched raw package under --raw-root or an explicit --package-root")
	}
	indexPath := filepath.Join(packageRoot, "index.json")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	out = append(out, packageFile{
		Name: "index.json", Path: indexPath, URL: archiveURL(cik, accession, ""), Hash: sha256Hex(indexData), Kind: "sec_archive_index", Data: indexData,
	})
	var idx struct {
		Directory struct {
			Item []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"item"`
		} `json:"directory"`
	}
	if err := json.Unmarshal(indexData, &idx); err != nil {
		return nil, err
	}
	hasExtractedInstance := false
	for _, item := range idx.Directory.Item {
		if isExtractedInlineInstanceName(item.Name) {
			hasExtractedInstance = true
		}
	}
	for _, item := range idx.Directory.Item {
		name := item.Name
		if !isPackageArtifactName(name, primary) {
			continue
		}
		path := filepath.Join(packageRoot, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		out = append(out, packageFile{
			Name: name, Path: path, URL: archiveURL(cik, accession, name), Hash: sha256Hex(data), Kind: packageFileKind(name, primary, hasExtractedInstance), Data: data,
		})
	}
	_ = rawRoot
	return out, nil
}

func packageFileKind(name, primary string, hasExtractedInstance bool) string {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".htm") || strings.HasSuffix(lower, ".html") {
		if hasExtractedInstance && name == primary {
			return "primary_document"
		}
		return "inline_xbrl"
	}
	if strings.HasSuffix(lower, ".xsd") {
		return "xbrl_schema"
	}
	if strings.HasSuffix(lower, "_lab.xml") || strings.HasSuffix(lower, "_pre.xml") ||
		strings.HasSuffix(lower, "_def.xml") || strings.HasSuffix(lower, "_cal.xml") {
		return "xbrl_linkbase"
	}
	if lower == "filingsummary.xml" || lower == "metalinks.json" {
		return "xbrl_report_metadata"
	}
	return "xbrl_instance"
}

func isExtractedInlineInstanceName(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), "_htm.xml")
}

func parseContexts(root *xmlNode) map[string]contextInfo {
	var nodes []*xmlNode
	descendants(root, "context", &nodes)
	out := map[string]contextInfo{}
	for _, n := range nodes {
		id := attr(n, "id")
		if id == "" {
			continue
		}
		entity := firstDescendant(n, "identifier")
		period := child(n, "period")
		info := contextInfo{ID: id, EntityIdentifier: text(entity), DimensionsHash: totalDimensionsHash, DimensionsJSON: totalDimensionsJSON, ScopeClass: "consolidated_total"}
		if period != nil {
			info.Period = parsePeriod(period)
		}
		var members []*xmlNode
		descendants(n, "explicitMember", &members)
		if len(members) > 0 {
			dims := make([]map[string]string, 0, len(members))
			for _, m := range members {
				dims = append(dims, map[string]string{"dimension": attr(m, "dimension"), "member": text(m)})
			}
			sort.Slice(dims, func(i, j int) bool { return dims[i]["dimension"] < dims[j]["dimension"] })
			j := mustCanonicalJSON(dims)
			info.DimensionsJSON = j
			info.DimensionsHash = sha256Hex([]byte(j))
			info.AxisCount = len(dims)
			info.ScopeClass = "segment"
			for _, d := range dims {
				if strings.Contains(strings.ToLower(d["dimension"]), "product") {
					info.ScopeClass = "product"
				}
			}
		}
		out[id] = info
	}
	return out
}

func parseFilingFiscalMetadata(root *xmlNode) filingFiscalMetadata {
	var nodes []*xmlNode
	descendants(root, "", &nodes)
	var out filingFiscalMetadata
	for _, n := range nodes {
		concept := ""
		if name := attr(n, "name"); name != "" {
			_, concept = splitQName(name)
		} else if taxonomyForSpace(n.Name.Space) == "dei" {
			concept = n.Name.Local
		}
		if concept == "" {
			continue
		}
		value := strings.TrimSpace(text(n))
		switch concept {
		case "DocumentFiscalYearFocus":
			if year, ok := parseIntegerText(value); ok {
				out.FiscalYearFocus = sql.NullInt64{Int64: int64(year), Valid: true}
			}
		case "DocumentFiscalPeriodFocus":
			period := normalizeFiscalPeriod(value)
			if period != "" {
				out.FiscalPeriodFocus = sql.NullString{String: period, Valid: true}
				if ordinal := fiscalQuarterOrdinal(period); ordinal > 0 {
					out.FiscalPeriodOrdinal = sql.NullInt64{Int64: int64(ordinal), Valid: true}
				}
			}
		case "DocumentPeriodEndDate":
			if isDate(value) {
				out.DocumentPeriodEnd = sql.NullString{String: value, Valid: true}
			}
		case "CurrentFiscalYearEndDate":
			if value != "" {
				out.CurrentFiscalYearEnd = sql.NullString{String: value, Valid: true}
			}
		}
	}
	return out
}

func applyFilingFiscalMetadata(contexts map[string]contextInfo, meta filingFiscalMetadata) {
	if !meta.FiscalYearFocus.Valid || !meta.FiscalPeriodFocus.Valid || !meta.FiscalPeriodOrdinal.Valid || !meta.DocumentPeriodEnd.Valid {
		return
	}
	for id, ctx := range contexts {
		p := ctx.Period
		end := ""
		if p.RawEndDate.Valid {
			end = p.RawEndDate.String
		} else if p.RawInstantDate.Valid {
			end = p.RawInstantDate.String
		}
		if end == "" {
			continue
		}
		year, ok := comparableFiscalYear(end, meta.DocumentPeriodEnd.String, int(meta.FiscalYearFocus.Int64))
		if !ok {
			continue
		}
		p.FiscalYear = sql.NullInt64{Int64: int64(year), Valid: true}
		if p.PeriodKind == "duration" {
			ordinal := int(meta.FiscalPeriodOrdinal.Int64)
			if isFiscalYTDDuration(p.DurationDays, ordinal) {
				p.PeriodSemantics = "fiscal_ytd"
				p.FiscalPeriod = meta.FiscalPeriodFocus
				p.FiscalPeriodOrdinal = meta.FiscalPeriodOrdinal
			} else if isQuarterDuration(p.DurationDays) {
				p.PeriodSemantics = "fiscal_quarter"
				p.FiscalPeriod = meta.FiscalPeriodFocus
				p.FiscalPeriodOrdinal = meta.FiscalPeriodOrdinal
			}
		}
		ctx.Period = p
		contexts[id] = ctx
	}
}

func parsePeriod(period *xmlNode) periodInfo {
	start := text(child(period, "startDate"))
	end := text(child(period, "endDate"))
	instant := text(child(period, "instant"))
	var p periodInfo
	if instant != "" {
		p.RawInstantDate = sql.NullString{String: instant, Valid: true}
		p.StartDateInclusive = sql.NullString{String: instant, Valid: true}
		p.EndDateExclusive = sql.NullString{String: instant, Valid: true}
		p.PeriodKind = "instant"
		p.PeriodSemantics = "instant"
		p.PeriodLengthClass = "instant"
		if y, err := strconv.Atoi(instant[:4]); err == nil {
			p.FiscalYear = sql.NullInt64{Int64: int64(y), Valid: true}
		}
		return p
	}
	p.RawStartDate = sql.NullString{String: start, Valid: start != ""}
	p.RawEndDate = sql.NullString{String: end, Valid: end != ""}
	p.StartDateInclusive = p.RawStartDate
	if t, err := parseDate(end); err == nil {
		p.EndDateExclusive = sql.NullString{String: t.AddDate(0, 0, 1).Format("2006-01-02"), Valid: true}
	}
	p.PeriodKind = "duration"
	p.DurationDays = durationDays(start, end)
	p.PeriodLengthClass = periodLengthClass(p.DurationDays)
	p.PeriodSemantics = "irregular"
	if isQuarterDuration(p.DurationDays) {
		p.PeriodSemantics = "fiscal_quarter"
	}
	if p.DurationDays >= 330 {
		p.PeriodSemantics = "fiscal_year"
	}
	if len(end) >= 4 {
		if y, err := strconv.Atoi(end[:4]); err == nil {
			p.FiscalYear = sql.NullInt64{Int64: int64(y), Valid: true}
		}
	}
	if p.PeriodSemantics == "fiscal_quarter" {
		q := quarterFromEnd(end)
		p.FiscalPeriod = sql.NullString{String: fmt.Sprintf("Q%d", q), Valid: true}
		p.FiscalPeriodOrdinal = sql.NullInt64{Int64: int64(q), Valid: true}
	}
	return p
}

func parseIntegerText(value string) (int, bool) {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return 0, false
	}
	if strings.Contains(clean, ".") {
		if v, err := strconv.ParseFloat(clean, 64); err == nil {
			return int(v), true
		}
	}
	v, err := strconv.Atoi(clean)
	return v, err == nil
}

func normalizeFiscalPeriod(value string) string {
	clean := strings.ToUpper(strings.TrimSpace(value))
	if clean == "" {
		return ""
	}
	if ordinal := fiscalQuarterOrdinal(clean); ordinal > 0 {
		return fmt.Sprintf("Q%d", ordinal)
	}
	return clean
}

func fiscalQuarterOrdinal(period string) int {
	switch strings.ToUpper(strings.TrimSpace(period)) {
	case "Q1", "1":
		return 1
	case "Q2", "2":
		return 2
	case "Q3", "3":
		return 3
	case "Q4", "4":
		return 4
	default:
		return 0
	}
}

func isDate(value string) bool {
	_, err := parseDate(value)
	return err == nil
}

func comparableFiscalYear(periodEnd, documentEnd string, documentFiscalYear int) (int, bool) {
	endDate, err := parseDate(periodEnd)
	if err != nil {
		return 0, false
	}
	docDate, err := parseDate(documentEnd)
	if err != nil {
		return 0, false
	}
	yearDelta := docDate.Year() - endDate.Year()
	if yearDelta < 0 || yearDelta > 10 {
		return 0, false
	}
	shifted := endDate.AddDate(yearDelta, 0, 0)
	if absDays(shifted.Sub(docDate).Hours()/24) > 10 {
		return 0, false
	}
	return documentFiscalYear - yearDelta, true
}

func absDays(days float64) float64 {
	if days < 0 {
		return -days
	}
	return days
}

func isQuarterDuration(days int) bool {
	return days >= 70 && days <= 110
}

func isFiscalYTDDuration(days, fiscalQuarter int) bool {
	if fiscalQuarter <= 1 {
		return false
	}
	return days >= fiscalQuarter*70 && days <= fiscalQuarter*110
}

func quarterFromEnd(date string) int {
	if len(date) < 7 {
		return 0
	}
	month, _ := strconv.Atoi(date[5:7])
	return ((month - 1) / 3) + 1
}

func periodLengthClass(days int) string {
	switch {
	case days == 0:
		return "instant"
	case days >= 70 && days <= 110:
		return "13w"
	case days >= 330 && days <= 380:
		return "year"
	default:
		return "irregular"
	}
}

func parseUnits(root *xmlNode) map[string]unitInfo {
	var nodes []*xmlNode
	descendants(root, "unit", &nodes)
	out := map[string]unitInfo{}
	for _, n := range nodes {
		id := attr(n, "id")
		if id == "" {
			continue
		}
		var measures []*xmlNode
		descendants(n, "measure", &measures)
		sig := ""
		for _, m := range measures {
			sig = normalizeMeasure(text(m))
			break
		}
		if sig == "" {
			sig = "unknown"
		}
		out[id] = unitInfo{ID: id, Signature: sig}
	}
	return out
}

func normalizeMeasure(value string) string {
	switch strings.TrimSpace(value) {
	case "iso4217:USD", "USD":
		return "USD"
	case "xbrli:shares", "shares":
		return "shares"
	default:
		if strings.Contains(value, "USD") {
			return "USD"
		}
		return strings.TrimSpace(value)
	}
}

func parseInlineFacts(root *xmlNode, docID string, securityID int64, cik, accession, form, filedAt, acceptedAt string, contexts map[string]contextInfo, units map[string]unitInfo) []rawFact {
	var nodes []*xmlNode
	descendants(root, "nonFraction", &nodes)
	descendants(root, "nonNumeric", &nodes)
	out := make([]rawFact, 0, len(nodes))
	for _, n := range nodes {
		name := attr(n, "name")
		if name == "" {
			continue
		}
		tax, local := splitQName(name)
		ctxID := attr(n, "contextRef")
		ctx, ok := contexts[ctxID]
		if !ok {
			continue
		}
		var unit *unitInfo
		unitRef := attr(n, "unitRef")
		if u, ok := units[unitRef]; ok {
			unit = &u
		}
		valueText := text(n)
		var dec sql.NullFloat64
		var txt sql.NullString
		if n.Name.Local == "nonFraction" {
			if v, ok := parseScaledNumber(valueText, attr(n, "scale")); ok {
				dec = sql.NullFloat64{Float64: v, Valid: true}
			}
		} else {
			txt = sql.NullString{String: valueText, Valid: true}
		}
		out = append(out, buildRawFact(docID, securityID, cik, accession, tax, local, ctx, unit, dec, txt, attr(n, "decimals"), attr(n, "precision"), form, filedAt, acceptedAt))
	}
	return out
}

func parseClassicFacts(root *xmlNode, docID string, securityID int64, cik, accession, form, filedAt, acceptedAt string, contexts map[string]contextInfo, units map[string]unitInfo) []rawFact {
	var nodes []*xmlNode
	descendants(root, "", &nodes)
	var out []rawFact
	for _, n := range nodes {
		ctxID := attr(n, "contextRef")
		if ctxID == "" {
			continue
		}
		tax := taxonomyForSpace(n.Name.Space)
		if tax == "" {
			continue
		}
		ctx, ok := contexts[ctxID]
		if !ok {
			continue
		}
		var unit *unitInfo
		if u, ok := units[attr(n, "unitRef")]; ok {
			unit = &u
		}
		valueText := text(n)
		var dec sql.NullFloat64
		var txt sql.NullString
		if v, ok := parseScaledNumber(valueText, "0"); ok {
			dec = sql.NullFloat64{Float64: v, Valid: true}
		} else {
			txt = sql.NullString{String: valueText, Valid: true}
		}
		out = append(out, buildRawFact(docID, securityID, cik, accession, tax, n.Name.Local, ctx, unit, dec, txt, attr(n, "decimals"), attr(n, "precision"), form, filedAt, acceptedAt))
	}
	return out
}

func splitQName(q string) (string, string) {
	parts := strings.SplitN(q, ":", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", q
}

func taxonomyForSpace(space string) string {
	switch {
	case strings.Contains(space, "us-gaap"):
		return "us-gaap"
	case strings.Contains(space, "dei"):
		return "dei"
	default:
		return ""
	}
}

func parseScaledNumber(text, scale string) (float64, bool) {
	clean := strings.ReplaceAll(strings.TrimSpace(text), ",", "")
	if clean == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(clean, 64)
	if err != nil {
		return 0, false
	}
	if scale != "" {
		s, err := strconv.Atoi(scale)
		if err == nil {
			v *= math.Pow10(s)
		}
	}
	return v, true
}

func buildRawFact(docID string, securityID int64, cik, accession, taxonomy, local string, ctx contextInfo, unit *unitInfo, dec sql.NullFloat64, txt sql.NullString, decimals, precision, form, filedAt, acceptedAt string) rawFact {
	unitID := sql.NullString{}
	if unit != nil {
		unitID = sql.NullString{String: unitIDFor(cik, accession, *unit), Valid: true}
	}
	scope := ctx.ScopeClass
	if scope == "" {
		scope = "consolidated_total"
	}
	return rawFact{
		SourceDocID: docID, SecurityID: securityID, CIK: cik, Accession: accession,
		Taxonomy: taxonomy, ConceptQName: taxonomy + ":" + local, ConceptLocalName: local,
		Context: ctx, ContextID: contextIDFor(cik, accession, ctx), RawContextID: ctx.ID,
		Unit: unit, UnitID: unitID, ValueDecimal: dec, ValueText: txt,
		DecimalsAttr:  sql.NullString{String: decimals, Valid: decimals != ""},
		PrecisionAttr: sql.NullString{String: precision, Valid: precision != ""},
		Form:          form, FiledAt: filedAt, AcceptedAt: acceptedAt, AvailableAt: acceptedAt,
		DimensionsHash: ctx.DimensionsHash, DimensionalScope: scope, DimensionsJSON: ctx.DimensionsJSON,
	}
}

func contextIDFor(cik, accession string, ctx contextInfo) string {
	p := ctx.Period
	return stableID("ctx", map[string]any{
		"cik": cik, "accession_number": accession, "raw_context_id": ctx.ID,
		"raw_start_date": nullableString(p.RawStartDate), "raw_end_date": nullableString(p.RawEndDate),
		"raw_instant_date": nullableString(p.RawInstantDate), "fiscal_year": nullableInt(p.FiscalYear),
		"fiscal_period": nullableString(p.FiscalPeriod), "dimensions_hash": ctx.DimensionsHash,
	})
}

func unitIDFor(cik, accession string, unit unitInfo) string {
	return stableID("unit", map[string]any{"cik": cik, "accession_number": accession, "raw_unit_id": unit.ID, "unit_signature": unit.Signature})
}

func periodIDFor(cik, accession string, p periodInfo) string {
	return stableID("period", map[string]any{
		"cik": cik, "accession_number": accession, "raw_start_date": nullableString(p.RawStartDate),
		"raw_end_date": nullableString(p.RawEndDate), "raw_instant_date": nullableString(p.RawInstantDate),
		"period_semantics": p.PeriodSemantics, "fiscal_year": nullableInt(p.FiscalYear), "fiscal_period": nullableString(p.FiscalPeriod),
	})
}

func insertContext(tx execer, cik, accession string, ctx contextInfo) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO dimension_signatures(dimensions_hash, dimensions_json, has_dimensions, scope_class, axis_count, created_at)
VALUES (?, ?, ?, ?, ?, ?)`, ctx.DimensionsHash, ctx.DimensionsJSON, boolInt(ctx.AxisCount > 0), ctx.ScopeClass, ctx.AxisCount, utcNow()); err != nil {
		return err
	}
	pid := periodIDFor(cik, accession, ctx.Period)
	p := ctx.Period
	if _, err := tx.Exec(`INSERT OR IGNORE INTO reporting_periods(
period_id, cik, accession_number, raw_start_date, raw_end_date, raw_instant_date,
start_date_inclusive, end_date_exclusive, duration_days, period_kind, period_semantics,
fiscal_year, fiscal_period, fiscal_period_ordinal, period_length_class, source, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'xbrl_context', ?)`,
		pid, cik, accession, nullableString(p.RawStartDate), nullableString(p.RawEndDate), nullableString(p.RawInstantDate),
		nullableString(p.StartDateInclusive), nullableString(p.EndDateExclusive), p.DurationDays, p.PeriodKind, p.PeriodSemantics,
		nullableInt(p.FiscalYear), nullableString(p.FiscalPeriod), nullableInt(p.FiscalPeriodOrdinal), p.PeriodLengthClass, utcNow()); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT OR IGNORE INTO xbrl_contexts(
context_id, cik, accession_number, raw_context_id, entity_identifier, period_id, period_kind,
raw_start_date, raw_end_date, raw_instant_date, start_date_inclusive, end_date_exclusive,
duration_days, dimensions_hash, dimensions_json, segment_json, scenario_json, raw_context_sha256)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		contextIDFor(cik, accession, ctx), cik, accession, ctx.ID, ctx.EntityIdentifier, pid, p.PeriodKind,
		nullableString(p.RawStartDate), nullableString(p.RawEndDate), nullableString(p.RawInstantDate),
		nullableString(p.StartDateInclusive), nullableString(p.EndDateExclusive), p.DurationDays,
		ctx.DimensionsHash, ctx.DimensionsJSON, nullableString(ctx.SegmentJSON), nullableString(ctx.ScenarioJSON),
		sha256Hex([]byte(ctx.ID+ctx.DimensionsJSON)))
	return err
}

func insertUnit(tx execer, cik, accession string, unit unitInfo) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO xbrl_units(unit_id, cik, accession_number, raw_unit_id, unit_signature, numerator_json, denominator_json, raw_unit_sha256)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, unitIDFor(cik, accession, unit), cik, accession, unit.ID, unit.Signature,
		nullableString(unit.NumeratorJSON), nullableString(unit.DenominatorJSON), sha256Hex([]byte(unit.ID+unit.Signature)))
	return err
}

func insertRawFact(tx execer, fact rawFact) (bool, error) {
	rawHash := rawFactHash(fact)
	factID := stableID("fact", map[string]any{
		"security_id": fact.SecurityID, "taxonomy": fact.Taxonomy, "concept": fact.ConceptQName,
		"unit": nullableString(fact.UnitID), "value_decimal": nullableFloat(fact.ValueDecimal),
		"value_text": nullableString(fact.ValueText), "accession_number": fact.Accession,
		"period_id":  periodIDFor(fact.CIK, fact.Accession, fact.Context.Period),
		"context_id": fact.ContextID, "raw_context_id": fact.RawContextID, "dimensions_hash": fact.DimensionsHash, "form": fact.Form,
	})
	res, err := tx.Exec(`INSERT OR IGNORE INTO xbrl_facts(
fact_id, source_doc_id, security_id, cik, accession_number, taxonomy, concept_qname, concept_local_name,
context_id, unit_id, value_decimal, value_text, decimals_attr, precision_attr, is_nil, raw_fact_hash,
raw_context_id, form, filed_at, accepted_at, available_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		factID, fact.SourceDocID, fact.SecurityID, fact.CIK, fact.Accession, fact.Taxonomy, fact.ConceptQName, fact.ConceptLocalName,
		fact.ContextID, nullableString(fact.UnitID), nullableFloat(fact.ValueDecimal), nullableString(fact.ValueText),
		nullableString(fact.DecimalsAttr), nullableString(fact.PrecisionAttr), fact.IsNil, rawHash,
		fact.RawContextID, fact.Form, fact.FiledAt, fact.AcceptedAt, fact.AvailableAt, utcNow())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func rawFactHash(f rawFact) string {
	return sha256Hex([]byte(mustCanonicalJSON(map[string]any{
		"security_id": f.SecurityID, "concept_qname": f.ConceptQName, "context_id": f.ContextID,
		"unit_id": nullableString(f.UnitID), "value_decimal": nullableFloat(f.ValueDecimal),
		"value_text": nullableString(f.ValueText), "accession_number": f.Accession,
	})))
}

func insertCanonicalObservation(tx execer, fact rawFact) (bool, error) {
	candidate, ok := conceptCandidates[fact.ConceptQName]
	if !ok || !fact.ValueDecimal.Valid || fact.DimensionalScope != "consolidated_total" {
		return false, nil
	}
	unit := ""
	if fact.Unit != nil {
		unit = fact.Unit.Signature
	}
	if unit == "" {
		unit = candidate.unit
	}
	pid := periodIDFor(fact.CIK, fact.Accession, fact.Context.Period)
	factID := stableID("fact", map[string]any{
		"security_id": fact.SecurityID, "taxonomy": fact.Taxonomy, "concept": fact.ConceptQName,
		"unit": nullableString(fact.UnitID), "value_decimal": nullableFloat(fact.ValueDecimal),
		"value_text": nullableString(fact.ValueText), "accession_number": fact.Accession,
		"period_id": pid, "context_id": fact.ContextID, "raw_context_id": fact.RawContextID,
		"dimensions_hash": fact.DimensionsHash, "form": fact.Form,
	})
	obsID := stableID("obs", map[string]any{
		"resolver_version": resolverVersion, "security_id": fact.SecurityID, "metric_id": candidate.metricID,
		"basis_id": candidate.basisID, "period_id": pid, "dimensions_hash": fact.DimensionsHash,
		"source_fact_id": factID, "observation_status": "selected",
	})
	obsHash := strings.TrimPrefix(obsID, "obs-")
	flags := 0
	if fact.DimensionsHash != totalDimensionsHash {
		flags |= qualityDimensionalFact
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO canonical_observations(
observation_id, observation_hash, resolver_version, security_id, cik, metric_id, metric_name, metric_kind,
basis_id, period_id, period_semantics, duration_days, fiscal_year, fiscal_period, value_decimal,
unit_signature, dimensions_hash, dimensional_scope, observation_status, source_fact_id, source_accession,
taxonomy, concept_qname, selection_reason, quality_flags, quality_flags_json, quality_score,
accepted_at, available_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'selected', ?, ?, ?, ?, ?, ?, '[]', 1.0, ?, ?, ?)`,
		obsID, obsHash, resolverVersion, fact.SecurityID, fact.CIK, candidate.metricID, metricNames[candidate.metricID], metricKinds[candidate.metricID],
		candidate.basisID, pid, fact.Context.Period.PeriodSemantics, fact.Context.Period.DurationDays,
		nullableInt(fact.Context.Period.FiscalYear), nullableString(fact.Context.Period.FiscalPeriod), fact.ValueDecimal.Float64,
		unit, fact.DimensionsHash, fact.DimensionalScope, factID, fact.Accession, fact.Taxonomy, fact.ConceptQName,
		fmt.Sprintf("priority=%d total-company xbrl fact", candidate.priority), flags, fact.AcceptedAt, fact.AvailableAt, utcNow())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
