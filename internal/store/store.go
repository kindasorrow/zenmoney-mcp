// Package store — локальная копия данных ZenMoney в SQLite и синхронизация с API.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"zenmoney-mcp/internal/report"
	"zenmoney-mcp/internal/zenmoney"
)

const (
	metaServerTimestamp = "server_timestamp"
	metaSyncedAt        = "synced_at"
)

// batchSize держит число параметров в запросе ниже лимита SQLite (32766).
const batchSize = 500

// Store — локальный кэш: файл можно удалить, следующий синк скачает всё заново.
type Store struct {
	db *gorm.DB
	// ro — отдельное соединение только на чтение для тула query.
	ro *sql.DB
}

// Open открывает базу, при необходимости создаёт файл и схему.
func Open(path string) (*Store, error) {
	if err := createPrivateFile(path); err != nil {
		return nil, err
	}

	// glebarez/sqlite — диалект GORM на движке modernc.org/sqlite: чистый Go, без cgo.
	db, err := gorm.Open(sqlite.Open(dsn(path,
		"_pragma=busy_timeout(5000)", // ждать запись второго контейнера на том же файле
		"_pragma=journal_mode(WAL)",  // чтение не блокируется чужой записью
		"_pragma=temp_store(memory)", // корневая ФС контейнера read-only
		"_txlock=immediate",          // Apply читает метку и пишет без гонки с чужим коммитом
	)), &gorm.Config{
		// stdout занят MCP, а логгер GORM пишет туда по умолчанию; ошибки и так возвращаются наверх.
		Logger:                                   logger.Discard,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("store: get connection pool: %w", err)
	}
	// Одно соединение: писатель в SQLite один, а с пулом параллельные тулы ловили бы SQLITE_BUSY.
	sqlDB.SetMaxOpenConns(1)

	if err = migrate(db); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	ro, err := openReadOnly(path)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	return &Store{db: db, ro: ro}, nil
}

// openReadOnly открывает файл в mode=ro: SQLite отказывает в любой записи в базу,
// что бы ни прислали в query. Временные таблицы при этом доступны, они в памяти.
func openReadOnly(path string) (*sql.DB, error) {
	ro, err := sql.Open(sqlite.DriverName, dsn(path, "mode=ro", "_pragma=busy_timeout(5000)", "_pragma=temp_store(memory)"))
	if err != nil {
		return nil, fmt.Errorf("store: open %s read-only: %w", path, err)
	}
	if err := ro.Ping(); err != nil {
		_ = ro.Close()
		return nil, fmt.Errorf("store: open %s read-only: %w", path, err)
	}
	return ro, nil
}

// dsn собирает URI файла. Драйвер разбирает его как URL: без экранирования «#» или «%» в пути открыли бы другой файл.
func dsn(path string, params ...string) string {
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + strings.Join(params, "&")
}

// migrate создаёт схему. Повтор — для двух контейнеров на пустом volume: второй падает
// на CREATE TABLE, а при повторе таблицы уже есть.
func migrate(db *gorm.DB) error {
	err := db.AutoMigrate(models...)
	if err != nil {
		err = db.AutoMigrate(models...)
	}
	if err != nil {
		return fmt.Errorf("store: migrate schema: %w", err)
	}
	return createOperationsView(db)
}

// createPrivateFile создаёт файл базы с правами 0600 до драйвера, который взял бы umask (обычно 0644):
// в нём вся финансовая история. Журнал наследует права файла; Chmod чинит файл от прошлых запусков.
func createPrivateFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("store: create %s: %w", path, err)
	}
	defer f.Close()

	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("store: chmod %s: %w", path, err)
	}

	return nil
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return errors.Join(s.ro.Close(), fmt.Errorf("store: get connection pool: %w", err))
	}
	return errors.Join(s.ro.Close(), sqlDB.Close())
}

// ServerTimestamp — метка последнего применённого диффа; ноль до первого синка.
func (s *Store) ServerTimestamp(ctx context.Context) (int64, error) {
	return readMeta(s.db.WithContext(ctx), metaServerTimestamp)
}

