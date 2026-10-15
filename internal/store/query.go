package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	// MaxQueryRows держит ответ query небольшим, как maxLimit у list_transactions.
	MaxQueryRows = 500
	// QueryTimeout обрывает случайный декартов JOIN, написанный моделью.
	QueryTimeout = 10 * time.Second
)

// operationsView — операции в том виде, в каком их видят отчёты: тип, сумма и валюта по правилам
// report.Transaction, названия вместо id. Правила дублируют Kind и Amount, тест сверяет одно с другим.
const operationsView = `CREATE VIEW operations AS
SELECT
	t.id,
	t.date,
	CASE
		WHEN t.income > 0 AND t.outcome > 0 THEN 'transfer'
		WHEN t.income > 0 THEN 'income'
		ELSE 'expense'
	END AS kind,
	CASE WHEN t.outcome > 0 THEN t.outcome ELSE t.income END AS amount,
	CASE WHEN t.outcome > 0 THEN oi.code ELSE ii.code END AS currency,
	CASE WHEN t.outcome > 0 THEN oa.title ELSE ia.title END AS account,
	CASE WHEN t.income > 0 AND t.outcome > 0 THEN ia.title END AS to_account,
	CASE WHEN t.income > 0 AND t.outcome > 0 THEN t.income END AS to_amount,
	CASE WHEN t.income > 0 AND t.outcome > 0 THEN ii.code END AS to_currency,
	COALESCE(parent.title, tag.title) AS category,
	CASE WHEN parent.title IS NOT NULL THEN tag.title END AS subcategory,
	COALESCE(NULLIF(m.title, ''), NULLIF(t.payee, '')) AS payee,
	NULLIF(t.payee, '') AS raw_payee,
	NULLIF(t.comment, '') AS comment
FROM transactions t
LEFT JOIN accounts oa ON oa.id = t.outcome_account_id
LEFT JOIN accounts ia ON ia.id = t.income_account_id
LEFT JOIN instruments oi ON oi.id = t.outcome_instrument_id
LEFT JOIN instruments ii ON ii.id = t.income_instrument_id
LEFT JOIN tags tag ON tag.id = t.tag_id
LEFT JOIN tags parent ON parent.id = tag.parent_id
LEFT JOIN merchants m ON m.id = t.merchant_id
WHERE t.income > 0 OR t.outcome > 0`

// createOperationsView пересоздаёт представление при каждом запуске: новая версия сервера
// не должна читать старое определение из базы.
func createOperationsView(db *gorm.DB) error {
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DROP VIEW IF EXISTS operations").Error; err != nil {
			return err
		}
		return tx.Exec(operationsView).Error
	})
	if err != nil {
		return fmt.Errorf("store: create view operations: %w", err)
	}
	return nil
}

// QueryResult — ответ query. Truncated — строк было больше MaxQueryRows.
type QueryResult struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated,omitempty"`
}

// Query выполняет один SELECT на соединении только для чтения.
func (s *Store) Query(ctx context.Context, query string) (QueryResult, error) {
	query, err := checkQuery(query)
	if err != nil {
		return QueryResult{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()

	// Временная таблица видна только своему соединению, поэтому все шаги идут через одно.
	conn, err := s.ro.Conn(ctx)
	if err != nil {
		return QueryResult{}, fmt.Errorf("sql: get connection: %w", err)
	}
	defer conn.Close()

	// Драйвер прерывает по ctx только Exec, а шаги rows.Next — нет. Поэтому запрос целиком
	// считается в CREATE TABLE AS, а LIMIT не даёт временной таблице разрастись.
	if _, err = conn.ExecContext(ctx, "DROP TABLE IF EXISTS temp.result"); err != nil {
		return QueryResult{}, fmt.Errorf("sql: %w", err)
	}
	create := fmt.Sprintf("CREATE TEMP TABLE result AS SELECT * FROM (%s) LIMIT %d", query, MaxQueryRows+1)
	if _, err = conn.ExecContext(ctx, create); err != nil {
		return QueryResult{}, fmt.Errorf("sql: %w", err)
	}

	rows, err := conn.QueryContext(ctx, "SELECT * FROM temp.result")
	if err != nil {
		return QueryResult{}, fmt.Errorf("sql: %w", err)
	}
	defer rows.Close()

	return readRows(rows)
}

// checkQuery пропускает один SELECT или WITH ... SELECT. Запись в базу и так запрещена соединением,
// а здесь отсекаются ATTACH, PRAGMA и VACUUM INTO, которые пишут в другие файлы, и цепочки через «;».
func checkQuery(query string) (string, error) {
	query = strings.TrimSuffix(strings.TrimSpace(query), ";")
	if strings.Contains(query, ";") {
		return "", errors.New("sql: нужен один запрос без «;»")
	}

	head := strings.ToUpper(query)
	if !strings.HasPrefix(head, "SELECT") && !strings.HasPrefix(head, "WITH") {
		return "", errors.New("sql: разрешён только SELECT или WITH ... SELECT")
	}

	return query, nil
}

func readRows(rows *sql.Rows) (QueryResult, error) {
	columns, err := rows.Columns()
	if err != nil {
		return QueryResult{}, fmt.Errorf("sql: read columns: %w", err)
	}

	res := QueryResult{Columns: columns, Rows: [][]any{}}
	for rows.Next() {
		if len(res.Rows) == MaxQueryRows {
			res.Truncated = true
			break
		}

		row, err := scanRow(rows, len(columns))
		if err != nil {
			return QueryResult{}, err
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, fmt.Errorf("sql: read rows: %w", err)
	}

	return res, nil
}

// scanRow читает строку как есть; BLOB отдаётся строкой, иначе JSON закодировал бы его в base64.
func scanRow(rows *sql.Rows, n int) ([]any, error) {
	values := make([]any, n)
	ptrs := make([]any, n)
	for i := range values {
		ptrs[i] = &values[i]
	}

	if err := rows.Scan(ptrs...); err != nil {
		return nil, fmt.Errorf("sql: read row: %w", err)
	}

	for i, v := range values {
		if b, ok := v.([]byte); ok {
			values[i] = string(b)
		}
	}

	return values, nil
}
