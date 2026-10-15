package tools

import (
	"cmp"
	"fmt"
	"time"

	"zenmoney-mcp/internal/report"
	"zenmoney-mcp/internal/store"
)

const (
	defaultListDays   = 30
	defaultListLimit  = 100
	defaultGroupLimit = 50
	defaultFlowMonths = 12
	// maxLimit держит ответ небольшим: тысяча операций — это десятки тысяч токенов.
	maxLimit = 1000

	defaultMonths = 12
	maxMonths     = 60
)

// startFunc выводит начало периода по умолчанию из его конца.
type startFunc func(end time.Time) time.Time

// lastDays — n дней, считая день конца.
func lastDays(n int) startFunc {
	return func(end time.Time) time.Time { return end.AddDate(0, 0, 1-n) }
}

// monthStart — 1-е число месяца, в котором конец периода.
func monthStart(end time.Time) time.Time {
	return time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, end.Location())
}

// lastMonths — n календарных месяцев, считая месяц конца.
func lastMonths(n int) startFunc {
	return func(end time.Time) time.Time { return monthStart(end).AddDate(0, 1-n, 0) }
}

// parsePeriod проверяет prefix+"from" и prefix+"to". С start пустой to — сегодня, а пустой
// from — start(to), то есть считается от конца периода. Без start обе границы обязательны.
func parsePeriod(prefix, from, to string, start startFunc) (store.Period, error) {
	if to == "" && start != nil {
		to = time.Now().Format(time.DateOnly)
	}
	end, err := parseDate(prefix+"to", to)
	if err != nil {
		return store.Period{}, err
	}

	if from == "" && start != nil {
		from = start(end).Format(time.DateOnly)
	}
	begin, err := parseDate(prefix+"from", from)
	if err != nil {
		return store.Period{}, err
	}

	if begin.After(end) {
		return store.Period{}, fmt.Errorf("%sfrom %s позже %sto %s", prefix, from, prefix, to)
	}

	return store.Period{From: from, To: to}, nil
}

func parseDate(name, value string) (time.Time, error) {
	d, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: ожидается дата YYYY-MM-DD, получено %q", name, value)
	}
	return d, nil
}

func parseKind(value string) (report.Kind, error) {
	switch kind := report.Kind(value); kind {
	case "", report.Expense, report.Income, report.Transfer:
		return kind, nil
	default:
		return "", fmt.Errorf("type: ожидается expense, income или transfer, получено %q", value)
	}
}

// parseFlowKind — тип для сводок: расход по умолчанию или доход. Переводы в сводки не входят.
func parseFlowKind(value string) (report.Kind, error) {
	switch kind := cmp.Or(report.Kind(value), report.Expense); kind {
	case report.Expense, report.Income:
		return kind, nil
	default:
		return "", fmt.Errorf("type: ожидается expense или income, получено %q", value)
	}
}

func parseGroupBy(value string) (report.GroupBy, error) {
	by := report.GroupBy(cmp.Or(value, string(report.ByCategory)))
	if !by.Valid() {
		return "", fmt.Errorf("group_by: ожидается category, subcategory, merchant, day, week или month, получено %q", value)
	}
	return by, nil
}

// parsePeriodBy проверяет разрез по календарю в параметре name; пустое значение даёт fallback.
func parsePeriodBy(name, value string, fallback report.GroupBy) (report.GroupBy, error) {
	by := cmp.Or(report.GroupBy(value), fallback)
	if by != "" && !by.IsPeriod() {
		return "", fmt.Errorf("%s: ожидается day, week или month, получено %q", name, value)
	}
	return by, nil
}

func parseLimit(value, fallback int) (int, error) {
	limit := cmp.Or(value, fallback)
	if limit < 1 || limit > maxLimit {
		return 0, fmt.Errorf("limit: ожидается число от 1 до %d, получено %d", maxLimit, value)
	}
	return limit, nil
}

func parseMonths(value int) (int, error) {
	months := cmp.Or(value, defaultMonths)
	if months < 1 || months > maxMonths {
		return 0, fmt.Errorf("months: ожидается число от 1 до %d, получено %d", maxMonths, value)
	}
	return months, nil
}