// LastSync — время последнего успешного синка; нулевое, если синка не было.
func (s *Store) LastSync(ctx context.Context) (time.Time, error) {
	unix, err := readMeta(s.db.WithContext(ctx), metaSyncedAt)
	if err != nil || unix == 0 {
		return time.Time{}, err
	}
	return time.Unix(unix, 0), nil
}

// readMeta читает метку. Find, а не First: до первого синка метки нет, и это не ошибка.
func readMeta(db *gorm.DB, key string) (int64, error) {
	var m Meta
	if err := db.Limit(1).Find(&m, "key = ?", key).Error; err != nil {
		return 0, fmt.Errorf("store: read %s: %w", key, err)
	}
	return m.Value, nil
}

// Changes — что изменил дифф. В Updated только ненулевые типы, Deleted — реально удалённые строки.
type Changes struct {
	Updated map[string]int `json:"updated"`
	Deleted int            `json:"deleted"`
}

// Apply применяет дифф вместе с новой меткой одной транзакцией: при обрыве следующий синк
// повторит этот же дифф, а не начнёт с метки, под которой не хватает данных.
func (s *Store) Apply(ctx context.Context, d *zenmoney.Diff, syncedAt time.Time) (Changes, error) {
	var changes Changes

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		stored, err := readMeta(tx, metaServerTimestamp)
		if err != nil {
			return err
		}
		// Второй контейнер уже применил дифф новее: этот откатил бы данные назад.
		if d.ServerTimestamp < stored {
			changes = Changes{Updated: map[string]int{}}
			return nil
		}

		if changes, err = applyDiff(tx, d); err != nil {
			return err
		}

		return upsert(tx, []Meta{
			{Key: metaServerTimestamp, Value: d.ServerTimestamp},
			{Key: metaSyncedAt, Value: syncedAt.Unix()},
		})
	})
	if err != nil {
		return Changes{}, err
	}

	return changes, nil
}

func applyDiff(tx *gorm.DB, d *zenmoney.Diff) (Changes, error) {
	live, deletedIDs := splitDeleted(d.Transaction)

	if err := upsert(tx, convert(d.Instrument, newInstrument)); err != nil {
		return Changes{}, err
	}
	if err := upsert(tx, convert(d.Account, newAccount)); err != nil {
		return Changes{}, err
	}
	if err := upsert(tx, convert(d.Tag, newTag)); err != nil {
		return Changes{}, err
	}
	if err := upsert(tx, convert(d.Merchant, newMerchant)); err != nil {
		return Changes{}, err
	}
	if err := upsert(tx, convert(live, newTransaction)); err != nil {
		return Changes{}, err
	}
	deleted, err := deleteTransactions(tx, deletedIDs)
	if err != nil {
		return Changes{}, err
	}
	removed, err := applyDeletions(tx, d.Deletion)
	if err != nil {
		return Changes{}, err
	}

	updated := map[string]int{
		"instrument":  len(d.Instrument),
		"account":     len(d.Account),
		"tag":         len(d.Tag),
		"merchant":    len(d.Merchant),
		"transaction": len(live),
	}
	maps.DeleteFunc(updated, func(_ string, n int) bool { return n == 0 })

	return Changes{Updated: updated, Deleted: deleted + removed}, nil
}

// upsert заменяет записи с тем же ключом целиком.
func upsert[T any](tx *gorm.DB, rows []T) error {
	if len(rows) == 0 {
		return nil
	}

	err := tx.Clauses(clause.OnConflict{UpdateAll: true}).
		Omit(clause.Associations).
		CreateInBatches(rows, batchSize).Error
	if err != nil {
		return fmt.Errorf("store: upsert %T: %w", rows, err)
	}

	return nil
}

func convert[From, To any](items []From, fn func(From) To) []To {
	out := make([]To, len(items))
	for i, item := range items {
		out[i] = fn(item)
	}
	return out
}

