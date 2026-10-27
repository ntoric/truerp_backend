package audit

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"truerp/models"

	"github.com/google/uuid"
)

// The spool is a local append-only journal that makes the in-memory queue
// survive process crashes and restarts: every enqueued entry is appended as a
// JSON line *before* the channel send; once a batch is committed to the
// database its IDs are acked and a periodic compaction rewrites the file
// without them. On startup the file is replayed into the queue and inserts
// use ON CONFLICT DO NOTHING so replays are idempotent.
//
// Durability scope: a write() survives process death and machine restart
// (page cache), but not necessarily a power loss between write and fsync —
// acceptable for an audit trail.

const (
	spoolCompactInterval = 30 * time.Second
	spoolCompactMaxBytes = 8 << 20 // compact eagerly past 8 MiB
	spoolDefaultName     = "audit_spool.jsonl"
)

type spooler struct {
	mu   sync.Mutex
	f    *os.File
	path string
	size int64

	committed   map[uuid.UUID]struct{}
	committedMu sync.Mutex

	stopCh chan struct{}
	done   chan struct{}
}

func spoolPath() string {
	if p := strings.TrimSpace(os.Getenv("AUDIT_SPOOL_PATH")); p != "" {
		return p
	}
	return filepath.Join("data", spoolDefaultName)
}

func spoolDisabled() bool {
	return strings.EqualFold(os.Getenv("AUDIT_SPOOL_DISABLED"), "1") ||
		strings.EqualFold(os.Getenv("AUDIT_SPOOL_DISABLED"), "true")
}

// openSpool creates/loads the spool file and returns the spooler plus any
// entries recovered from a previous run.
func openSpool() (*spooler, []models.AuditLog) {
	if spoolDisabled() {
		return nil, nil
	}
	s := &spooler{
		path:      spoolPath(),
		committed: map[uuid.UUID]struct{}{},
		stopCh:    make(chan struct{}),
		done:      make(chan struct{}),
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		log.Printf("audit: spool disabled, mkdir %s failed: %v", filepath.Dir(s.path), err)
		return nil, nil
	}

	replayed := s.readEntries()

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("audit: spool disabled, open %s failed: %v", s.path, err)
		return nil, nil
	}
	s.f = f
	if info, err := f.Stat(); err == nil {
		s.size = info.Size()
	}

	go s.runCompactor()
	log.Printf("audit: spool enabled at %s (%d entries recovered)", s.path, len(replayed))
	return s, replayed
}

// readEntries parses every JSON line in the spool file.
func (s *spooler) readEntries() []models.AuditLog {
	f, err := os.Open(s.path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var entries []models.AuditLog
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	bad := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e models.AuditLog
		if err := json.Unmarshal([]byte(line), &e); err != nil || e.ID == uuid.Nil {
			bad++
			continue
		}
		entries = append(entries, e)
	}
	if bad > 0 {
		log.Printf("audit: spool contained %d corrupt lines, skipped", bad)
	}
	return entries
}

// append writes one entry as a JSON line. Callers hold no ordering guarantees.
func (s *spooler) append(e models.AuditLog) {
	if s == nil || s.f == nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.mu.Lock()
	n, err := s.f.Write(append(b, '\n'))
	s.size += int64(n)
	s.mu.Unlock()
	if err != nil {
		log.Printf("audit: spool write failed: %v", err)
	}
}

// ack marks entries as durably committed to the database. Their spool lines
// are removed by the next compaction.
func (s *spooler) ack(ids []uuid.UUID) {
	if s == nil {
		return
	}
	s.committedMu.Lock()
	for _, id := range ids {
		s.committed[id] = struct{}{}
	}
	s.committedMu.Unlock()
}

// runCompactor periodically rewrites the spool without committed entries.
// Runs until stopCh closes, then does a final pass.
func (s *spooler) runCompactor() {
	defer close(s.done)
	ticker := time.NewTicker(spoolCompactInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.maybeCompact(false)
		case <-s.stopCh:
			s.compact()
			return
		}
	}
}

func (s *spooler) maybeCompact(force bool) {
	s.committedMu.Lock()
	hasCommitted := len(s.committed) > 0
	s.committedMu.Unlock()
	s.mu.Lock()
	oversized := s.size > spoolCompactMaxBytes
	s.mu.Unlock()
	if force || oversized || hasCommitted {
		s.compact()
	}
}

// compact rewrites the spool keeping only uncommitted entries.
func (s *spooler) compact() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.committedMu.Lock()
	committed := s.committed
	s.committed = map[uuid.UUID]struct{}{}
	s.committedMu.Unlock()
	if len(committed) == 0 {
		return
	}

	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), "audit_spool_*.tmp")
	if err != nil {
		f.Close()
		return
	}
	tmpPath := tmp.Name()
	w := bufio.NewWriter(tmp)

	var kept, dropped, bad int64
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var probe struct {
			ID uuid.UUID `json:"id"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			bad++
			continue
		}
		if _, ok := committed[probe.ID]; ok {
			dropped++
			continue
		}
		w.Write(line)
		w.WriteByte('\n')
		kept++
	}
	f.Close()
	w.Flush()
	tmp.Close()

	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath)
		log.Printf("audit: spool compaction rename failed: %v", err)
		return
	}
	// Reopen the (new) file for appends.
	if s.f != nil {
		s.f.Close()
	}
	nf, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		s.f = nil
		return
	}
	s.f = nf
	if info, err := nf.Stat(); err == nil {
		s.size = info.Size()
	}
	if dropped > 0 {
		log.Printf("audit: spool compacted, %d committed lines dropped, %d kept", dropped, kept)
	}
}

func (s *spooler) close() {
	if s == nil {
		return
	}
	close(s.stopCh)
	<-s.done
	s.mu.Lock()
	if s.f != nil {
		s.f.Close()
		s.f = nil
	}
	s.mu.Unlock()
}
