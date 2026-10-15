package store

import (
	"cmp"
	"context"
	"strings"
	"testing"
	"time"

	"zenmoney-mcp/internal/report"
)

// Расход с подкатегорией и мерчантом, расход без категории, доход и перевод с конвертацией.
const mixedDiff = `{
	"serverTimestamp": 100,
	"instrument": [{"id": 1, "shortTitle": "USD"}, {"id": 2, "shortTitle": "RUB"}],
	"account": [
		{"id": "card", "title": "Карта", "type": "ccard", "instrument": 2, "balance": 1000, "archive": false},
		{"id": "usd", "title": "Долларовая", "type": "ccard", "instrument": 1, "balance": 50, "archive": false}
	],
	"tag": [
		{"id": "food", "title": "Еда", "parent": null},
		{"id": "cafe", "title": "Кафе", "parent": "food"},
		{"id": "salary", "title": "Зарплата", "parent": null}
	],
	"merchant": [{"id": "m1", "title": "Кофейня"}],
	"transaction": [
		{"id": "t1", "date": "2026-10-01", "income": 0, "outcome": 300, "incomeAccount": "card", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2, "tag": ["cafe"], "merchant": "m1", "payee": "COFFEE 123"},
		{"id": "t2", "date": "2026-10-02", "income": 0, "outcome": 700, "incomeAccount": "card", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2, "payee": "Такси"},
		{"id": "t3", "date": "2026-10-03", "income": 100000, "outcome": 0, "incomeAccount": "card", "outcomeAccount": "card",
		 "incomeInstrument": 2, "outcomeInstrument": 2, "tag": ["salary"]},
		{"id": "t4", "date": "2026-10-04", "income": 10, "outcome": 900, "incomeAccount": "usd", "outcomeAccount": "card",
		 "incomeInstrument": 1, "outcomeInstrument": 2}
	]
}`

func openSyncedStore(t *testing.T, diff string) *Store {
	t.Helper()

	syncer, st, _ := newTestSyncer(t, time.Hour, diff)
	if _, err := syncer.Sync(context.Background()); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return st
}

// rowMaps превращает ответ query в карты «колонка → значение».
func rowMaps(res QueryResult) []map[string]any {
	out := make([]map[string]any, len(res.Rows))
	for i, row := range res.Rows {
		out[i] = map[string]any{}
		for j, col := range res.Columns {
			out[i][col] = row[j]
		}
	}
	return out
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// Представление дублирует правила report.Transaction в SQL; расхождение сломало бы ответы query.
func TestOperationsView_MatchesReport(t *testing.T) {
	ctx := context.Background()
	st := openSyncedStore(t, mixedDiff)

	txs, err := st.Transactions(ctx, year2026)
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	res, err := st.Query(ctx, "SELECT * FROM operations ORDER BY date DESC, id")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	rows := rowMaps(res)
	if len(rows) != len(txs) || len(rows) != 4 {
		t.Fatalf("строк %d, операций %d; want 4", len(rows), len(txs))
	}

	for i, tx := range txs {
		row := rows[i]
		amount, currency := tx.Amount()
		account := tx.OutcomeAccount
		if tx.Kind() == report.Income {
			account = tx.IncomeAccount
		}

		if str(row["kind"]) != string(tx.Kind()) || row["amount"] != amount || str(row["currency"]) != currency ||
			str(row["account"]) != account || str(row["payee"]) != tx.Payee ||
			str(row["category"]) != cmp.Or(tx.ParentCategory, tx.Category) {
			t.Errorf("строка %d = %v, операция %+v", i, row, tx)
		}
	}

	transfer := rows[0]
	if transfer["to_account"] != "Долларовая" || transfer["to_amount"] != 10.0 || transfer["to_currency"] != "USD" {
		t.Errorf("перевод = %v", transfer)
	}
	if coffee := rows[3]; coffee["subcategory"] != "Кафе" || coffee["raw_payee"] != "COFFEE 123" {
		t.Errorf("расход с мерчантом = %v", coffee)
	}
}

// Запрос не может изменить кэш ни напрямую, ни через PRAGMA, ATTACH или цепочку через «;».
func TestQuery_ReadOnly(t *testing.T) {
	ctx := context.Background()
	st := openSyncedStore(t, mixedDiff)

	queries := []string{
		"DELETE FROM transactions",
		"WITH x AS (SELECT 1) DELETE FROM transactions",
		"SELECT 1; DELETE FROM transactions",
		"PRAGMA query_only = 0",
		"ATTACH DATABASE 'x.db' AS x",
		"VACUUM INTO 'copy.db'",
		"SELECT 1) LIMIT 1; DELETE FROM transactions; SELECT (1",
	}
	for _, q := range queries {
		if _, err := st.Query(ctx, q); err == nil {
			t.Errorf("Query(%q) выполнен, want ошибку", q)
		}
	}

	// Проверка запроса — второй рубеж: само соединение тоже не пишет.
	if _, err := st.ro.ExecContext(ctx, "DELETE FROM transactions"); err == nil || !strings.Contains(err.Error(), "readonly") {
		t.Errorf("запись через соединение query: %v, want readonly", err)
	}

	if _, n, _ := st.Counts(ctx); n != 4 {
		t.Errorf("транзакций после попыток записи %d, want 4", n)
	}
}

func TestQuery_TruncatesRows(t *testing.T) {
	st := openTestStore(t)

	res, err := st.Query(context.Background(),
		"WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM n LIMIT 600) SELECT x FROM n ORDER BY x DESC;")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(res.Rows) != MaxQueryRows || !res.Truncated || res.Rows[0][0] != int64(600) {
		t.Errorf("строк %d, truncated %v, первая %v; want %d, true, 600", len(res.Rows), res.Truncated, res.Rows[0], MaxQueryRows)
	}
}

// Бесконечный запрос обрывается по ctx, а не держит тул.
func TestQuery_Timeout(t *testing.T) {
	st := openTestStore(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := st.Query(ctx, "WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x + 1 FROM n) SELECT count(*) FROM n")
	if err == nil || !strings.Contains(err.Error(), "interrupt") {
		t.Errorf("err = %v, want прерывание", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("запрос шёл %v", elapsed)
	}
}
