package report

import (
	"maps"
	"slices"
)

// FlowPeriod — доходы и расходы одной валюты за период; Net = Income − Expense.
type FlowPeriod struct {
	Key     string  `json:"key"`
	Income  float64 `json:"income"`
	Expense float64 `json:"expense"`
	Net     float64 `json:"net"`
}

// CurrencyFlow — денежный поток в одной валюте. SavingsRate — доля дохода, не ушедшая в расходы,
// в процентах; пуста, если дохода не было.
type CurrencyFlow struct {
	Currency    string       `json:"currency"`
	Income      float64      `json:"income"`
	Expense     float64      `json:"expense"`
	Net         float64      `json:"net"`
	SavingsRate *float64     `json:"savings_rate,omitempty"`
	Periods     []FlowPeriod `json:"periods"`
}

// CashFlow сводит доходы и расходы по валютам и периодам by; переводы между своими счетами пропускаются.
func CashFlow(txs []Transaction, by GroupBy) []CurrencyFlow {
	byCurrency := map[string]map[string]*FlowPeriod{}

	for _, t := range txs {
		kind := t.Kind()
		if kind == Transfer {
			continue
		}

		amount, currency := t.Amount()
		p := cell(byCurrency, currency, by.key(t))
		if kind == Income {
			p.Income += amount
		} else {
			p.Expense += amount
		}
	}

	out := []CurrencyFlow{}
	for currency, periods := range byCurrency {
		out = append(out, currencyFlow(currency, periods))
	}
	slices.SortFunc(out, func(a, b CurrencyFlow) int { return currencyOrder(a.Currency, b.Currency) })

	return out
}

func currencyFlow(currency string, periods map[string]*FlowPeriod) CurrencyFlow {
	f := CurrencyFlow{Currency: currency}

	var income, expense float64
	for _, key := range slices.Sorted(maps.Keys(periods)) {
		p := periods[key]
		income += p.Income
		expense += p.Expense
		f.Periods = append(f.Periods, FlowPeriod{
			Key:     key,
			Income:  round2(p.Income),
			Expense: round2(p.Expense),
			Net:     round2(p.Income - p.Expense),
		})
	}

	f.Income, f.Expense, f.Net = round2(income), round2(expense), round2(income-expense)
	if income > 0 {
		f.SavingsRate = new(share(income-expense, income))
	}

	return f
}
