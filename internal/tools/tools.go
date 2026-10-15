// Package tools публикует аналитику ZenMoney как MCP-тулы. Все они только читают: в ZenMoney уходит лишь запрос диффа.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"zenmoney-mcp/internal/report"
	"zenmoney-mcp/internal/store"
)

const instructions = `Read-only аналитика расходов и доходов из ZenMoney.
Суммы в валюте счёта. Итоги разбиты по валютам: не складывай разные валюты между собой.
Переводы между своими счетами не считаются ни расходом, ни доходом.
Сначала используй готовые тулы; query — для вопросов, на которые их не хватает.
Устаревшие данные синхронизируются автоматически перед ответом; принудительно — тулом sync.`

// queryDescription описывает схему для query: без неё модель угадывала бы таблицы и правила подсчёта.
var queryDescription = fmt.Sprintf(`Произвольный SELECT к локальной копии в SQLite для вопросов, на которые не хватает готовых тулов. `+
	`Только чтение, один запрос без «;», до %d строк, до %.0f секунд.
Основная таблица — представление operations, строка на операцию: id, date (текст YYYY-MM-DD, месяц — substr(date, 1, 7)),
kind (expense, income, transfer), amount, currency, account, to_account, to_amount, to_currency (только у переводов),
category (верхний уровень, NULL без категории), subcategory, payee (мерчант или получатель из выписки), raw_payee, comment.
Суммы в валюте счёта: группируй по currency и не складывай разные валюты. Переводы — kind = 'transfer', это не трата и не доход.
Исходные таблицы: accounts (id, title, type, balance, archive, instrument_id), instruments (id, code),
tags (id, title, parent_id), merchants (id, title), transactions.
LIKE, lower и upper не учитывают регистр и для кириллицы: category LIKE '%%еда%%' находит «Еда».`, store.MaxQueryRows, store.QueryTimeout.Seconds())

type handlers struct {
	store  *store.Store
	syncer *store.Syncer
	logger *slog.Logger
}

// NewServer собирает MCP-сервер со всеми тулами.
func NewServer(st *store.Store, syncer *store.Syncer, logger *slog.Logger, version string) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: "zenmoney-mcp", Version: version},
		&mcp.ServerOptions{Instructions: instructions},
	)

	h := &handlers{store: st, syncer: syncer, logger: logger}
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(server, &mcp.Tool{
		Name:        "sync",
		Description: "Принудительно синхронизировать данные с ZenMoney. Возвращает, сколько сущностей обновлено и удалено, и время синка.",
		Annotations: readOnly,
	}, h.sync)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_accounts",
		Description: "Счета с текущими балансами и валютами. Архивные скрыты, если не попросить include_archived.",
		Annotations: readOnly,
	}, h.listAccounts)

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_transactions",
		Description: "Операции за период, от новых к старым, с названиями счетов и категорий. " +
			"По умолчанию последние 30 дней и не больше 100 штук; matched — сколько нашлось до обрезки по limit.",
		Annotations: readOnly,
	}, h.listTransactions)

	mcp.AddTool(server, &mcp.Tool{
		Name: "spending_summary",
		Description: "Сводка расходов (с type=income — доходов) за период по валютам: сумма, количество и доля " +
			"от итога в каждой группе. Переводы не входят. Категория верхнего уровня включает подкатегории. " +
			"split_by раскладывает каждую группу по месяцам, неделям или дням: так видна динамика категорий. " +
			"По умолчанию текущий месяц, расходы, группировка по категориям.",
		Annotations: readOnly,
	}, h.spendingSummary)

	mcp.AddTool(server, &mcp.Tool{
		Name: "compare_periods",
		Description: "Сравнение расходов (с type=income — доходов) двух периодов (A и B) по категориям верхнего уровня: " +
			"суммы, разница B − A и изменение в процентах. Строки по убыванию модуля разницы.",
		Annotations: readOnly,
	}, h.comparePeriods)

	mcp.AddTool(server, &mcp.Tool{
		Name: "cash_flow",
		Description: "Денежный поток по валютам: доходы, расходы и их разница по месяцам (неделям, дням) и за весь период, " +
			"savings_rate — доля дохода, не ушедшая в расходы, в процентах. Переводы между своими счетами не входят. " +
			"По умолчанию последние 12 месяцев, считая текущий.",
		Annotations: readOnly,
	}, h.cashFlow)

	mcp.AddTool(server, &mcp.Tool{
		Name: "recurring_payments",
		Description: "Поиск регулярных списаний, то есть подписок: один получатель, похожая сумма, " +
			"примерно равный интервал. Сначала действующие, затем по валюте, внутри по стоимости в месяц. " +
			"Для годовых подписок нужно окно months от 36.",
		Annotations: readOnly,
	}, h.recurringPayments)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "query",
		Description: queryDescription,
		Annotations: readOnly,
	}, h.query)

	return server
}

