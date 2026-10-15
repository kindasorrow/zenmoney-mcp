package report

import (
	"cmp"
	"maps"
	"math"
	"slices"
	"strings"
	"time"
)

// GroupBy — разрез сводки трат.
type GroupBy string

const (
	ByCategory    GroupBy = "category"
	BySubcategory GroupBy = "subcategory"
	ByMerchant    GroupBy = "merchant"
	ByDay         GroupBy = "day"
	ByWeek        GroupBy = "week"
	ByMonth       GroupBy = "month"
)

func (g GroupBy) Valid() bool {
	switch g {
	case ByCategory, BySubcategory, ByMerchant, ByDay, ByWeek, ByMonth:
		return true
	}
	return false
}

func (g GroupBy) isPeriod() bool {
	return g == ByDay || g == ByWeek || g == ByMonth
}

func (g GroupBy) key(t Transaction) string {
	switch g {
	case BySubcategory:
		return cmp.Or(t.CategoryPath(), noCategory)
	case ByMerchant:
		return cmp.Or(t.Payee, noPayee)
	case ByDay:
		return t.Date
	case ByWeek:
		return reformatDate(t.Date, weekStart)
	case ByMonth:
		return reformatDate(t.Date, func(d time.Time) string { return d.Format("2006-01") })
	default:
		return t.TopCategory()
	}
}

// weekStart — понедельник недели, к которой относится дата.
func weekStart(d time.Time) string {
	offset := (int(d.Weekday()) + 6) % 7
	return d.AddDate(0, 0, -offset).Format(time.DateOnly)
}

// reformatDate переводит дату в ключ периода; неразборчивую оставляет как есть, чтобы не терять трату.
func reformatDate(date string, format func(time.Time) string) string {
	d, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return format(d)
}

// Group — строка сводки. Share — доля от итога валюты в процентах.
type Group struct {
	Key   string  `json:"key"`
	Sum   float64 `json:"sum"`
	Count int     `json:"count"`
	Share float64 `json:"share"`
}

// CurrencySummary — сводка в одной валюте. Omitted — групп, не вошедших из-за limit; Total их учитывает.
type CurrencySummary struct {
	Currency string  `json:"currency"`
	Total    float64 `json:"total"`
	Count    int     `json:"count"`
	Groups   []Group `json:"groups"`
	Omitted  int     `json:"omitted,omitempty"`
}

// Summarize сводит расходы по валютам и группам, не смешивая валюты; доходы и переводы пропускаются.
// limit режет только категории и мерчантов: у периодов обрезка по сумме убрала бы дни из середины диапазона.
func Summarize(txs []Transaction, by GroupBy, limit int) []CurrencySummary {
	out := []CurrencySummary{}
	for currency, buckets := range expenseBuckets(txs, by.key) {
		out = append(out, summarizeCurrency(currency, buckets, by, limit))
	}
	slices.SortFunc(out, func(a, b CurrencySummary) int { return currencyOrder(a.Currency, b.Currency) })

	return out
}

func summarizeCurrency(currency string, buckets map[string]*bucket, by GroupBy, limit int) CurrencySummary {
	s := CurrencySummary{Currency: currency}

	var total float64
	for _, b := range buckets {
		total += b.sum
		s.Count += b.count
	}
	s.Total = round2(total)

	for key, b := range buckets {
		s.Groups = append(s.Groups, Group{Key: key, Sum: round2(b.sum), Count: b.count, Share: share(b.sum, total)})
	}

	if by.isPeriod() {
		slices.SortFunc(s.Groups, func(a, b Group) int { return strings.Compare(a.Key, b.Key) })
		return s
	}

	slices.SortFunc(s.Groups, func(a, b Group) int {
		return cmp.Or(cmp.Compare(b.Sum, a.Sum), strings.Compare(a.Key, b.Key))
	})
	if limit > 0 && len(s.Groups) > limit {
		s.Omitted = len(s.Groups) - limit
		s.Groups = s.Groups[:limit]
	}

	return s
}

