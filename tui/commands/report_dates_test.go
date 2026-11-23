package commands

import (
	"testing"
	"time"
)

func TestReportDateHelpers(t *testing.T) {
	loc := time.FixedZone("test", 9*60*60)
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
	now := time.Date(2026, time.November, 23, 15, 4, 5, 900_000_000, loc)
	for _, tc := range []struct{ months, want string }{{"0", "2026-11-01 15:04:05"}, {"1", "2026-10-01 15:04:05"}, {"3", "2026-08-01 15:04:05"}} {
		got, err := mtxSince(now, tc.months)
		if err != nil || got.Format("2006-01-02 15:04:05") != tc.want {
			t.Fatalf("mtxSince(%s) = %v, %v", tc.months, got, err)
		}
	}
	if got, err := dtxSince("20240229"); err != nil || got.Location() != loc || got.Format("2006-01-02 15:04:05") != "2024-02-29 00:00:00" {
		t.Fatalf("dtxSince() = %v, %v", got, err)
	}
	for _, invalid := range []string{"20240230", "20241301"} {
		if _, err := dtxSince(invalid); err == nil {
			t.Errorf("dtxSince(%q) accepted", invalid)
		}
	}
}