// refresh синхронизирует данные, если они устарели. Если синк не удался, а копия есть, тул отвечает по ней
// и предупреждает о причине и возрасте данных: без сети отчёт за прошлый месяц всё ещё полезен.
func (h *handlers) refresh(ctx context.Context) (warning string, err error) {
	syncErr := h.syncer.EnsureFresh(ctx)
	if syncErr == nil {
		return "", nil
	}

	last, err := h.store.LastSync(ctx)
	if err != nil {
		return "", err
	}
	if last.IsZero() {
		return "", syncErr
	}

	h.logger.Warn("auto sync failed, serving cached data", "error", syncErr)

	return fmt.Sprintf("Синхронизация не удалась: %v. Данные на %s.", syncErr, last.Format("2006-01-02 15:04 MST")), nil
}

// reply отдаёт JSON одним текстовым блоком; structuredContent пуст, иначе клиент платил бы контекстом дважды.
func reply(v any, warning string) (*mcp.CallToolResult, any, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, nil, fmt.Errorf("encode result: %w", err)
	}

	res := &mcp.CallToolResult{}
	if warning != "" {
		res.Content = append(res.Content, &mcp.TextContent{Text: warning})
	}
	res.Content = append(res.Content, &mcp.TextContent{Text: string(body)})

	return res, nil, nil
}

func (h *handlers) sync(ctx context.Context, _ *mcp.CallToolRequest, _ any) (*mcp.CallToolResult, any, error) {
	result, err := h.syncer.Sync(ctx)
	if err != nil {
		return nil, nil, err
	}
	return reply(result, "")
}

type listAccountsInput struct {
	IncludeArchived bool `json:"include_archived,omitempty" jsonschema:"показать и архивные счета"`
}

// accountItem — счёт в ответе list_accounts.
type accountItem struct {
	Title    string  `json:"title"`
	Type     string  `json:"type"`
	Balance  float64 `json:"balance"`
	Currency string  `json:"currency"`
	Archived bool    `json:"archived,omitempty"`
}

func (h *handlers) listAccounts(ctx context.Context, _ *mcp.CallToolRequest, in listAccountsInput) (*mcp.CallToolResult, any, error) {
	warning, err := h.refresh(ctx)
	if err != nil {
		return nil, nil, err
	}

	accounts, err := h.store.Accounts(ctx, in.IncludeArchived)
	if err != nil {
		return nil, nil, err
	}

	items := make([]accountItem, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, accountItem{
			Title:    a.Title,
			Type:     a.Type,
			Balance:  a.Balance,
			Currency: a.Currency(),
			Archived: a.Archive,
		})
	}

	return reply(items, warning)
}

type listTransactionsInput struct {
	DateFrom  string  `json:"date_from,omitempty" jsonschema:"начало периода YYYY-MM-DD, по умолчанию 30 дней до date_to включительно"`
	DateTo    string  `json:"date_to,omitempty" jsonschema:"конец периода YYYY-MM-DD включительно, по умолчанию сегодня"`
	Category  string  `json:"category,omitempty" jsonschema:"подстрока названия категории без учёта регистра; родитель включает подкатегории"`
	Payee     string  `json:"payee,omitempty" jsonschema:"подстрока названия мерчанта или получателя из выписки без учёта регистра"`
	MinAmount float64 `json:"min_amount,omitempty" jsonschema:"минимальная сумма в валюте счёта"`
	MaxAmount float64 `json:"max_amount,omitempty" jsonschema:"максимальная сумма в валюте счёта"`
	Type      string  `json:"type,omitempty" jsonschema:"expense, income или transfer"`
	Limit     int     `json:"limit,omitempty" jsonschema:"сколько операций вернуть, по умолчанию 100, максимум 1000"`
}

// transactionItem — операция в ответе list_transactions. У перевода to_amount и to_currency
// заполнены, только если зачислена другая сумма или другая валюта.
type transactionItem struct {
	Date       string      `json:"date"`
	Type       report.Kind `json:"type"`
	Amount     float64     `json:"amount"`
	Currency   string      `json:"currency"`
	Account    string      `json:"account"`
	ToAccount  string      `json:"to_account,omitempty"`
	ToAmount   float64     `json:"to_amount,omitempty"`
	ToCurrency string      `json:"to_currency,omitempty"`
	Category   string      `json:"category,omitempty"`
	Payee      string      `json:"payee,omitempty"`
	Comment    string      `json:"comment,omitempty"`
}

type transactionList struct {
	Matched int               `json:"matched"`
	Items   []transactionItem `json:"items"`
}

