package services

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"truerp/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ProgressFunc is invoked by a migration runner after each row to report
// per-row progress. current is 1-based, total is the total number of rows
// (0 if not yet known), and imported is the running count of rows that
// have been successfully imported so far.
type ProgressFunc func(current, total, imported int)

// MigrationRunner executes a single import kind. It parses the uploaded
// content, processes rows (calling progress after each row), and returns a
// JSON-serializable result map, a list of per-row error strings, and a
// fatal error if the whole import failed before rows could be processed.
type MigrationRunner func(userID uuid.UUID, content []byte, options map[string]string, progress ProgressFunc) (result map[string]interface{}, errs []string, err error)

// -----------------------------------------------------------------------------
// Runner registry
// -----------------------------------------------------------------------------

var (
	runnersMu sync.RWMutex
	runners   = map[string]MigrationRunner{}
)

// RegisterMigrationRunner associates an import kind (e.g. "parties",
// "stock-summary", "mybillbook") with the runner that performs it.
// Registrations happen at package init time from the controllers package.
func RegisterMigrationRunner(kind string, runner MigrationRunner) {
	runnersMu.Lock()
	defer runnersMu.Unlock()
	runners[kind] = runner
}

func lookupRunner(kind string) (MigrationRunner, bool) {
	runnersMu.RLock()
	defer runnersMu.RUnlock()
	r, ok := runners[kind]
	return r, ok
}

// -----------------------------------------------------------------------------
// Live (in-memory) job state
//
// The worker updates an in-memory copy of the job on every progress tick
// (cheap, mutex-protected). The DB is updated on a throttled cadence (at most
// once per second) and on completion, so polling stays fast even for large
// imports without flooding the database with writes.
// -----------------------------------------------------------------------------

type liveJob struct {
	mu          sync.Mutex
	job         models.MigrationJob
	lastDBWrite time.Time
}

var (
	liveMu   sync.Mutex
	liveJobs = map[uuid.UUID]*liveJob{}
)

// storeLive registers a job as actively running.
func storeLive(jobID uuid.UUID, job models.MigrationJob) *liveJob {
	lj := &liveJob{job: job}
	liveMu.Lock()
	liveJobs[jobID] = lj
	liveMu.Unlock()
	return lj
}

// dropLive removes a job from the in-memory registry (called when the worker
// finishes). A short grace window is not needed because the final state is
// persisted to the DB before dropLive is called.
func dropLive(jobID uuid.UUID) {
	liveMu.Lock()
	delete(liveJobs, jobID)
	liveMu.Unlock()
}

// lookupLive returns the live state for a running job, or nil if the job is
// not currently in memory (e.g. it already finished or the server restarted).
func lookupLive(jobID uuid.UUID) *liveJob {
	liveMu.Lock()
	defer liveMu.Unlock()
	return liveJobs[jobID]
}

// -----------------------------------------------------------------------------
// Enqueue / status API
// -----------------------------------------------------------------------------

// EnqueueMigrationJob creates a MigrationJob row in the database and launches
// a background goroutine that runs the registered runner for the given kind.
// Returns the new job ID. Returns an error if no runner is registered for
// the kind.
func EnqueueMigrationJob(db *gorm.DB, userID uuid.UUID, kind, fileName string, content []byte, options map[string]string) (uuid.UUID, error) {
	runner, ok := lookupRunner(kind)
	if !ok {
		return uuid.Nil, fmt.Errorf("unknown migration kind %q", kind)
	}

	optsJSON, _ := json.Marshal(options)

	job := models.MigrationJob{
		ID:        uuid.New(),
		UserID:    userID,
		Kind:      kind,
		FileName:  fileName,
		Options:   string(optsJSON),
		Status:    "queued",
		TotalRows: 0,
	}
	if err := db.Create(&job).Error; err != nil {
		return uuid.Nil, fmt.Errorf("failed to create migration job: %w", err)
	}

	lj := storeLive(job.ID, job)

	// Keep the file content in memory for the worker. For very large uploads
	// this is acceptable because the request already held the full body; we
	// just retain it until the worker has consumed it.
	go runMigrationJob(db, job.ID, userID, kind, content, options, runner, lj)

	return job.ID, nil
}

