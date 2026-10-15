// Package report считает аналитику по операциям: фильтры, сводки трат, сравнение периодов
// и регулярные списания. Про базу и MCP не знает: на вход идут операции с готовыми названиями.
package report

import (
	"cmp"
	"math"
	"strings"
)

type Kind string

const (
	Expense  Kind = "expense"
	Income   Kind = "income"
	Transfer Kind = "transfer"
)

// noCategory подставляется в группировки вместо пустой категории.
const noCategory = "Без категории"

// noPayee подставляется в группировку по мерчантам вместо пустого получателя.
const noPayee = "Без получателя"

// mainCurrency идёт первой в ответах, остальные валюты — по алфавиту.
const mainCurrency = "RUB"

// Transaction — операция для отчётов. Суммы неотрицательные, в валюте своего счёта; обе нулевыми не бывают.
type Transaction struct {
	Date            string
	Income          float64
	IncomeCurrency  string
	IncomeAccount   string
	Outcome         float64
	OutcomeCurrency string
	OutcomeAccount  string
	// Category — основная категория; пустая, если категории нет.
	Category string
	// ParentCategory — родитель Category; пустой у категорий верхнего уровня.
	ParentCategory string
	// Payee — название мерчанта, а если его нет, получатель из выписки.
	Payee string
	// RawPayee — получатель из выписки, даже если операции назначен мерчант.
	RawPayee string
	Comment  string
}

// Kind определяет тип по суммам. Если ненулевые обе, это перевод между своими счетами (или в долг):
// ни в траты, ни в доходы он не входит. Счета сравнивать не нужно: у расхода они совпадают.
func (t Transaction) Kind() Kind {
	switch {
	case t.Income > 0 && t.Outcome > 0:
		return Transfer
	case t.Income > 0:
		return Income
	default:
		return Expense
	}
}

// Amount — сумма и валюта: у дохода сторона зачисления, у расхода и перевода сторона списания.
func (t Transaction) Amount() (float64, string) {
	if t.Kind() == Income {
		return t.Income, t.IncomeCurrency
	}
	return t.Outcome, t.OutcomeCurrency
}

// TopCategory — категория верхнего уровня: подкатегория сворачивается в родителя.
func (t Transaction) TopCategory() string {
	return cmp.Or(t.ParentCategory, t.Category, noCategory)
}

// CategoryPath — полное имя категории вида «Еда / Кафе»; пустое без категории.
func (t Transaction) CategoryPath() string {
	if t.ParentCategory == "" {
		return t.Category
	}
	return t.ParentCategory + " / " + t.Category
}

// Filter отбирает операции. Пустые поля ничего не ограничивают.
type Filter struct {
	Kind Kind
	// Category — подстрока без учёта регистра; ищется и в родителе, поэтому родитель включает подкатегории.
	Category string
	// Payee ищется подстрокой без учёта регистра в мерчанте и в получателе из выписки.
	Payee     string
	MinAmount float64
	MaxAmount float64
}

// Apply возвращает подходящие под фильтр операции в исходном порядке.
func (f Filter) Apply(txs []Transaction) []Transaction {
	var out []Transaction
	for _, t := range txs {
		if f.match(t) {
			out = append(out, t)
		}
	}
	return out
}

func (f Filter) match(t Transaction) bool {
	amount, _ := t.Amount()

	switch {
	case f.Kind != "" && t.Kind() != f.Kind:
		return false
	case f.MinAmount > 0 && amount < f.MinAmount:
		return false
	case f.MaxAmount > 0 && amount > f.MaxAmount:
		return false
	case f.Category != "" && !containsFold(t.Category, f.Category) && !containsFold(t.ParentCategory, f.Category):
		return false
	case f.Payee != "" && !containsFold(t.Payee, f.Payee) && !containsFold(t.RawPayee, f.Payee):
		return false
	}

	return true
}

// containsFold регистронезависим в Go, а не в SQL: LOWER в SQLite без ICU не понижает кириллицу.
func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func currencyOrder(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == mainCurrency:
		return -1
	case b == mainCurrency:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

func round1(x float64) float64 { return math.Round(x*10) / 10 }
