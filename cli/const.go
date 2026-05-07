package main

const (
	abiVersion      = 2
	reasonBytes     = 128
	diagnosticBytes = 1024

	metricRevenue           = 1
	metricNetIncome         = 2
	metricEPSDiluted        = 3
	metricDilutedShares     = 4
	metricOperatingCashFlow = 5
	metricCapex             = 6
	metricCash              = 7
	metricDebt              = 8

	basisRevenueExcludingTax = 101
	basisRevenueIncludingTax = 102
	basisRevenueSalesNet     = 103
	basisRevenueGeneric      = 104
	basisNetIncome           = 201
	basisEPSDiluted          = 301
	basisDilutedShares       = 401
	basisOperatingCashFlow   = 501
	basisCapex               = 601
	basisCash                = 701
	basisDebt                = 801

	qualityDimensionalFact      = 1 << 16
	stmtMissingRevenue          = 1 << 0
	stmtMissingShares           = 1 << 1
	stmtMissingCash             = 1 << 2
	stmtMissingDebt             = 1 << 3
	stmtNegativeFCF             = 1 << 4
	stmtLowConfidence           = 1 << 7
	resolverVersion             = "canonical_resolver_v3"
	totalDimensionsJSON         = "{}"
	defaultUserAgent            = "sec-fa operator@example.invalid"
	secArchiveBaseURL           = "https://www.sec.gov/Archives/edgar/data/%d/%s"
	defaultScenarioAssumptionID = "default"
)

var (
	totalDimensionsHash = sha256Hex([]byte(totalDimensionsJSON))

	metricNames = map[int]string{
		metricRevenue:           "revenue",
		metricNetIncome:         "net_income",
		metricEPSDiluted:        "eps_diluted",
		metricDilutedShares:     "diluted_shares",
		metricOperatingCashFlow: "operating_cash_flow",
		metricCapex:             "capex",
		metricCash:              "cash",
		metricDebt:              "debt",
	}

	metricKinds = map[int]string{
		metricRevenue:           "flow",
		metricNetIncome:         "flow",
		metricEPSDiluted:        "per_share_flow",
		metricDilutedShares:     "flow",
		metricOperatingCashFlow: "flow",
		metricCapex:             "flow",
		metricCash:              "instant",
		metricDebt:              "instant",
	}

	metricKindCodes = map[string]uint32{
		"flow":           1,
		"instant":        2,
		"per_share_flow": 3,
		"ratio":          4,
		"derived":        5,
	}

	periodCodes = map[string]uint32{
		"fiscal_quarter":        1,
		"fiscal_ytd":            2,
		"fiscal_year":           3,
		"trailing_twelve_month": 4,
		"instant":               5,
		"stub":                  6,
		"transition":            7,
		"irregular":             8,
	}

	basisByMetric = map[int]int{
		metricRevenue:           basisRevenueExcludingTax,
		metricNetIncome:         basisNetIncome,
		metricEPSDiluted:        basisEPSDiluted,
		metricDilutedShares:     basisDilutedShares,
		metricOperatingCashFlow: basisOperatingCashFlow,
		metricCapex:             basisCapex,
		metricCash:              basisCash,
		metricDebt:              basisDebt,
	}

	conceptCandidates = map[string]canonicalConcept{
		"us-gaap:RevenueFromContractWithCustomerExcludingAssessedTax":           {metricRevenue, basisRevenueExcludingTax, 1, "USD"},
		"us-gaap:RevenueFromContractWithCustomerIncludingAssessedTax":           {metricRevenue, basisRevenueIncludingTax, 2, "USD"},
		"us-gaap:SalesRevenueNet":                                               {metricRevenue, basisRevenueSalesNet, 3, "USD"},
		"us-gaap:Revenues":                                                      {metricRevenue, basisRevenueGeneric, 4, "USD"},
		"us-gaap:NetIncomeLoss":                                                 {metricNetIncome, basisNetIncome, 1, "USD"},
		"us-gaap:ProfitLoss":                                                    {metricNetIncome, basisNetIncome, 2, "USD"},
		"us-gaap:EarningsPerShareDiluted":                                       {metricEPSDiluted, basisEPSDiluted, 1, "USD/shares"},
		"us-gaap:WeightedAverageNumberOfDilutedSharesOutstanding":               {metricDilutedShares, basisDilutedShares, 1, "shares"},
		"us-gaap:NetCashProvidedByUsedInOperatingActivities":                    {metricOperatingCashFlow, basisOperatingCashFlow, 1, "USD"},
		"us-gaap:PaymentsToAcquirePropertyPlantAndEquipment":                    {metricCapex, basisCapex, 1, "USD"},
		"us-gaap:CashAndCashEquivalentsAtCarryingValue":                         {metricCash, basisCash, 1, "USD"},
		"us-gaap:CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents": {metricCash, basisCash, 2, "USD"},
		"us-gaap:LongTermDebtAndFinanceLeaseObligationsCurrent":                 {metricDebt, basisDebt, 1, "USD"},
		"us-gaap:LongTermDebtCurrent":                                           {metricDebt, basisDebt, 2, "USD"},
		"us-gaap:LongTermDebtNoncurrent":                                        {metricDebt, basisDebt, 3, "USD"},
	}
)

type canonicalConcept struct {
	metricID int
	basisID  int
	priority int
	unit     string
}
