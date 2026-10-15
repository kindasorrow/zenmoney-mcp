package store

import (
	"cmp"

	"zenmoney-mcp/internal/report"
	"zenmoney-mcp/internal/zenmoney"
)

// Модели хранят только поля, нужные отчётам; связи объявлены ради Preload. Внешних ключей нет
// (см. Open): API не гарантирует целостность, и операция может ссылаться на удалённую категорию.

// Instrument — валюта. Code — трёхбуквенный код: RUB, USD.
type Instrument struct {
	ID   int `gorm:"primaryKey;autoIncrement:false"`
	Code string
}

type Account struct {
	ID           string `gorm:"primaryKey"`
	Title        string
	Type         string
	InstrumentID int
	Instrument   *Instrument
	Balance      float64
	Archive      bool
}

// Currency — код валюты счёта; пустой, если валюты нет в базе.
func (a Account) Currency() string {
	return a.Instrument.code()
}

// Tag — категория. ParentID пуст у категорий верхнего уровня.
type Tag struct {
	ID       string `gorm:"primaryKey"`
	Title    string
	ParentID string
	Parent   *Tag
}

type Merchant struct {
	ID    string `gorm:"primaryKey"`
	Title string
}

// Transaction — операция; TagID — первый тег из API, остальные отчётам не нужны.
type Transaction struct {
	ID                  string `gorm:"primaryKey"`
	Date                string `gorm:"index"`
	Income              float64
	Outcome             float64
	IncomeAccountID     string
	IncomeAccount       *Account
	OutcomeAccountID    string
	OutcomeAccount      *Account
	IncomeInstrumentID  int
	IncomeInstrument    *Instrument
	OutcomeInstrumentID int
	OutcomeInstrument   *Instrument
	TagID               string
	Tag                 *Tag
	MerchantID          string
	Merchant            *Merchant
	Payee               string
	Comment             string
}

// Meta хранит служебные метки синка.
type Meta struct {
	Key   string `gorm:"primaryKey"`
	Value int64
}

// models — все модели схемы, в порядке для AutoMigrate.
var models = []any{&Meta{}, &Instrument{}, &Account{}, &Tag{}, &Merchant{}, &Transaction{}}

func newInstrument(i zenmoney.Instrument) Instrument {
	return Instrument{ID: i.ID, Code: i.ShortTitle}
}

func newAccount(a zenmoney.Account) Account {
	return Account{
		ID:           a.ID,
		Title:        a.Title,
		Type:         a.Type,
		InstrumentID: a.Instrument,
		Balance:      a.Balance,
		Archive:      a.Archive,
	}
}

func newTag(t zenmoney.Tag) Tag {
	return Tag{ID: t.ID, Title: t.Title, ParentID: t.Parent}
}

func newMerchant(m zenmoney.Merchant) Merchant {
	return Merchant{ID: m.ID, Title: m.Title}
}

func newTransaction(t zenmoney.Transaction) Transaction {
	var tagID string
	if len(t.Tag) > 0 {
		tagID = t.Tag[0]
	}

	return Transaction{
		ID:                  t.ID,
		Date:                t.Date,
		Income:              t.Income,
		Outcome:             t.Outcome,
		IncomeAccountID:     t.IncomeAccount,
		OutcomeAccountID:    t.OutcomeAccount,
		IncomeInstrumentID:  t.IncomeInstrument,
		OutcomeInstrumentID: t.OutcomeInstrument,
		TagID:               tagID,
		MerchantID:          t.Merchant,
		Payee:               t.Payee,
		Comment:             t.Comment,
	}
}

// report переводит операцию в вид для отчётов; без мерчанта получатель — payee из выписки.
func (t Transaction) report() report.Transaction {
	r := report.Transaction{
		Date:            t.Date,
		Income:          t.Income,
		IncomeCurrency:  t.IncomeInstrument.code(),
		IncomeAccount:   t.IncomeAccount.title(),
		Outcome:         t.Outcome,
		OutcomeCurrency: t.OutcomeInstrument.code(),
		OutcomeAccount:  t.OutcomeAccount.title(),
		Payee:           cmp.Or(t.Merchant.title(), t.Payee),
		RawPayee:        t.Payee,
		Comment:         t.Comment,
	}
	if t.Tag != nil {
		r.Category = t.Tag.Title
		r.ParentCategory = t.Tag.Parent.title()
	}

	return r
}

// Методы ниже безопасны для nil: связь пуста, если операция ссылается на сущность, которой нет в базе.

func (i *Instrument) code() string {
	if i == nil {
		return ""
	}
	return i.Code
}

func (a *Account) title() string {
	if a == nil {
		return ""
	}
	return a.Title
}

func (t *Tag) title() string {
	if t == nil {
		return ""
	}
	return t.Title
}

func (m *Merchant) title() string {
	if m == nil {
		return ""
	}
	return m.Title
}