func (h *handlers) listTransactions(ctx context.Context, _ *mcp.CallToolRequest, in listTransactionsInput) (*mcp.CallToolResult, any, error) {
	p, err := parsePeriod("date_", in.DateFrom, in.DateTo, lastDays(defaultListDays))
	if err != nil {
		return nil, nil, err
	}
	kind, err := parseKind(in.Type)
	if err != nil {
		return nil, nil, err
	}
	limit, err := parseLimit(in.Limit, defaultListLimit)
	if err != nil {
		return nil, nil, err
	}

	warning, err := h.refresh(ctx)
	if err != nil {
		return nil, nil, err
	}

	txs, err := h.store.Transactions(ctx, p)
	if err != nil {
		return nil, nil, err
	}

	matched := report.Filter{
		Kind:      kind,
		Category:  in.Category,
		Payee:     in.Payee,
		MinAmount: in.MinAmount,
		MaxAmount: in.MaxAmount,
	}.Apply(txs)

	shown := matched[:min(len(matched), limit)]
	items := make([]transactionItem, 0, len(shown))
	for _, t := range shown {
		items = append(items, newTransactionItem(t))
	}

	return reply(transactionList{Matched: len(matched), Items: items}, warning)
}

func newTransactionItem(t report.Transaction) transactionItem {
	amount, currency := t.Amount()
	item := transactionItem{
		Date:     t.Date,
		Type:     t.Kind(),
		Amount:   amount,
		Currency: currency,
		Account:  t.OutcomeAccount,
		Category: t.CategoryPath(),
		Payee:    t.Payee,
		Comment:  t.Comment,
	}

	switch item.Type {
	case report.Income:
		item.Account = t.IncomeAccount
	case report.Transfer:
		item.ToAccount = t.IncomeAccount
		if t.Income != t.Outcome || t.IncomeCurrency != t.OutcomeCurrency {
			item.ToAmount = t.Income
			item.ToCurrency = t.IncomeCurrency
		}
	}

	return item
}

type spendingSummaryInput struct {
	DateFrom string `json:"date_from,omitempty" jsonschema:"начало периода YYYY-MM-DD, по умолчанию 1-е число месяца date_to"`
	DateTo   string `json:"date_to,omitempty" jsonschema:"конец периода YYYY-MM-DD включительно, по умолчанию сегодня"`
	Type     string `json:"type,omitempty" jsonschema:"expense (по умолчанию) или income"`
	GroupBy  string `json:"group_by,omitempty" jsonschema:"category (по умолчанию, верхний уровень), subcategory, merchant, day, week (ключ — понедельник), month"`
	SplitBy  string `json:"split_by,omitempty" jsonschema:"разложить каждую группу по периодам: day, week или month; только для group_by category, subcategory и merchant"`
	Category string `json:"category,omitempty" jsonschema:"учитывать только категории с этой подстрокой в названии; родитель включает подкатегории"`
	Limit    int    `json:"limit,omitempty" jsonschema:"сколько крупнейших групп вернуть для category, subcategory и merchant, по умолчанию 50; периоды возвращаются все"`
}

type spendingSummary struct {
	From       string                   `json:"from"`
	To         string                   `json:"to"`
	Type       report.Kind              `json:"type"`
	GroupBy    report.GroupBy           `json:"group_by"`
	SplitBy    report.GroupBy           `json:"split_by,omitempty"`
	Currencies []report.CurrencySummary `json:"currencies"`
}

