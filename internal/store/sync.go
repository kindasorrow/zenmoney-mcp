package store

import (
	"context"
	"errors"
	"time"

	"zenmoney-mcp/internal/zenmoney"
)

// retryAfter — пауза после неудачного синка: без неё каждый тул заново ждал бы недоступный API.
const retryAfter = time.Minute

// autoSyncTimeout ограничивает автосинк, когда копия уже есть: инкремент мал, а тул ждёт ответа.
const autoSyncTimeout = 30 * time.Second

// Syncer подтягивает изменения из ZenMoney в локальную базу.
type Syncer struct {
	client *zenmoney.Client
	store  *Store
	ttl    time.Duration

	// lock пускает один синк за раз: два вызова скачали бы один дифф дважды.
	// Канал, а не мьютекс, чтобы ожидание прерывалось отменой ctx.
	lock chan struct{}
	// failedAt и lastErr — последняя неудача синка, доступ только под lock.
	failedAt time.Time
	lastErr  error
}

// NewSyncer создаёт синхронизатор; ttl — возраст данных, после которого EnsureFresh идёт в API.
func NewSyncer(client *zenmoney.Client, store *Store, ttl time.Duration) *Syncer {
	return &Syncer{client: client, store: store, ttl: ttl, lock: make(chan struct{}, 1)}
}

// SyncResult — итог синка для тула sync.
type SyncResult struct {
	Changes
	SyncedAt time.Time `json:"synced_at"`
}

// Sync синхронизируется безусловно, без паузы после прошлой неудачи.
func (s *Syncer) Sync(ctx context.Context) (*SyncResult, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()

	return s.sync(ctx)
}

// EnsureFresh синхронизируется, если с прошлого синка прошло больше ttl. После неудачи
// retryAfter не ходит в API и сразу возвращает ту же ошибку.
func (s *Syncer) EnsureFresh(ctx context.Context) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()

	last, err := s.store.LastSync(ctx)
	if err != nil {
		return err
	}
	if time.Since(last) < s.ttl {
		return nil
	}
	if time.Since(s.failedAt) < retryAfter {
		return s.lastErr
	}

	// Первый синк — полная история, ему нужен весь таймаут клиента.
	if !last.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, autoSyncTimeout)
		defer cancel()
	}

	_, err = s.sync(ctx)
	return err
}

func (s *Syncer) acquire(ctx context.Context) error {
	select {
	case s.lock <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Syncer) release() {
	<-s.lock
}

// sync запоминает неудачу для паузы. Отмену вызывающим неудачей не считаем: API тут ни при чём.
func (s *Syncer) sync(ctx context.Context) (*SyncResult, error) {
	result, err := s.fetchAndApply(ctx)
	switch {
	case err == nil:
		s.failedAt, s.lastErr = time.Time{}, nil
	case !errors.Is(err, context.Canceled):
		s.failedAt, s.lastErr = time.Now(), err
	}
	return result, err
}

func (s *Syncer) fetchAndApply(ctx context.Context) (*SyncResult, error) {
	since, err := s.store.ServerTimestamp(ctx)
	if err != nil {
		return nil, err
	}

	diff, err := s.client.Diff(ctx, since)
	if err != nil {
		return nil, err
	}

	// Секунды без дробной части: так время синка и хранится, и выглядит в ответе.
	syncedAt := time.Now().Truncate(time.Second)

	changes, err := s.store.Apply(ctx, diff, syncedAt)
	if err != nil {
		return nil, err
	}

	return &SyncResult{Changes: changes, SyncedAt: syncedAt}, nil
}
