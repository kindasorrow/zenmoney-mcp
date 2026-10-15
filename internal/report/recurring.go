package report

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// ponytail: эвристика на медианах без сезонности. Рост цены больше amountTolerance делит серию пополам,
// и половинки могут не набрать minCount. Точнее: искать сдвиг цены внутри серии, а не кластеры по сумме.
const (
	// minOccurrences — на двух списаниях подпиской выглядит любое совпадение.
	minOccurrences = 3
	// amountTolerance — разброс суммы внутри подписки, доля от минимальной.
	amountTolerance = 0.15
	// intervalTolerance — допуск интервала, доля от медианы.
	intervalTolerance = 0.2
	// minIntervalSlack — нижний допуск в днях: списание 31-го уезжает на 1-е, выходные сдвигают ещё на пару дней.
	minIntervalSlack = 3
	// minRegularShare — доля интервалов в допуске; один пропущенный месяц подписку не ломает.
	minRegularShare = 0.75
	// minIntervalDays — чаще раза в неделю платят по привычке (кофе, обеды), а не по подписке.
	minIntervalDays = 6
)

// Recurring — регулярное списание: похожая сумма одному получателю через примерно равные промежутки.
type Recurring struct {
	Payee        string  `json:"payee"`
	Category     string  `json:"category"`
	Currency     string  `json:"currency"`
	Amount       float64 `json:"amount"`
	IntervalDays int     `json:"interval_days"`
	Period       string  `json:"period,omitempty"`
	Count        int     `json:"count"`
	LastDate     string  `json:"last_date"`
	NextDate     string  `json:"next_date"`
	// Active — с последнего списания прошло не больше интервала с допуском.
	Active bool `json:"active"`
}

// FindRecurring ищет регулярные списания среди расходов с получателем: по одной категории
// подписку от обычных трат не отличить. Сначала действующие, затем по валюте (RUB первым),
// внутри валюты по убыванию стоимости в месяц: суммы в разных валютах не сравниваются.
func FindRecurring(txs []Transaction, minCount int, today time.Time) []Recurring {
	minCount = max(minCount, minOccurrences)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)

	found := []Recurring{}
	for _, series := range payeeSeries(txs) {
		for _, cluster := range clusterByAmount(series) {
			if r, ok := detectRecurring(cluster, minCount, today); ok {
				found = append(found, r)
			}
		}
	}

	slices.SortFunc(found, func(a, b Recurring) int {
		if a.Active != b.Active {
			if a.Active {
				return -1
			}
			return 1
		}
		return cmp.Or(
			currencyOrder(a.Currency, b.Currency),
			cmp.Compare(b.monthlyCost(), a.monthlyCost()),
			strings.Compare(a.Payee, b.Payee),
			cmp.Compare(a.Amount, b.Amount),
		)
	})

	return found
}

func (r Recurring) monthlyCost() float64 {
	return r.Amount * 30 / float64(r.IntervalDays)
}

// payeeSeries группирует расходы по получателю без учёта регистра и по валюте.
func payeeSeries(txs []Transaction) map[string][]Transaction {
	series := map[string][]Transaction{}
	for _, t := range txs {
		if t.Kind() != Expense || t.Payee == "" {
			continue
		}
		key := t.OutcomeCurrency + "\x00" + strings.ToLower(t.Payee)
		series[key] = append(series[key], t)
	}
	return series
}

// clusterByAmount делит серию по близким суммам: у одного мерчанта бывает несколько подписок по разной цене.
func clusterByAmount(txs []Transaction) [][]Transaction {
	sorted := slices.Clone(txs)
	slices.SortFunc(sorted, func(a, b Transaction) int { return cmp.Compare(a.Outcome, b.Outcome) })

	var clusters [][]Transaction
	start := 0
	for i := 1; i <= len(sorted); i++ {
		if i == len(sorted) || sorted[i].Outcome > sorted[start].Outcome*(1+amountTolerance) {
			clusters = append(clusters, sorted[start:i])
			start = i
		}
	}

	return clusters
}

func detectRecurring(txs []Transaction, minCount int, today time.Time) (Recurring, bool) {
	if len(txs) < minCount {
		return Recurring{}, false
	}
	slices.SortFunc(txs, func(a, b Transaction) int { return strings.Compare(a.Date, b.Date) })

	dates := make([]time.Time, len(txs))
	amounts := make([]float64, len(txs))
	for i, t := range txs {
		d, err := time.Parse(time.DateOnly, t.Date)
		if err != nil {
			return Recurring{}, false
		}
		dates[i], amounts[i] = d, t.Outcome
	}

	intervals := make([]int, len(dates)-1)
	for i := range intervals {
		intervals[i] = daysBetween(dates[i], dates[i+1])
	}

	interval := median(intervals)
	if interval < minIntervalDays {
		return Recurring{}, false
	}

	slack := max(minIntervalSlack, int(float64(interval)*intervalTolerance))
	regular := 0
	for _, iv := range intervals {
		if abs(iv-interval) <= slack {
			regular++
		}
	}
	if float64(regular) < minRegularShare*float64(len(intervals)) {
		return Recurring{}, false
	}

	last := txs[len(txs)-1]
	lastDate := dates[len(dates)-1]

	return Recurring{
		Payee:        last.Payee,
		Category:     last.TopCategory(),
		Currency:     last.OutcomeCurrency,
		Amount:       round2(median(amounts)),
		IntervalDays: interval,
		Period:       periodName(interval),
		Count:        len(txs),
		LastDate:     last.Date,
		NextDate:     lastDate.AddDate(0, 0, interval).Format(time.DateOnly),
		Active:       daysBetween(lastDate, today) <= interval+slack,
	}, true
}

// periodName называет привычные периоды; для остальных хватает interval_days.
func periodName(days int) string {
	switch {
	case days >= 6 && days <= 8:
		return "weekly"
	case days >= 27 && days <= 33:
		return "monthly"
	case days >= 85 && days <= 95:
		return "quarterly"
	case days >= 355 && days <= 375:
		return "yearly"
	}
	return ""
}

func daysBetween(from, to time.Time) int {
	return int(to.Sub(from).Hours() / 24)
}

func median[T int | float64](xs []T) T {
	s := slices.Clone(xs)
	slices.Sort(s)

	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
