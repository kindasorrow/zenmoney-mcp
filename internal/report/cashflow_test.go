package report

import "testing"

// Переводы не входят ни в доход, ни в расход; валюты и месяцы считаются отдельно.
func TestCashFlow(t *testing.T) {
	txs := []Transaction{
		{Date: "2026-09-05", Income: 100000, IncomeCurrency: "RUB"},
		expense("2026-09-10", 60000, "RUB", "", "", ""),
		expense("2026-10-01", 30000, "RUB", "", "", ""),
		{Date: "2026-10-02", Income: 50000, IncomeCurrency: "RUB", Outcome: 50000, OutcomeCurrency: "RUB"},
		expense("2026-10-03", 15, "USD", "", "", ""),
	}

	got := CashFlow(txs, ByMonth)
	if len(got) != 2 || got[0].Currency != "RUB" || got[1].Currency != "USD" {
		t.Fatalf("CashFlow() валюты = %s", toJSON(got))
	}

	rub := got[0]
	wantPeriods := []FlowPeriod{
		{Key: "2026-09", Income: 100000, Expense: 60000, Net: 40000},
		{Key: "2026-10", Income: 0, Expense: 30000, Net: -30000},
	}
	if rub.Income != 100000 || rub.Expense != 90000 || rub.Net != 10000 || rub.SavingsRate == nil || *rub.SavingsRate != 10 {
		t.Errorf("итог RUB = %s", toJSON(rub))
	}
	if toJSON(rub.Periods) != toJSON(wantPeriods) {
		t.Errorf("периоды RUB = %s, want %s", toJSON(rub.Periods), toJSON(wantPeriods))
	}

	if usd := got[1]; usd.Expense != 15 || usd.Net != -15 || usd.SavingsRate != nil {
		t.Errorf("USD без дохода = %s", toJSON(usd))
	}
}