func (h *handlers) spendingSummary(ctx context.Context, _ *mcp.CallToolRequest, in spendingSummaryInput) (*mcp.CallToolResult, any, error) {
	p, err := parsePeriod("date_", in.DateFrom, in.DateTo, monthStart)
	if err != nil {
		return nil, nil, err
	}
	kind, err := parseFlowKind(in.Type)
	if err != nil {
		return nil, nil, err
	}
	by, err := parseGroupBy(in.GroupBy)
	if err != nil {
		return nil, nil, err
	}
	split, err := parsePeriodBy("split_by", in.SplitBy, "")
	if err != nil {
		return nil, nil, err
	}
	if split != "" && by.IsPeriod() {
		return nil, nil, fmt.Errorf("split_by: group_by %s уже разбит по периодам", by)
	}
	limit, err := parseLimit(in.Limit, defaultGroupLimit)
	if err != nil {
		return nil, nil, err
	}

	warning, err := h.refresh(ctx)
	if err != nil {
		return nil, nil, err
	}

	txs, err := h.store.Transactions(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	txs = report.Filter{Category: in.Category}.Apply(txs)

	return reply(spendingSummary{
		From:       p.From,
		To:         p.To,
		Type:       kind,
		GroupBy:    by,
		SplitBy:    split,
		Currencies: report.Summarize(txs, report.SummaryOptions{Kind: kind, By: by, Split: split, Limit: limit}),
	}, warning)
}

type comparePeriodsInput struct {
	AFrom string `json:"a_from" jsonschema:"начало периода A, YYYY-MM-DD"`
	ATo   string `json:"a_to" jsonschema:"конец периода A включительно, YYYY-MM-DD"`
	BFrom string `json:"b_from" jsonschema:"начало периода B, YYYY-MM-DD"`
	BTo   string `json:"b_to" jsonschema:"конец периода B включительно, YYYY-MM-DD"`
	Type  string `json:"type,omitempty" jsonschema:"expense (по умолчанию) или income"`
}

func (h *handlers) comparePeriods(ctx context.Context, _ *mcp.CallToolRequest, in comparePeriodsInput) (*mcp.CallToolResult, any, error) {
	a, err := parsePeriod("a_", in.AFrom, in.ATo, nil)
	if err != nil {
		return nil, nil, err
	}
	b, err := parsePeriod("b_", in.BFrom, in.BTo, nil)
	if err != nil {
		return nil, nil, err
	}
	kind, err := parseFlowKind(in.Type)
	if err != nil {
		return nil, nil, err
	}

	warning, err := h.refresh(ctx)
	if err != nil {
		return nil, nil, err
	}

	txs, err := h.store.Transactions(ctx, a, b)
	if err != nil {
		return nil, nil, err
	}

	return reply(report.Compare(within(txs, a), within(txs, b), kind), warning)
}

type cashFlowInput struct {
	DateFrom string `json:"date_from,omitempty" jsonschema:"начало периода YYYY-MM-DD, по умолчанию 1-е число месяца на 11 месяцев раньше date_to"`
	DateTo   string `json:"date_to,omitempty" jsonschema:"конец периода YYYY-MM-DD включительно, по умолчанию сегодня"`
	GroupBy  string `json:"group_by,omitempty" jsonschema:"month (по умолчанию), week (ключ — понедельник) или day"`
}

type cashFlow struct {
	From       string                `json:"from"`
	To         string                `json:"to"`
	GroupBy    report.GroupBy        `json:"group_by"`
	Currencies []report.CurrencyFlow `json:"currencies"`
}

func (h *handlers) cashFlow(ctx context.Context, _ *mcp.CallToolRequest, in cashFlowInput) (*mcp.CallToolResult, any, error) {
	p, err := parsePeriod("date_", in.DateFrom, in.DateTo, lastMonths(defaultFlowMonths))
	if err != nil {
		return nil, nil, err
	}
	by, err := parsePeriodBy("group_by", in.GroupBy, report.ByMonth)
	if err != nil {
		return nil, nil, err
	}

	warning, err := h.refresh(ctx)
	if err != nil {
		return nil, nil, err
	}

	txs, err := h.store.Transactions(ctx, p)
	if err != nil {
		return nil, nil, err
	}

	return reply(cashFlow{From: p.From, To: p.To, GroupBy: by, Currencies: report.CashFlow(txs, by)}, warning)
}

// within отбирает операции периода; периоды могут пересекаться, поэтому операция может попасть в оба.
func within(txs []report.Transaction, p store.Period) []report.Transaction {
	var out []report.Transaction
	for _, t := range txs {
		if p.Contains(t.Date) {
			out = append(out, t)
		}
	}
	return out
}

type recurringPaymentsInput struct {
	Months   int `json:"months,omitempty" jsonschema:"окно поиска в месяцах назад от сегодня, по умолчанию 12, максимум 60"`
	MinCount int `json:"min_count,omitempty" jsonschema:"минимум списаний в серии, по умолчанию и не меньше 3"`
}

func (h *handlers) recurringPayments(ctx context.Context, _ *mcp.CallToolRequest, in recurringPaymentsInput) (*mcp.CallToolResult, any, error) {
	months, err := parseMonths(in.Months)
	if err != nil {
		return nil, nil, err
	}

	warning, err := h.refresh(ctx)
	if err != nil {
		return nil, nil, err
	}

	today := time.Now()
	txs, err := h.store.Transactions(ctx, store.Period{
		From: today.AddDate(0, -months, 0).Format(time.DateOnly),
		To:   today.Format(time.DateOnly),
	})
	if err != nil {
		return nil, nil, err
	}

	return reply(report.FindRecurring(txs, in.MinCount, today), warning)
}

type queryInput struct {
	SQL string `json:"sql" jsonschema:"один SELECT или WITH ... SELECT на диалекте SQLite"`
}

func (h *handlers) query(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
	warning, err := h.refresh(ctx)
	if err != nil {
		return nil, nil, err
	}

	res, err := h.store.Query(ctx, in.SQL)
	if err != nil {
		return nil, nil, err
	}

	return reply(res, warning)
}
