package main

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB

func getDBPath() string {
	return filepath.Join(getConfigDir(), "stats.db")
}

func initDB() error {
	var err error
	db, err = sql.Open("sqlite", getDBPath())
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}

	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return fmt.Errorf("set wal: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy_timeout: %w", err)
	}

	schema := `
CREATE TABLE IF NOT EXISTS requests (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp INTEGER NOT NULL,
	model TEXT NOT NULL,
	provider TEXT NOT NULL,
	client TEXT,
	input_tokens INTEGER NOT NULL,
	output_tokens INTEGER NOT NULL,
	duration_ms INTEGER NOT NULL,
	tokens_per_sec REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_requests_time ON requests(timestamp);
CREATE INDEX IF NOT EXISTS idx_requests_model ON requests(model);
CREATE INDEX IF NOT EXISTS idx_requests_provider ON requests(provider);
CREATE INDEX IF NOT EXISTS idx_requests_client ON requests(client);

CREATE TABLE IF NOT EXISTS tps_snapshots (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp INTEGER NOT NULL,
	model TEXT,
	provider TEXT,
	tps REAL NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tps_time ON tps_snapshots(timestamp);
`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	// Migration: add client column if it doesn't exist (safe to ignore error)
	_, _ = db.Exec("ALTER TABLE requests ADD COLUMN client TEXT")
	return nil
}

func closeDB() {
	if db != nil {
		db.Close()
		db = nil
	}
}