// runMigrationJob is the worker goroutine. It runs the runner with a progress
// callback that updates the in-memory state and throttles DB writes.
func runMigrationJob(db *gorm.DB, jobID, userID uuid.UUID, kind string, content []byte, options map[string]string, runner MigrationRunner, lj *liveJob) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("migration job %s (%s): PANIC recovered: %v", jobID, kind, r)
			lj.mu.Lock()
			lj.job.Status = "failed"
			lj.job.FailureReason = fmt.Sprintf("internal error: %v", r)
			now := time.Now()
			lj.job.FinishedAt = &now
			lj.mu.Unlock()
			persistJob(db, lj)
		}
		dropLive(jobID)
	}()

	// Mark as running.
	now := time.Now()
	lj.mu.Lock()
	lj.job.Status = "running"
	lj.job.StartedAt = &now
	lj.job.Step = kind
	lj.mu.Unlock()
	persistJob(db, lj)

	progress := func(current, total, imported int) {
		lj.mu.Lock()
		lj.job.CurrentRow = current
		if total > 0 {
			lj.job.TotalRows = total
		}
		if imported > 0 {
			lj.job.Imported = imported
		}
		lj.mu.Unlock()
		// Throttle DB writes to at most once per second.
		lj.mu.Lock()
		shouldWrite := time.Since(lj.lastDBWrite) > time.Second
		if shouldWrite {
			lj.lastDBWrite = time.Now()
		}
		lj.mu.Unlock()
		if shouldWrite {
			persistJob(db, lj)
		}
	}

	result, errs, err := runner(userID, content, options, progress)

	finishedAt := time.Now()
	lj.mu.Lock()
	lj.job.FinishedAt = &finishedAt
	lj.job.CurrentRow = lj.job.TotalRows
	if err != nil {
		lj.job.Status = "failed"
		lj.job.FailureReason = err.Error()
	} else {
		lj.job.Status = "completed"
	}
	if result != nil {
		if n, ok := result["imported"].(int); ok && n > 0 {
			lj.job.Imported = n
		}
		if rj, mErr := json.Marshal(result); mErr == nil {
			lj.job.Result = string(rj)
		}
	}
	lj.job.ErrorCount = len(errs)
	if len(errs) > 0 {
		// Cap the persisted error list to avoid blowing up the column for
		// very large failed imports; the full list is still returned to the
		// caller via the runner result.
		cap := errs
		if len(cap) > 200 {
			cap = cap[:200]
		}
		if ej, mErr := json.Marshal(cap); mErr == nil {
			lj.job.Errors = string(ej)
		}
	}
	lj.mu.Unlock()
	persistJob(db, lj)
}

// persistJob writes the current in-memory job state to the database. It reads
// the job under the liveJob mutex so callers do not need to hold it.
func persistJob(db *gorm.DB, lj *liveJob) {
	lj.mu.Lock()
	job := lj.job
	lj.mu.Unlock()
	// Save all progress fields without touching CreatedAt.
	db.Model(&models.MigrationJob{}).Where("id = ?", job.ID).Updates(map[string]interface{}{
		"status":         job.Status,
		"step":           job.Step,
		"current_row":    job.CurrentRow,
		"total_rows":     job.TotalRows,
		"imported":       job.Imported,
		"result":         job.Result,
		"error_count":    job.ErrorCount,
		"errors":         job.Errors,
		"failure_reason": job.FailureReason,
		"started_at":     job.StartedAt,
		"finished_at":    job.FinishedAt,
	})
}

// GetMigrationJob returns the current state of a job. For running jobs it
// returns the live in-memory state (which may be ahead of the DB); for
// finished/queued jobs it reads from the DB. The second return is false if
// no such job exists.
func GetMigrationJob(db *gorm.DB, jobID, userID uuid.UUID) (models.MigrationJob, bool) {
	if lj := lookupLive(jobID); lj != nil {
		lj.mu.Lock()
		job := lj.job
		lj.mu.Unlock()
		if job.UserID != userID {
			return models.MigrationJob{}, false
		}
		return job, true
	}
	var job models.MigrationJob
	if err := db.Where("id = ? AND user_id = ?", jobID, userID).First(&job).Error; err != nil {
		return models.MigrationJob{}, false
	}
	return job, true
}

// ListMigrationJobs returns recent migration jobs for a user, newest first.
func ListMigrationJobs(db *gorm.DB, userID uuid.UUID, limit int) ([]models.MigrationJob, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var jobs []models.MigrationJob
	if err := db.Where("user_id = ?", userID).Order("created_at DESC").Limit(limit).Find(&jobs).Error; err != nil {
		return nil, err
	}
	// Overlay live state for any jobs that are still running.
	for i := range jobs {
		if lj := lookupLive(jobs[i].ID); lj != nil {
			lj.mu.Lock()
			jobs[i] = lj.job
			lj.mu.Unlock()
		}
	}
	return jobs, nil
}