// CompareRow — категория в двух периодах, Diff = B − A. DiffPct пуст, если в A было 0: от нуля процент не считается.
type CompareRow struct {
	Category string   `json:"category"`
	A        float64  `json:"a"`
	B        float64  `json:"b"`
	Diff     float64  `json:"diff"`
	DiffPct  *float64 `json:"diff_pct,omitempty"`
}

// CurrencyComparison — сравнение периодов в одной валюте.
type CurrencyComparison struct {
	Currency string       `json:"currency"`
	TotalA   float64      `json:"total_a"`
	TotalB   float64      `json:"total_b"`
	Diff     float64      `json:"diff"`
	DiffPct  *float64     `json:"diff_pct,omitempty"`
	Rows     []CompareRow `json:"rows"`
}

// Compare сравнивает расходы двух периодов по категориям верхнего уровня; строки идут по убыванию |Diff|.
func Compare(a, b []Transaction) []CurrencyComparison {
	bucketsA := expenseBuckets(a, ByCategory.key)
	bucketsB := expenseBuckets(b, ByCategory.key)

	out := []CurrencyComparison{}
	for _, currency := range unionKeys(bucketsA, bucketsB) {
		out = append(out, compareCurrency(currency, bucketsA[currency], bucketsB[currency]))
	}
	slices.SortFunc(out, func(x, y CurrencyComparison) int { return currencyOrder(x.Currency, y.Currency) })

	return out
}

func compareCurrency(currency string, a, b map[string]*bucket) CurrencyComparison {
	c := CurrencyComparison{Currency: currency}

	var totalA, totalB float64
	for _, category := range unionKeys(a, b) {
		sumA, sumB := a[category].total(), b[category].total()
		totalA += sumA
		totalB += sumB

		c.Rows = append(c.Rows, CompareRow{
			Category: category,
			A:        round2(sumA),
			B:        round2(sumB),
			Diff:     round2(sumB - sumA),
			DiffPct:  percentChange(sumA, sumB),
		})
	}

	c.TotalA, c.TotalB = round2(totalA), round2(totalB)
	c.Diff = round2(totalB - totalA)
	c.DiffPct = percentChange(totalA, totalB)

	slices.SortStableFunc(c.Rows, func(x, y CompareRow) int {
		return cmp.Compare(math.Abs(y.Diff), math.Abs(x.Diff))
	})

	return c
}

type bucket struct {
	sum   float64
	count int
}

// total безопасен для nil: категории может не быть в одном из периодов.
func (b *bucket) total() float64 {
	if b == nil {
		return 0
	}
	return b.sum
}

// expenseBuckets раскладывает расходы по валюте, а внутри неё по ключу группы.
func expenseBuckets(txs []Transaction, key func(Transaction) string) map[string]map[string]*bucket {
	byCurrency := map[string]map[string]*bucket{}

	for _, t := range txs {
		if t.Kind() != Expense {
			continue
		}

		groups := byCurrency[t.OutcomeCurrency]
		if groups == nil {
			groups = map[string]*bucket{}
			byCurrency[t.OutcomeCurrency] = groups
		}

		k := key(t)
		b := groups[k]
		if b == nil {
			b = &bucket{}
			groups[k] = b
		}
		b.sum += t.Outcome
		b.count++
	}

	return byCurrency
}

// unionKeys — отсортированное объединение ключей двух карт.
func unionKeys[V any](a, b map[string]V) []string {
	keys := slices.AppendSeq(slices.Collect(maps.Keys(a)), maps.Keys(b))
	slices.Sort(keys)
	return slices.Compact(keys)
}

func share(part, total float64) float64 {
	if total == 0 {
		return 0
	}
	return round1(part / total * 100)
}

func percentChange(from, to float64) *float64 {
	if from == 0 {
		return nil
	}
	return new(round1((to - from) / from * 100))
}
