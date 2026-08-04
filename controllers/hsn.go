package controllers

import (
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/gob"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/nlpodyssey/cybertron/pkg/models/bert"
	"github.com/nlpodyssey/cybertron/pkg/tasks"
	"github.com/nlpodyssey/cybertron/pkg/tasks/textencoding"
)

// HSNResult represents a single HSN search result
type HSNResult struct {
	HSNCode     string  `json:"hsn_code"`
	Description string  `json:"description"`
	Similarity  float64 `json:"similarity"`
}

// hsnEntry holds one row from the CSV plus its embedding
type hsnEntry struct {
	HSNCode     string
	Description string
	Embedding   []float32
}

// HSNEmbedder manages the embedding model and HSN code index
type HSNEmbedder struct {
	model   textencoding.Interface
	entries []hsnEntry
	mu      sync.RWMutex
	ready   bool
}

var (
	hsnEmbedder     *HSNEmbedder
	hsnEmbedderOnce sync.Once
)

// GetHSNEmbedder returns the singleton HSNEmbedder, initializing on first call
func GetHSNEmbedder() *HSNEmbedder {
	hsnEmbedderOnce.Do(func() {
		e := &HSNEmbedder{}
		if err := e.init(); err != nil {
			fmt.Printf("[HSN] Failed to initialize embedder: %v\n", err)
		}
		hsnEmbedder = e
	})
	return hsnEmbedder
}

func (e *HSNEmbedder) init() error {
	if err := e.loadModel(); err != nil {
		return fmt.Errorf("load model: %w", err)
	}
	if err := e.loadHSNData(); err != nil {
		return fmt.Errorf("load HSN data: %w", err)
	}
	return nil
}

func (e *HSNEmbedder) loadModel() error {
	modelsDir := filepath.Join("..", "models")
	// Try BGE first, then fallback to cybertron default
	modelNames := []string{
		"BAAI/bge-small-en-v1.5",
		"sentence-transformers/all-MiniLM-L6-v2",
	}

	var lastErr error
	for _, name := range modelNames {
		fmt.Printf("[HSN] Loading model %s ...\n", name)
		m, err := tasks.Load[textencoding.Interface](&tasks.Config{
			ModelsDir:         modelsDir,
			ModelName:         name,
			DownloadPolicy:    tasks.DownloadMissing,
			ConversionPolicy:  tasks.ConvertMissing,
			ConversionPrecision: tasks.F32,
		})
		if err == nil {
			e.model = m
			fmt.Printf("[HSN] Model %s loaded successfully\n", name)
			return nil
		}
		lastErr = err
		fmt.Printf("[HSN] Model %s failed: %v\n", name, err)
	}
	return lastErr
}

func (e *HSNEmbedder) loadHSNData() error {
	csvPath := filepath.Join("..", "HSN_DATASET.csv")
	cachePath := filepath.Join("..", "data", "hsn_embeddings.gob.gz")

	// Try loading from cache first
	if _, err := os.Stat(cachePath); err == nil {
		fmt.Println("[HSN] Loading embeddings from cache...")
		if err := e.loadFromCache(cachePath); err == nil {
			fmt.Printf("[HSN] Loaded %d HSN entries from cache\n", len(e.entries))
			e.ready = true
			return nil
		}
		fmt.Printf("[HSN] Cache load failed: %v, regenerating...\n", err)
	}

	// Load CSV
	fmt.Println("[HSN] Loading HSN CSV...")
	entries, err := e.loadCSV(csvPath)
	if err != nil {
		return err
	}
	fmt.Printf("[HSN] Loaded %d HSN entries from CSV\n", len(entries))

	// Compute embeddings
	fmt.Println("[HSN] Computing embeddings (this may take a few minutes)...")
	for i := range entries {
		emb, err := e.encode(entries[i].Description)
		if err != nil {
			fmt.Printf("[HSN] Warning: failed to encode '%s': %v\n", entries[i].Description, err)
			continue
		}
		entries[i].Embedding = emb
		if (i+1)%1000 == 0 {
			fmt.Printf("[HSN] Computed %d/%d embeddings\n", i+1, len(entries))
		}
	}
	e.entries = entries
	e.ready = true

	// Save cache
	if err := e.saveToCache(cachePath); err != nil {
		fmt.Printf("[HSN] Cache save failed: %v\n", err)
	} else {
		fmt.Println("[HSN] Embeddings cached to disk")
	}

	return nil
}

