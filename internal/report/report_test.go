package report

import (
	"reflect"
	"testing"
)

func expense(date string, amount float64, currency, category, parent, payee string) Transaction {
	return Transaction{
		Date: date, Outcome: amount, OutcomeCurrency: currency, OutcomeAccount: "Карта",
		Category: category, ParentCategory: parent, Payee: payee,
	}
}

func TestKind(t *testing.T) {
	tests := []struct {
		name            string
		income, outcome float64
		want            Kind
	}{
		{"расход", 0, 300, Expense},
		{"доход", 5000, 0, Income},
		{"перевод между счетами", 1000, 1000, Transfer},
		{"обмен валюты", 10, 950, Transfer},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (Transaction{Income: tt.income, Outcome: tt.outcome}).Kind(); got != tt.want {
				t.Errorf("Kind() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestFilter(t *testing.T) {
	cafe := expense("2026-10-01", 300, "RUB", "Кафе", "Еда", "Кофейня")
	food := expense("2026-10-02", 2000, "RUB", "Еда", "", "Пятёрочка")
	taxi := expense("2026-10-03", 700, "RUB", "Такси", "Транспорт", "Яндекс Go")
	salary := Transaction{Date: "2026-10-05", Income: 100000, IncomeCurrency: "RUB", Category: "Зарплата"}
	all := []Transaction{cafe, food, taxi, salary}

	tests := []struct {
		name   string
		filter Filter
		want   []Transaction
	}{
		{"родитель включает подкатегории", Filter{Category: "еда"}, []Transaction{cafe, food}},
		{"подкатегория не тянет родителя", Filter{Category: "кафе"}, []Transaction{cafe}},
		{"получатель без учёта регистра", Filter{Payee: "ЯНДЕКС"}, []Transaction{taxi}},
		{"по типу", Filter{Kind: Income}, []Transaction{salary}},
		{"по диапазону суммы", Filter{MinAmount: 500, MaxAmount: 2000}, []Transaction{food, taxi}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.filter.Apply(all); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Apply() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Мерчант не прячет текст выписки: операцию «Кофейни» находит и «coffee».
func TestFilter_PayeeMatchesRawPayee(t *testing.T) {
	coffee := expense("2026-10-01", 300, "RUB", "Кафе", "", "Кофейня")
	coffee.RawPayee = "COFFEE 123"

	if got := (Filter{Payee: "coffee"}).Apply([]Transaction{coffee}); len(got) != 1 {
		t.Errorf("Apply() = %+v, want операцию по тексту выписки", got)
	}
}