func dbRecordRequest(req RequestStats) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(
		`INSERT INTO requests (timestamp, model, provider, client, input_tokens, output_tokens, duration_ms, tokens_per_sec)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		req.Timestamp.Unix(), req.Model, req.Provider, req.Client, req.InputTokens, req.OutputTokens, req.DurationMs, req.TokensPerSec,
	)
	if err != nil {
		log.Printf("[DB] failed to record request: %v", err)
	}
	return err
}

func dbRecordTPSSnapshot(model, provider string, tps float64) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(
		`INSERT INTO tps_snapshots (timestamp, model, provider, tps) VALUES (?, ?, ?, ?)`,
		time.Now().Unix(), model, provider, tps,
	)
	if err != nil {
		log.Printf("[DB] failed to record TPS snapshot: %v", err)
	}
	return err
}

type DailyTokens struct {
	Date   string `json:"date"`
	Input  int64  `json:"input"`
	Output int64  `json:"output"`
	Total  int64  `json:"total"`
}

func getDailyTokens(from, to int64, provider, model, client string) ([]DailyTokens, error) {
	if db == nil {
		return nil, nil
	}
	q := `SELECT date(timestamp, 'unixepoch') as day, SUM(input_tokens), SUM(output_tokens)
		  FROM requests
		  WHERE timestamp >= ? AND timestamp <= ?`
	args := []interface{}{from, to}
	if provider != "" {
		q += " AND provider = ?"
		args = append(args, provider)
	}
	if model != "" {
		q += " AND model = ?"
		args = append(args, model)
	}
	if client != "" {
		q += " AND COALESCE(NULLIF(client, ''), 'Unknown') = ?"
		args = append(args, client)
	}
	q += " GROUP BY day ORDER BY day"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []DailyTokens
	for rows.Next() {
		var d DailyTokens
		if err := rows.Scan(&d.Date, &d.Input, &d.Output); err != nil {
			continue
		}
		d.Total = d.Input + d.Output
		result = append(result, d)
	}
	return result, rows.Err()
}

type MonthlyTokens struct {
	Month  string `json:"month"`
	Input  int64  `json:"input"`
	Output int64  `json:"output"`
	Total  int64  `json:"total"`
}

func getMonthlyTokens(client string) ([]MonthlyTokens, error) {
	if db == nil {
		return nil, nil
	}
	q := `SELECT strftime('%Y-%m', timestamp, 'unixepoch') as month,
		       SUM(input_tokens), SUM(output_tokens)
		FROM requests`
	args := []interface{}{}
	if client != "" {
		q += " WHERE COALESCE(NULLIF(client, ''), 'Unknown') = ?"
		args = append(args, client)
	}
	q += " GROUP BY month ORDER BY month"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MonthlyTokens
	for rows.Next() {
		var m MonthlyTokens
		if err := rows.Scan(&m.Month, &m.Input, &m.Output); err != nil {
			continue
		}
		m.Total = m.Input + m.Output
		result = append(result, m)
	}
	return result, rows.Err()
}

type TPSHistory struct {
	Timestamp int64   `json:"timestamp"`
	Model     string  `json:"model"`
	AvgTPS    float64 `json:"avg_tps"`
}

func getTPSHistory(from, to int64, provider, model, client string) ([]TPSHistory, error) {
	if db == nil {
		return nil, nil
	}
	q := `SELECT (timestamp / 300) * 300 as bucket, model, AVG(tps)
		  FROM tps_snapshots
		  WHERE timestamp >= ? AND timestamp <= ?`
	args := []interface{}{from, to}
	if provider != "" {
		q += " AND provider = ?"
		args = append(args, provider)
	}
	if model != "" {
		q += " AND model = ?"
		args = append(args, model)
	}
	if client != "" {
		q += " AND COALESCE(NULLIF(client, ''), 'Unknown') = ?"
		args = append(args, client)
	}
	q += " GROUP BY bucket, model ORDER BY bucket"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TPSHistory
	for rows.Next() {
		var h TPSHistory
		if err := rows.Scan(&h.Timestamp, &h.Model, &h.AvgTPS); err != nil {
			continue
		}
		result = append(result, h)
	}
	return result, rows.Err()
}

type ModelHistory struct {
	Model        string  `json:"model"`
	Provider     string  `json:"provider"`
	Requests     int     `json:"requests"`
	AvgTPS       float64 `json:"avg_tps"`
	MaxTPS       float64 `json:"max_tps"`
	TotalInput   int64   `json:"total_input"`
	TotalOutput  int64   `json:"total_output"`
}

func getModelHistory(from, to int64, provider, model, client string) ([]ModelHistory, error) {
	if db == nil {
		return nil, nil
	}
	q := `SELECT model, provider, COUNT(*), AVG(tokens_per_sec), MAX(tokens_per_sec),
		       SUM(input_tokens), SUM(output_tokens)
		  FROM requests
		  WHERE timestamp >= ? AND timestamp <= ?`
	args := []interface{}{from, to}
	if provider != "" {
		q += " AND provider = ?"
		args = append(args, provider)
	}
	if model != "" {
		q += " AND model = ?"
		args = append(args, model)
	}
	if client != "" {
		q += " AND COALESCE(NULLIF(client, ''), 'Unknown') = ?"
		args = append(args, client)
	}
	q += " GROUP BY model, provider ORDER BY COUNT(*) DESC"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ModelHistory
	for rows.Next() {
		var m ModelHistory
		if err := rows.Scan(&m.Model, &m.Provider, &m.Requests, &m.AvgTPS, &m.MaxTPS, &m.TotalInput, &m.TotalOutput); err != nil {
			continue
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func clearAllStats() error {
	if db == nil {
		return nil
	}
	if _, err := db.Exec("DELETE FROM requests"); err != nil {
		return err
	}
	if _, err := db.Exec("DELETE FROM tps_snapshots"); err != nil {
		return err
	}
	if _, err := db.Exec("VACUUM"); err != nil {
		return err
	}
	return nil
}

func getDistinctModels() ([]string, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(`SELECT DISTINCT model FROM requests ORDER BY model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			continue
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func getDistinctProviders() ([]string, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(`SELECT DISTINCT provider FROM requests ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			continue
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

type ClientHistory struct {
	Client      string `json:"client"`
	Requests    int    `json:"requests"`
	TotalInput  int64  `json:"total_input"`
	TotalOutput int64  `json:"total_output"`
	TotalTokens int64  `json:"total_tokens"`
}

func getClientHistory(from, to int64, provider, model, client string) ([]ClientHistory, error) {
	if db == nil {
		return nil, nil
	}
	q := `SELECT COALESCE(NULLIF(client, ''), 'Unknown'), COUNT(*), SUM(input_tokens), SUM(output_tokens)
		  FROM requests
		  WHERE timestamp >= ? AND timestamp <= ?`
	args := []interface{}{from, to}
	if provider != "" {
		q += " AND provider = ?"
		args = append(args, provider)
	}
	if model != "" {
		q += " AND model = ?"
		args = append(args, model)
	}
	if client != "" {
		q += " AND COALESCE(NULLIF(client, ''), 'Unknown') = ?"
		args = append(args, client)
	}
	q += " GROUP BY COALESCE(NULLIF(client, ''), 'Unknown') ORDER BY SUM(input_tokens + output_tokens) DESC"

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ClientHistory
	for rows.Next() {
		var c ClientHistory
		if err := rows.Scan(&c.Client, &c.Requests, &c.TotalInput, &c.TotalOutput); err != nil {
			continue
		}
		c.TotalTokens = c.TotalInput + c.TotalOutput
		result = append(result, c)
	}
	return result, rows.Err()
}

func getDistinctClients() ([]string, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(`SELECT DISTINCT COALESCE(NULLIF(client, ''), 'Unknown') FROM requests ORDER BY client`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			continue
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
