package report

import (
	"testing"
	"time"
)

var today = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func TestFindRecurring_MonthlySubscription(t *testing.T) {
	// Дата списания гуляет на пару дней, как у настоящих подписок.
	var txs []Transaction
	for _, date := range []string{"2026-05-10", "2026-06-11", "2026-07-10", "2026-08-12", "2026-09-10", "2026-10-10"} {
		txs = append(txs, expense(date, 299, "RUB", "Подписки", "", "Яндекс Плюс"))
	}

	got := FindRecurring(txs, 3, today)
	if len(got) != 1 {
		t.Fatalf("FindRecurring() = %+v, want одну подписку", got)
	}

	r := got[0]
	if r.Payee != "Яндекс Плюс" || r.Amount != 299 || r.Period != "monthly" || r.Count != 6 ||
		r.LastDate != "2026-10-10" || !r.Active {
		t.Errorf("подписка = %+v", r)
	}
	if r.NextDate < "2026-11-08" || r.NextDate > "2026-11-11" {
		t.Errorf("NextDate = %s, want около 2026-11-10", r.NextDate)
	}
}

// Две подписки одного мерчанта по разной цене — две серии, а не одна с большим разбросом.
func TestFindRecurring_SplitsByAmount(t *testing.T) {
	var txs []Transaction
	for _, date := range []string{"2026-07-05", "2026-08-05", "2026-09-05", "2026-10-05"} {
		txs = append(txs,
			expense(date, 169, "RUB", "Подписки", "", "Apple"),
			expense(date, 899, "RUB", "Подписки", "", "APPLE"),
		)
	}

	got := FindRecurring(txs, 3, today)
	if len(got) != 2 || got[0].Amount != 899 || got[1].Amount != 169 {
		t.Errorf("FindRecurring() = %+v, want две подписки, дорогая первой", got)
	}
}

func TestFindRecurring_IgnoresIrregular(t *testing.T) {
	tests := []struct {
		name string
		txs  []Transaction
	}{
		{"разные суммы в одном магазине", []Transaction{
			expense("2026-09-01", 1200, "RUB", "Еда", "", "Пятёрочка"),
			expense("2026-09-08", 450, "RUB", "Еда", "", "Пятёрочка"),
			expense("2026-09-15", 3100, "RUB", "Еда", "", "Пятёрочка"),
			expense("2026-09-22", 800, "RUB", "Еда", "", "Пятёрочка"),
		}},
		{"кофе через день", []Transaction{
			expense("2026-10-01", 250, "RUB", "Кафе", "", "Кофейня"),
			expense("2026-10-03", 250, "RUB", "Кафе", "", "Кофейня"),
			expense("2026-10-04", 250, "RUB", "Кафе", "", "Кофейня"),
			expense("2026-10-06", 250, "RUB", "Кафе", "", "Кофейня"),
		}},
		{"случайные интервалы", []Transaction{
			expense("2026-03-01", 500, "RUB", "", "", "Такси"),
			expense("2026-03-20", 500, "RUB", "", "", "Такси"),
			expense("2026-06-02", 500, "RUB", "", "", "Такси"),
			expense("2026-06-12", 500, "RUB", "", "", "Такси"),
		}},
		{"без получателя", []Transaction{
			expense("2026-07-05", 300, "RUB", "Связь", "", ""),
			expense("2026-08-05", 300, "RUB", "Связь", "", ""),
			expense("2026-09-05", 300, "RUB", "Связь", "", ""),
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FindRecurring(tt.txs, 3, today); len(got) != 0 {
				t.Errorf("FindRecurring() = %+v, want ничего", got)
			}
		})
	}
}

// Отменённая подписка помечается недействующей и идёт после действующих, даже если дороже.
func TestFindRecurring_InactiveGoesLast(t *testing.T) {
	var txs []Transaction
	for _, date := range []string{"2026-01-03", "2026-02-03", "2026-03-03", "2026-04-03"} {
		txs = append(txs, expense(date, 999, "RUB", "Подписки", "", "Кинопоиск"))
	}
	for _, date := range []string{"2026-08-01", "2026-09-01", "2026-10-01"} {
		txs = append(txs, expense(date, 199, "RUB", "Подписки", "", "VPN"))
	}

	got := FindRecurring(txs, 3, today)
	if len(got) != 2 || got[0].Payee != "VPN" || !got[0].Active || got[1].Payee != "Кинопоиск" || got[1].Active {
		t.Errorf("FindRecurring() = %+v", got)
	}
}

// Суммы в разных валютах не сравниваются: рубли идут первыми, хотя 15 < 299.
func TestFindRecurring_GroupsByCurrency(t *testing.T) {
	var txs []Transaction
	for _, date := range []string{"2026-07-05", "2026-08-05", "2026-09-05", "2026-10-05"} {
		txs = append(txs,
			expense(date, 15, "USD", "Подписки", "", "Netflix"),
			expense(date, 299, "RUB", "Подписки", "", "Яндекс Плюс"),
			expense(date, 99, "RUB", "Подписки", "", "VPN"),
		)
	}

	got := FindRecurring(txs, 3, today)
	if len(got) != 3 || got[0].Payee != "Яндекс Плюс" || got[1].Payee != "VPN" || got[2].Payee != "Netflix" {
		t.Errorf("FindRecurring() = %+v, want RUB по стоимости, затем USD", got)
	}
}
