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

// IsPeriod — разрез по календарю: день, неделя или месяц.
func (g GroupBy) IsPeriod() bool {
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
// Periods — сумма группы по периодам SummaryOptions.Split; периодов без операций в ней нет.
type Group struct {
	Key     string             `json:"key"`
	Sum     float64            `json:"sum"`
	Count   int                `json:"count"`
	Share   float64            `json:"share"`
	Periods map[string]float64 `json:"periods,omitempty"`
}

// CurrencySummary — сводка в одной валюте. Omitted — групп, не вошедших из-за limit; Total их учитывает.
type CurrencySummary struct {
	Currency string  `json:"currency"`
	Total    float64 `json:"total"`
	Count    int     `json:"count"`
	Groups   []Group `json:"groups"`
	Omitted  int     `json:"omitted,omitempty"`
}

// SummaryOptions — параметры Summarize. Пустой Kind — расходы; Split пуст, если разбивка по периодам не нужна.
type SummaryOptions struct {
	Kind  Kind
	By    GroupBy
	Split GroupBy
	Limit int
}

// Summarize сводит расходы или доходы по валютам и группам, не смешивая валюты; переводы пропускаются.
// Limit режет только категории и мерчантов: у периодов обрезка по сумме убрала бы дни из середины диапазона.
func Summarize(txs []Transaction, opts SummaryOptions) []CurrencySummary {
	out := []CurrencySummary{}
	for currency, buckets := range groupBuckets(txs, cmp.Or(opts.Kind, Expense), opts.By, opts.Split) {
		out = append(out, summarizeCurrency(currency, buckets, opts))
	}
	slices.SortFunc(out, func(a, b CurrencySummary) int { return currencyOrder(a.Currency, b.Currency) })

	return out
}

func summarizeCurrency(currency string, buckets map[string]*bucket, opts SummaryOptions) CurrencySummary {
	s := CurrencySummary{Currency: currency}

	var total float64
	for _, b := range buckets {
		total += b.sum
		s.Count += b.count
	}
	s.Total = round2(total)

	for key, b := range buckets {
		for period, sum := range b.periods {
			b.periods[period] = round2(sum)
		}
		s.Groups = append(s.Groups, Group{Key: key, Sum: round2(b.sum), Count: b.count, Share: share(b.sum, total), Periods: b.periods})
	}

	if opts.By.IsPeriod() {
		slices.SortFunc(s.Groups, func(a, b Group) int { return strings.Compare(a.Key, b.Key) })
		return s
	}

	slices.SortFunc(s.Groups, func(a, b Group) int {
		return cmp.Or(cmp.Compare(b.Sum, a.Sum), strings.Compare(a.Key, b.Key))
	})
	if opts.Limit > 0 && len(s.Groups) > opts.Limit {
		s.Omitted = len(s.Groups) - opts.Limit
		s.Groups = s.Groups[:opts.Limit]
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

// Compare сравнивает расходы или доходы двух периодов по категориям верхнего уровня; строки идут по убыванию |Diff|.
func Compare(a, b []Transaction, kind Kind) []CurrencyComparison {
	bucketsA := groupBuckets(a, kind, ByCategory, "")
	bucketsB := groupBuckets(b, kind, ByCategory, "")

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
	sum     float64
	count   int
	periods map[string]float64
}

// total безопасен для nil: категории может не быть в одном из периодов.
func (b *bucket) total() float64 {
	if b == nil {
		return 0
	}
	return b.sum
}

// groupBuckets раскладывает операции типа kind по валюте, а внутри неё по группе by.
// С непустым split сумма группы копится ещё и по периодам.
func groupBuckets(txs []Transaction, kind Kind, by, split GroupBy) map[string]map[string]*bucket {
	byCurrency := map[string]map[string]*bucket{}

	for _, t := range txs {
		if t.Kind() != kind {
			continue
		}

		amount, currency := t.Amount()
		b := cell(byCurrency, currency, by.key(t))
		b.sum += amount
		b.count++

		if split != "" {
			if b.periods == nil {
				b.periods = map[string]float64{}
			}
			b.periods[split.key(t)] += amount
		}
	}

	return byCurrency
}

// cell возвращает m[outer][inner], создавая недостающие уровни.
func cell[V any](m map[string]map[string]*V, outer, inner string) *V {
	inners := m[outer]
	if inners == nil {
		inners = map[string]*V{}
		m[outer] = inners
	}

	v := inners[inner]
	if v == nil {
		v = new(V)
		inners[inner] = v
	}

	return v
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
