package report

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Переводы и доходы не считаются тратами, подкатегории сворачиваются в родителя, валюты не смешиваются.
func TestSummarize_ByCategory(t *testing.T) {
	txs := []Transaction{
		expense("2026-10-01", 300, "RUB", "Кафе", "Еда", "Кофейня"),
		expense("2026-10-02", 700, "RUB", "Еда", "", "Пятёрочка"),
		expense("2026-10-03", 1000, "RUB", "", "", ""),
		expense("2026-10-04", 20, "USD", "Подписки", "", "Netflix"),
		{Date: "2026-10-05", Income: 5000, IncomeCurrency: "RUB", Outcome: 5000, OutcomeCurrency: "RUB"},
		{Date: "2026-10-06", Income: 100000, IncomeCurrency: "RUB", Category: "Зарплата"},
	}

	got := Summarize(txs, ByCategory, 0)
	want := []CurrencySummary{
		{Currency: "RUB", Total: 2000, Count: 3, Groups: []Group{
			{Key: "Без категории", Sum: 1000, Count: 1, Share: 50},
			{Key: "Еда", Sum: 1000, Count: 2, Share: 50},
		}},
		{Currency: "USD", Total: 20, Count: 1, Groups: []Group{
			{Key: "Подписки", Sum: 20, Count: 1, Share: 100},
		}},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Summarize() =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSummarize_BySubcategory(t *testing.T) {
	txs := []Transaction{
		expense("2026-10-01", 300, "RUB", "Кафе", "Еда", ""),
		expense("2026-10-02", 700, "RUB", "Еда", "", ""),
	}

	got := Summarize(txs, BySubcategory, 0)[0].Groups
	want := []Group{
		{Key: "Еда", Sum: 700, Count: 1, Share: 70},
		{Key: "Еда / Кафе", Sum: 300, Count: 1, Share: 30},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Groups = %+v, want %+v", got, want)
	}
}

// limit режет мелкие группы, но итог и доли считаются по всем.
func TestSummarize_LimitKeepsLargest(t *testing.T) {
	txs := []Transaction{
		expense("2026-10-01", 100, "RUB", "", "", "Аптека"),
		expense("2026-10-02", 300, "RUB", "", "", "Кофейня"),
		expense("2026-10-03", 600, "RUB", "", "", "Пятёрочка"),
	}

	got := Summarize(txs, ByMerchant, 2)[0]

	if got.Total != 1000 || got.Omitted != 1 || len(got.Groups) != 2 {
		t.Fatalf("Summarize() = %+v", got)
	}
	if got.Groups[0].Key != "Пятёрочка" || got.Groups[0].Share != 60 || got.Groups[1].Key != "Кофейня" {
		t.Errorf("Groups = %+v", got.Groups)
	}
}

// Неделя начинается с понедельника; периоды идут по порядку и limit их не режет.
func TestSummarize_ByWeek(t *testing.T) {
	txs := []Transaction{
		expense("2026-10-19", 50, "RUB", "", "", ""),
		expense("2026-10-12", 100, "RUB", "", "", ""),
		expense("2026-10-18", 200, "RUB", "", "", ""),
		expense("2026-10-05", 400, "RUB", "", "", ""),
	}

	got := Summarize(txs, ByWeek, 1)[0].Groups
	want := []Group{
		{Key: "2026-10-05", Sum: 400, Count: 1, Share: 53.3},
		{Key: "2026-10-12", Sum: 300, Count: 2, Share: 40},
		{Key: "2026-10-19", Sum: 50, Count: 1, Share: 6.7},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Groups = %+v, want %+v", got, want)
	}
}

func TestSummarize_Empty(t *testing.T) {
	if got := Summarize(nil, ByMonth, 0); got == nil || len(got) != 0 {
		t.Errorf("Summarize(nil) = %#v, want пустой срез", got)
	}
}

func TestCompare(t *testing.T) {
	a := []Transaction{
		expense("2026-09-01", 1000, "RUB", "Еда", "", ""),
		expense("2026-09-02", 200, "RUB", "Такси", "Транспорт", ""),
		{Date: "2026-09-03", Income: 3000, IncomeCurrency: "RUB", Outcome: 3000, OutcomeCurrency: "RUB"},
	}
	b := []Transaction{
		expense("2026-10-01", 1000, "RUB", "Еда", "", ""),
		expense("2026-10-02", 500, "RUB", "Кафе", "Еда", ""),
		expense("2026-10-03", 300, "RUB", "Подарки", "", ""),
		expense("2026-10-04", 10, "USD", "Подписки", "", ""),
	}

	got := Compare(a, b)
	if len(got) != 2 || got[0].Currency != "RUB" || got[1].Currency != "USD" {
		t.Fatalf("Compare() валюты = %+v", got)
	}

	rub := got[0]
	if rub.TotalA != 1200 || rub.TotalB != 1800 || rub.Diff != 600 || *rub.DiffPct != 50 {
		t.Errorf("итог RUB = %+v", rub)
	}

	wantRows := []CompareRow{
		{Category: "Еда", A: 1000, B: 1500, Diff: 500, DiffPct: new(50.0)},
		{Category: "Подарки", A: 0, B: 300, Diff: 300},
		{Category: "Транспорт", A: 200, B: 0, Diff: -200, DiffPct: new(-100.0)},
	}
	if !reflect.DeepEqual(rub.Rows, wantRows) {
		t.Errorf("Rows = %s, want %s", toJSON(rub.Rows), toJSON(wantRows))
	}

	if usd := got[1]; usd.TotalA != 0 || usd.TotalB != 10 || usd.DiffPct != nil {
		t.Errorf("итог USD = %+v", usd)
	}
}

// toJSON печатает значения указателей, а не адреса.
func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