// splitDeleted отделяет операции с флагом deleted: ZenMoney удаляет их мягко, а не записью в deletion.
func splitDeleted(txs []zenmoney.Transaction) (live []zenmoney.Transaction, deleted []string) {
	for _, t := range txs {
		if t.Deleted {
			deleted = append(deleted, t.ID)
		} else {
			live = append(live, t)
		}
	}
	return live, deleted
}

// deleteTransactions удаляет пачками: в полном синке удалённых может быть больше, чем SQLite принимает параметров в IN.
func deleteTransactions(tx *gorm.DB, ids []string) (int, error) {
	deleted := 0
	for chunk := range slices.Chunk(ids, batchSize) {
		res := tx.Delete(&Transaction{}, "id IN ?", chunk)
		if res.Error != nil {
			return 0, fmt.Errorf("store: delete transactions: %w", res.Error)
		}
		deleted += int(res.RowsAffected)
	}
	return deleted, nil
}

// Тип из deletion → модель; остальные сущности база не хранит, их удаления пропускаются.
var deletableModels = map[string]func() any{
	"instrument":  func() any { return &Instrument{} },
	"account":     func() any { return &Account{} },
	"tag":         func() any { return &Tag{} },
	"merchant":    func() any { return &Merchant{} },
	"transaction": func() any { return &Transaction{} },
}

func applyDeletions(tx *gorm.DB, deletions []zenmoney.Deletion) (int, error) {
	removed := 0
	for _, d := range deletions {
		model, ok := deletableModels[d.Object]
		if !ok {
			continue
		}
		res := tx.Delete(model(), "id = ?", d.ID)
		if res.Error != nil {
			return 0, fmt.Errorf("store: delete %s %s: %w", d.Object, d.ID, res.Error)
		}
		removed += int(res.RowsAffected)
	}
	return removed, nil
}

// Accounts возвращает счета с валютами: сначала действующие, внутри по названию.
func (s *Store) Accounts(ctx context.Context, includeArchived bool) ([]Account, error) {
	query := s.db.WithContext(ctx).Preload("Instrument").Order("archive, title")
	if !includeArchived {
		query = query.Where("archive = ?", false)
	}

	var accounts []Account
	if err := query.Find(&accounts).Error; err != nil {
		return nil, fmt.Errorf("store: read accounts: %w", err)
	}

	return accounts, nil
}

// Period — границы периода YYYY-MM-DD, обе включительно.
type Period struct {
	From, To string
}

// Contains сообщает, попадает ли дата в период.
func (p Period) Contains(date string) bool {
	return date >= p.From && date <= p.To
}

// Transactions возвращает операции из любого из периодов, от новых к старым, с названиями вместо id.
// Несколько периодов — один запрос, чтобы compare_periods не загружал связи дважды.
// Операции с обеими нулевыми суммами пропускаются: это не движение денег, и тип не определить.
func (s *Store) Transactions(ctx context.Context, period Period, more ...Period) ([]report.Transaction, error) {
	inPeriods := s.db.Where("date BETWEEN ? AND ?", period.From, period.To)
	for _, p := range more {
		inPeriods = inPeriods.Or("date BETWEEN ? AND ?", p.From, p.To)
	}

	var rows []Transaction
	err := s.db.WithContext(ctx).
		Preload("IncomeAccount").
		Preload("OutcomeAccount").
		Preload("IncomeInstrument").
		Preload("OutcomeInstrument").
		Preload("Tag.Parent").
		Preload("Merchant").
		Where(inPeriods).
		Where("(income > 0 OR outcome > 0)").
		Order("date DESC, id").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("store: read transactions: %w", err)
	}

	return convert(rows, Transaction.report), nil
}

// Counts — число счетов и транзакций в базе, для проверки --check.
func (s *Store) Counts(ctx context.Context) (accounts, transactions int64, err error) {
	db := s.db.WithContext(ctx)

	if err := db.Model(&Account{}).Count(&accounts).Error; err != nil {
		return 0, 0, fmt.Errorf("store: count accounts: %w", err)
	}
	if err := db.Model(&Transaction{}).Count(&transactions).Error; err != nil {
		return 0, 0, fmt.Errorf("store: count transactions: %w", err)
	}

	return accounts, transactions, nil
}
