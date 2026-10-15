package store

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"

	gosqlite "github.com/glebarez/go-sqlite"
)

// SQLite без ICU складывает регистр только у латиницы: в query LIKE '%еда%' молча пропустил бы «Еда».
// Свои lower, upper и like работают по Unicode; драйвер подключает их ко всем соединениям, открытым позже.
func init() {
	gosqlite.MustRegisterDeterministicScalarFunction("lower", 1, mapText(strings.ToLower))
	gosqlite.MustRegisterDeterministicScalarFunction("upper", 1, mapText(strings.ToUpper))
	// Драйвер хранит функции по имени, поэтому одна like на обе формы: с ESCAPE и без.
	gosqlite.MustRegisterDeterministicScalarFunction("like", -1, like)
}

type scalarFunc = func(*gosqlite.FunctionContext, []driver.Value) (driver.Value, error)

func mapText(fn func(string) string) scalarFunc {
	return func(_ *gosqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		s, ok := text(args[0])
		if !ok {
			return nil, nil
		}
		return fn(s), nil
	}
}

// like(pattern, s[, escape]): «s LIKE pattern ESCAPE e» SQLite вызывает именно в таком порядке аргументов.
func like(_ *gosqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
	if len(args) != 2 && len(args) != 3 {
		return nil, errors.New("like: ожидается 2 или 3 аргумента")
	}

	pattern, okPattern := text(args[0])
	s, okS := text(args[1])
	if !okPattern || !okS {
		return nil, nil
	}

	escape := rune(-1)
	if len(args) == 3 {
		e, ok := text(args[2])
		if !ok {
			return nil, nil
		}
		runes := []rune(strings.ToLower(e))
		if len(runes) != 1 {
			return nil, errors.New("ESCAPE: нужен ровно один символ")
		}
		escape = runes[0]
	}

	if likeMatch([]rune(strings.ToLower(pattern)), []rune(strings.ToLower(s)), escape) {
		return int64(1), nil
	}
	return int64(0), nil
}

// text приводит аргумент к строке, как SQLite перед текстовой функцией; ok = false у NULL.
func text(v driver.Value) (string, bool) {
	switch v := v.(type) {
	case nil:
		return "", false
	case string:
		return v, true
	case []byte:
		return string(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	default:
		return fmt.Sprint(v), true
	}
}

// likeMatch сверяет строку с шаблоном LIKE: % — любая подстрока, _ — один символ, escape делает
// следующий символ обычным. При несовпадении шаблон откатывается к последнему %, без рекурсии.
func likeMatch(pattern, s []rune, escape rune) bool {
	p, i := 0, 0
	starP, starI := -1, 0

	for i < len(s) {
		if p < len(pattern) {
			switch c := pattern[p]; {
			case c == escape && p+1 < len(pattern):
				if pattern[p+1] == s[i] {
					p, i = p+2, i+1
					continue
				}
			case c == '%':
				starP, starI = p, i
				p++
				continue
			case c == '_' || c == s[i]:
				p, i = p+1, i+1
				continue
			}
		}

		if starP < 0 {
			return false
		}
		starI++
		p, i = starP+1, starI
	}

	for p < len(pattern) && pattern[p] == '%' {
		p++
	}
	return p == len(pattern)
}
