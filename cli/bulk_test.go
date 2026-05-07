package main

import "testing"

func TestFilingIngestGroupsPreferAnnualAndQuarterlyHistory(t *testing.T) {
	groups := filingIngestGroups(defaultFundamentalForms, 1, 4, 1)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d: %#v", len(groups), groups)
	}
	if groups[0].Name != "annual" || groups[0].Forms != "10-K,10-K/A" || groups[0].Limit != 1 {
		t.Fatalf("unexpected annual group: %#v", groups[0])
	}
	if groups[1].Name != "quarterly" || groups[1].Forms != "10-Q,10-Q/A" || groups[1].Limit != 4 {
		t.Fatalf("unexpected quarterly group: %#v", groups[1])
	}
}

func TestFilingIngestGroupsFallbackForOtherForms(t *testing.T) {
	groups := filingIngestGroups("8-K,10-Q", 1, 2, 3)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d: %#v", len(groups), groups)
	}
	if groups[0].Name != "quarterly" || groups[0].Forms != "10-Q" || groups[0].Limit != 2 {
		t.Fatalf("unexpected quarterly group: %#v", groups[0])
	}
	if groups[1].Name != "other" || groups[1].Forms != "8-K" || groups[1].Limit != 3 {
		t.Fatalf("unexpected fallback group: %#v", groups[1])
	}
}
