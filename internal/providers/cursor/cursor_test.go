package cursor

import (
	"encoding/base64"
	"testing"
)

func TestSummaryIncludedFirstAndZeroIsAReading(t *testing.T) {
	ws, note, plan, err := parseSummary([]byte(`{
		"billingCycleEnd":"2026-10-01T00:00:00Z","membershipType":"pro",
		"individualUsage":{"plan":{"totalPercentUsed":0,"apiPercentUsed":12},"onDemand":{"enabled":true,"used":5,"limit":20}}
	}`))
	if err != nil || note != "" || plan != "Pro" {
		t.Fatalf("err=%v note=%q plan=%q", err, note, plan)
	}
	if len(ws) != 3 || ws[0].ID != "included" || ws[0].Used != 0 || ws[1].ID != "api" || ws[2].Used != 0.25 {
		t.Fatalf("got %+v", ws)
	}
	if ws[0].ResetsAt.Month() != 10 {
		t.Errorf("reset = %v", ws[0].ResetsAt)
	}
}

func TestSummaryWithNothingToMeterSaysWhy(t *testing.T) {
	ws, note, _, err := parseSummary([]byte(`{"membershipType":"enterprise","isUnlimited":true,"individualUsage":{}}`))
	if err != nil || len(ws) != 0 || note != "Unlimited on the enterprise plan — nothing to meter" {
		t.Fatalf("ws=%v note=%q err=%v", ws, note, err)
	}
}

func TestUserIDComesFromTheSubClaim(t *testing.T) {
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"google-oauth2|user_01ABC"}`))
	if got := userIDFromToken("h." + claims + ".s"); got != "user_01ABC" {
		t.Fatalf("got %q", got)
	}
	if got := userIDFromToken("garbage"); got != "" {
		t.Fatalf("got %q", got)
	}
}
