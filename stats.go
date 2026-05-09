package main

import (
	"encoding/json"
	"sync"
	"time"
)

// RequestStats holds metrics for a single completed request
type RequestStats struct {
	Model        string    `json:"model"`
	Provider     string    `json:"provider"`
	Client       string    `json:"client"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	DurationMs   int64     `json:"duration_ms"`
	TokensPerSec float64   `json:"tokens_per_sec"`
	Timestamp    time.Time `json:"timestamp"`
}

// LiveStats holds the current live statistics snapshot
type LiveStats struct {
	CurrentModel       string                 `json:"current_model"`
	CurrentProvider    string                 `json:"current_provider"`
	CurrentClient      string                 `json:"current_client"`
	RequestActive      bool                   `json:"request_active"`
	RequestStart       int64                  `json:"request_start,omitempty"`
	LiveTokensReceived int                    `json:"live_tokens_received"`
	LiveTokensPerSec   float64                `json:"live_tokens_per_sec"`
	TotalRequests      int                    `json:"total_requests"`
	TotalInputTokens   int64                  `json:"total_input_tokens"`
	TotalOutputTokens  int64                  `json:"total_output_tokens"`
	AvgTokensPerSec    float64                `json:"avg_tokens_per_sec"`
	RecentRequests     []RequestStats         `json:"recent_requests"`
	ByModel            map[string]*ModelStats `json:"by_model"`
	ByClient           map[string]*ModelStats `json:"by_client"`
}

// ModelStats holds per-model aggregated stats
type ModelStats struct {
	Requests        int     `json:"requests"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	AvgTokensPerSec float64 `json:"avg_tokens_per_sec"`
}

// StatsTracker records and provides request statistics
type StatsTracker struct {
	mu                  sync.Mutex
	recentRequests       []RequestStats
	maxRecent            int
	totalRequests        int
	totalInputTokens     int64
	totalOutputTokens    int64
	byModel              map[string]*ModelStats
	byClient             map[string]*ModelStats
	currentModel         string
	currentProvider      string
	currentClient        string
	requestActive        bool
	requestStart         time.Time
	liveTokensReceived   int
}

// Global stats tracker
var globalStats = NewStatsTracker(50)

func NewStatsTracker(maxRecent int) *StatsTracker {
	return &StatsTracker{
		recentRequests: make([]RequestStats, 0, maxRecent),
		maxRecent:      maxRecent,
		byModel:        make(map[string]*ModelStats),
		byClient:       make(map[string]*ModelStats),
	}
}

// RecordRequest records a completed request's stats
func (st *StatsTracker) RecordRequest(model, provider, client string, inputTokens, outputTokens int, duration time.Duration) {
	st.mu.Lock()
	defer st.mu.Unlock()

	durationMs := duration.Milliseconds()
	tokensPerSec := 0.0
	if duration.Seconds() > 0 && outputTokens > 0 {
		tokensPerSec = float64(outputTokens) / duration.Seconds()
	}

	stats := RequestStats{
		Model:        model,
		Provider:     provider,
		Client:       client,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		DurationMs:   durationMs,
		TokensPerSec: tokensPerSec,
		Timestamp:    time.Now(),
	}

	st.recentRequests = append(st.recentRequests, stats)
	if len(st.recentRequests) > st.maxRecent {
		st.recentRequests = st.recentRequests[1:]
	}

	st.totalRequests++
	st.totalInputTokens += int64(inputTokens)
	st.totalOutputTokens += int64(outputTokens)

	// Persist to SQLite (non-blocking, log on error)
	go dbRecordRequest(stats)

	ms, ok := st.byModel[model]
	if !ok {
		ms = &ModelStats{}
		st.byModel[model] = ms
	}
	ms.Requests++
	ms.InputTokens += int64(inputTokens)
	ms.OutputTokens += int64(outputTokens)
	ms.AvgTokensPerSec = (ms.AvgTokensPerSec*float64(ms.Requests-1) + tokensPerSec) / float64(ms.Requests)

	cs, ok := st.byClient[client]
	if !ok {
		cs = &ModelStats{}
		st.byClient[client] = cs
	}
	cs.Requests++
	cs.InputTokens += int64(inputTokens)
	cs.OutputTokens += int64(outputTokens)
	cs.AvgTokensPerSec = (cs.AvgTokensPerSec*float64(cs.Requests-1) + tokensPerSec) / float64(cs.Requests)
}

// StartRequest marks the beginning of a request for live tracking
func (st *StatsTracker) StartRequest(model, provider, client string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.currentModel = model
	st.currentProvider = provider
	st.currentClient = client
	st.requestActive = true
	st.requestStart = time.Now()
	st.liveTokensReceived = 0
}

// AddTokens increments the live token counter during streaming
func (st *StatsTracker) AddTokens(count int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.liveTokensReceived += count
}

// EndRequest clears the live request state
func (st *StatsTracker) EndRequest() {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.requestActive = false
	st.liveTokensReceived = 0
}

// GetSnapshot returns a snapshot of current stats
func (st *StatsTracker) GetSnapshot() LiveStats {
	st.mu.Lock()
	defer st.mu.Unlock()

	recent := make([]RequestStats, len(st.recentRequests))
	copy(recent, st.recentRequests)

	avgTps := 0.0
	if st.totalRequests > 0 {
		var weightedSum float64
		var totalReqs int
		for _, ms := range st.byModel {
			weightedSum += ms.AvgTokensPerSec * float64(ms.Requests)
			totalReqs += ms.Requests
		}
		if totalReqs > 0 {
			avgTps = weightedSum / float64(totalReqs)
		}
	}

	byModel := make(map[string]*ModelStats)
	for k, v := range st.byModel {
		vc := *v
		byModel[k] = &vc
	}

	byClient := make(map[string]*ModelStats)
	for k, v := range st.byClient {
		vc := *v
		byClient[k] = &vc
	}

	result := LiveStats{
		CurrentModel:       st.currentModel,
		CurrentProvider:    st.currentProvider,
		CurrentClient:      st.currentClient,
		RequestActive:      st.requestActive,
		TotalRequests:      st.totalRequests,
		TotalInputTokens:   st.totalInputTokens,
		TotalOutputTokens:  st.totalOutputTokens,
		AvgTokensPerSec:    avgTps,
		RecentRequests:     recent,
		ByModel:            byModel,
		ByClient:           byClient,
		LiveTokensReceived: st.liveTokensReceived,
	}

	if st.requestActive {
		result.RequestStart = st.requestStart.UnixMilli()
		elapsed := time.Since(st.requestStart).Seconds()
		if elapsed > 0 && st.liveTokensReceived > 0 {
			result.LiveTokensPerSec = float64(st.liveTokensReceived) / elapsed
		}
	}

	return result
}

// StatsToJSON returns the stats as JSON bytes
func StatsToJSON() ([]byte, error) {
	snapshot := globalStats.GetSnapshot()
	return json.Marshal(snapshot)
}
