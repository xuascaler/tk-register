package billing

import "testing"

func TestDailyGroupsModelsWithExactCountersAndMoney(t *testing.T) {
	a := bill("one@example.com", "15001", "0.10000001")
	a[3] = "9007199254740993"
	b := bill("one@example.com", "15001", "0.2")
	b[3] = "7"
	c := bill("one@example.com", "15001", "5.25")
	c[2] = "claude-other"
	rows, err := Daily(source(t, a, b, c, bill("outsider@example.com", "999", "1000")), []Account{{LocalAccountID: "1", IdentityKey: "first", Account: "one@example.com", PlatformAccountID: "15001"}, {LocalAccountID: "2", IdentityKey: "second", Account: "no-bills@example.com", PlatformAccountID: "15002"}, {LocalAccountID: "3", IdentityKey: "third", Account: "missing-id@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || len(rows[0].Rows) != 2 || rows[0].Rows[1].InputTokens != "9007199254741000" || rows[0].Rows[1].CostCNY != "0.30000001" || rows[0].Rows[1].CacheWriteTokens != "80" {
		t.Fatalf("incorrect aggregation %+v", rows)
	}
	if rows[1].Error != "" || len(rows[1].Rows) != 0 || rows[2].Error == "" {
		t.Fatal("missing identity was treated as a zero day", rows)
	}
}
func TestDailyRejectsIdentityConflicts(t *testing.T) {
	rows, err := Daily(source(t, bill("different@example.com", "15001", "9")), []Account{{LocalAccountID: "1", Account: "one@example.com", PlatformAccountID: "15001"}})
	if err != nil || rows[0].Error == "" || len(rows[0].Rows) != 0 {
		t.Fatal(rows, err)
	}
	if _, err = Daily([]byte("not a workbook"), nil); err == nil {
		t.Fatal("invalid export accepted")
	}
}