func (e *HSNEmbedder) loadCSV(path string) ([]hsnEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	var entries []hsnEntry
	for i, record := range records {
		if i == 0 {
			continue // skip header
		}
		if len(record) < 2 {
			continue
		}
		hsnCode := strings.TrimSpace(record[0])
		description := strings.TrimSpace(record[1])
		if hsnCode == "" || description == "" {
			continue
		}
		entries = append(entries, hsnEntry{
			HSNCode:     hsnCode,
			Description: description,
		})
	}
	return entries, nil
}

func (e *HSNEmbedder) loadFromCache(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	dec := gob.NewDecoder(gz)
	return dec.Decode(&e.entries)
}

func (e *HSNEmbedder) saveToCache(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	enc := gob.NewEncoder(gw)
	if err := enc.Encode(e.entries); err != nil {
		return err
	}
	return gw.Close()
}

func (e *HSNEmbedder) encode(text string) ([]float32, error) {
	resp, err := e.model.Encode(context.Background(), text, int(bert.ClsTokenPooling))
	if err != nil {
		return nil, err
	}

	// Extract float32 values from mat.Matrix
	var data []float32
	fs := resp.Vector.Data()
	if fs.BitSize() == 32 {
		data = fs.F32()
	} else {
		f64 := fs.F64()
		data = make([]float32, len(f64))
		for i, v := range f64 {
			data[i] = float32(v)
		}
	}

	// Copy to avoid reference issues
	result := make([]float32, len(data))
	copy(result, data)

	// L2 normalize
	norm := float32(0)
	for _, v := range result {
		norm += v * v
	}
	norm = float32(math.Sqrt(float64(norm)))
	if norm > 1e-8 {
		for i := range result {
			result[i] /= norm
		}
	}
	return result, nil
}

func (e *HSNEmbedder) Search(query string, topK int) []HSNResult {
	e.mu.RLock()
	defer e.mu.RUnlock()

	if !e.ready || len(e.entries) == 0 {
		return nil
	}

	queryEmb, err := e.encode(query)
	if err != nil {
		return nil
	}

	type scored struct {
		idx  int
		sim  float64
	}
	scores := make([]scored, 0, len(e.entries))

	for i, entry := range e.entries {
		if len(entry.Embedding) == 0 {
			continue
		}
		sim := cosineSimilarity(queryEmb, entry.Embedding)
		scores = append(scores, scored{idx: i, sim: sim})
	}

	sort.Slice(scores, func(a, b int) bool {
		return scores[a].sim > scores[b].sim
	})

	if topK > len(scores) {
		topK = len(scores)
	}

	results := make([]HSNResult, 0, topK)
	for i := 0; i < topK; i++ {
		entry := e.entries[scores[i].idx]
		results = append(results, HSNResult{
			HSNCode:     entry.HSNCode,
			Description: entry.Description,
			Similarity:  scores[i].sim,
		})
	}
	return results
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot // vectors are already normalized
}

// SearchHSNHandler handles POST /api/v1/hsn/search
func SearchHSNHandler(c *gin.Context) {
	var req struct {
		Description string `json:"description" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "message": err.Error()})
		return
	}

	embedder := GetHSNEmbedder()
	results := embedder.Search(req.Description, 1)

	if len(results) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"status":      "not_found",
			"hsn":         "",
			"actual_name": "",
		})
		return
	}

	best := results[0]
	c.JSON(http.StatusOK, gin.H{
		"status":      "success",
		"hsn":         best.HSNCode,
		"actual_name": best.Description,
	})
}

// Health check endpoint for HSN embedder
func HSNHealthHandler(c *gin.Context) {
	embedder := GetHSNEmbedder()
	embedder.mu.RLock()
	ready := embedder.ready
	count := len(embedder.entries)
	embedder.mu.RUnlock()

	c.JSON(http.StatusOK, gin.H{
		"ready":        ready,
		"hsn_count":    count,
		"model_loaded": embedder.model != nil,
	})
}
