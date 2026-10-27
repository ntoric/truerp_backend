package audit

import (
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	// queueCapacity bounds pending audit entries; Enqueue never blocks the
	// caller — when the buffer is full entries are dropped and counted.
	queueCapacity = 16384
	// batchSize / flushInterval control how many entries are grouped into a
	// single INSERT and how long a partial batch may linger.
	batchSize      = 500
	flushInterval  = 200 * time.Millisecond
	workerCount    = 2
	insertAttempts = 2
)

var (
	queue     chan models.AuditLog
	stopCh    chan struct{}
	workersWG sync.WaitGroup
	spool     *spooler

	started   int32
	dropped   int64
	written   int64
	writeFail int64

	dbHandle *gorm.DB
)

// Start initialises the audit pipeline: registers the GORM change-capture
// hooks, ensures the current and next month's shard tables, launches the
// queue workers, and replays any entries left in the crash spool. Call once
// at startup.
func Start(db *gorm.DB) {
	if !atomic.CompareAndSwapInt32(&started, 0, 1) {
		return
	}
	dbHandle = db
	queue = make(chan models.AuditLog, queueCapacity)
	stopCh = make(chan struct{})
	ensuredTables = sync.Map{}

	var replayed []models.AuditLog
	spool, replayed = openSpool()

	RegisterHooks(db)
	StartMonthScheduler(db, stopCh)

	for i := 0; i < workerCount; i++ {
		workersWG.Add(1)
		go worker(i)
	}

	// Re-inject spooled entries after workers are up; commits dedupe on the
	// entry's UUID primary key, so replayed lines are idempotent.
	if len(replayed) > 0 {
		go func() {
			for _, e := range replayed {
				queue <- e
			}
		}()
	}

	log.Printf("audit: async logging started (queue=%d, workers=%d, spooled=%d)",
		queueCapacity, workerCount, len(replayed))
}

// Enqueue queues an audit entry for asynchronous persistence. It never blocks:
// if the buffer is full the entry is dropped and counted in DroppedCount (it
// still survives in the crash spool and is replayed on restart).
func Enqueue(entry models.AuditLog) {
	if atomic.LoadInt32(&started) == 0 {
		return
	}
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	if entry.Status == "" {
		entry.Status = "success"
	}
	if spool != nil {
		spool.append(entry)
	}
	select {
	case queue <- entry:
	default:
		atomic.AddInt64(&dropped, 1)
		if atomic.LoadInt64(&dropped)%1000 == 1 {
			log.Printf("audit: queue full, dropped %d entries so far", dropped)
		}
	}
}

// Dropped returns the number of entries discarded because the queue was full.
func Dropped() int64 { return atomic.LoadInt64(&dropped) }

// Written returns the number of entries successfully persisted.
func Written() int64 { return atomic.LoadInt64(&written) }

// Pending returns the current queue depth.
func Pending() int { return len(queue) }

// Shutdown stops the scheduler, drains the queue, and compacts the spool
// before returning.
func Shutdown() {
	if !atomic.CompareAndSwapInt32(&started, 1, 0) {
		return
	}
	close(stopCh)
	close(queue)
	workersWG.Wait()
	if spool != nil {
		spool.close()
		spool = nil
	}
	log.Printf("audit: shutdown complete (written=%d dropped=%d)", written, dropped)
}

func worker(id int) {
	defer workersWG.Done()
	batch := make([]models.AuditLog, 0, batchSize)
	timer := time.NewTimer(flushInterval)
	if !timer.Stop() {
		<-timer.C
	}

	flush := func() {
		if len(batch) == 0 {
			return
		}
		writeBatch(batch)
		batch = batch[:0]
	}

	for {
		select {
		case entry, ok := <-queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, entry)
			if len(batch) >= batchSize {
				flush()
			}
		case <-timer.C:
			flush()
		}
		if len(batch) > 0 {
			timer.Reset(flushInterval)
		}
	}
}

// writeBatch persists a batch grouped by destination month table so each shard
// receives one multi-row INSERT.
func writeBatch(batch []models.AuditLog) {
	if dbHandle == nil {
		return
	}
	byTable := make(map[string][]models.AuditLog)
	order := make([]string, 0, 4)
	for _, e := range batch {
		table := MonthTableName(e.CreatedAt)
		if _, ok := byTable[table]; !ok {
			order = append(order, table)
		}
		byTable[table] = append(byTable[table], e)
	}

	for _, table := range order {
		rows := byTable[table]
		var lastErr error
		for attempt := 0; attempt < insertAttempts; attempt++ {
			if err := EnsureMonthTable(dbHandle, rows[0].CreatedAt); err != nil {
				lastErr = err
				continue
			}
			// ON CONFLICT DO NOTHING makes spool replays idempotent: an entry
			// committed before a crash but still present in the spool is a no-op.
			lastErr = dbHandle.Session(&gorm.Session{SkipHooks: true}).
				Table(table).
				Clauses(clause.OnConflict{DoNothing: true}).
				CreateInBatches(&rows, batchSize).Error
			if lastErr == nil {
				break
			}
			// Shard dropped while running (or a stale ensure-cache entry):
			// evict so the next attempt recreates it.
			if strings.Contains(strings.ToLower(lastErr.Error()), "no such table") ||
				strings.Contains(strings.ToLower(lastErr.Error()), "does not exist") {
				ensuredTables.Delete(table)
			}
		}
		if lastErr != nil {
			atomic.AddInt64(&writeFail, int64(len(rows)))
			log.Printf("audit: failed to write %d entries to %s: %v", len(rows), table, lastErr)
		} else {
			atomic.AddInt64(&written, int64(len(rows)))
			if spool != nil {
				ids := make([]uuid.UUID, len(rows))
				for i := range rows {
					ids[i] = rows[i].ID
				}
				spool.ack(ids)
			}
		}
	}
}
