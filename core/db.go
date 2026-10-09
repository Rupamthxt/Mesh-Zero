package core

import (
	"database/sql"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var (
	dbInstance *sql.DB
	dbMu       sync.Mutex
)

// InitDB initializes the SQLite database and runs migrations
func InitDB() error {
	dbMu.Lock()
	defer dbMu.Unlock()

	if dbInstance != nil {
		return nil
	}

	db, err := sql.Open("sqlite", "emdash.db")
	if err != nil {
		return fmt.Errorf("failed to open sqlite db: %v", err)
	}

	// Optimize SQLite performance settings
	_, _ = db.Exec("PRAGMA journal_mode=WAL;")
	_, _ = db.Exec("PRAGMA busy_timeout=5000;")
	_, _ = db.Exec("PRAGMA synchronous=NORMAL;")

	// Create tables
	queries := []string{
		`CREATE TABLE IF NOT EXISTS balances (
			id TEXT PRIMARY KEY,
			balance REAL NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS extraction_tasks (
			id INTEGER PRIMARY KEY,
			url TEXT NOT NULL,
			country TEXT,
			extract_script TEXT,
			status TEXT NOT NULL,
			status_code INTEGER DEFAULT 0,
			latency_ms INTEGER DEFAULT 0,
			result_json TEXT,
			receipt_json TEXT,
			error TEXT,
			created_at INTEGER NOT NULL,
			parent_id INTEGER DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS payout_requests (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			worker_id TEXT NOT NULL,
			amount REAL NOT NULL,
			status TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			key TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			credits REAL NOT NULL DEFAULT 1000.0,
			created_at INTEGER NOT NULL,
			is_active INTEGER NOT NULL DEFAULT 1
		);`,
	}

	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			db.Close()
			return fmt.Errorf("migration failure: %v", err)
		}
	}

	// Seed default trial demo API key if not present
	_, _ = db.Exec(`
		INSERT OR IGNORE INTO api_keys (key, name, credits, created_at, is_active)
		VALUES ('emdash_trial_demo', 'Default Trial Playground Key', 1000.0, ?, 1)
	`, time.Now().UnixNano())

	dbInstance = db
	fmt.Println("[DATABASE] SQLite database initialized successfully (WAL mode enabled).")
	return nil
}

// CloseDB closes the database instance safely
func CloseDB() {
	dbMu.Lock()
	defer dbMu.Unlock()
	if dbInstance != nil {
		dbInstance.Close()
		dbInstance = nil
	}
}

// GetBalanceDB retrieves the balance of an account (defaults to 100.0 if not found)
func GetBalanceDB(id string) float64 {
	if dbInstance == nil {
		return 0.0
	}
	var bal float64
	err := dbInstance.QueryRow("SELECT balance FROM balances WHERE id = ?", id).Scan(&bal)
	if err == sql.ErrNoRows {
		_ = SetBalanceDB(id, 100.0)
		return 100.0
	}
	if err != nil {
		return 0.0
	}
	return bal
}

// SetBalanceDB updates or inserts the balance of an account
func SetBalanceDB(id string, bal float64) error {
	if dbInstance == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := dbInstance.Exec(`
		INSERT INTO balances (id, balance) 
		VALUES (?, ?) 
		ON CONFLICT(id) DO UPDATE SET balance = excluded.balance
	`, id, bal)
	return err
}

type TaskRecord struct {
	ID            uint64 `json:"id"`
	URL           string `json:"url"`
	Country       string `json:"country"`
	ExtractScript string `json:"extract_script"`
	Status        string `json:"status"`
	StatusCode    int    `json:"status_code"`
	LatencyMs     int64  `json:"latency_ms"`
	ResultJSON    string `json:"result_json"`
	ReceiptJSON   string `json:"receipt_json"`
	Error         string `json:"error"`
	CreatedAt     int64  `json:"created_at"`
	ParentID      int64  `json:"parent_id"`
}

// CreateTaskDB inserts a new extraction task record in pending state
func CreateTaskDB(id uint64, targetURL, country, extractScript string, parentID int64) error {
	if dbInstance == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := dbInstance.Exec(`
		INSERT INTO extraction_tasks (id, url, country, extract_script, status, created_at, parent_id)
		VALUES (?, ?, ?, ?, 'pending', ?, ?)
	`, id, targetURL, country, extractScript, time.Now().UnixNano(), parentID)
	return err
}

// UpdateTaskDB updates a task record with extracted results
func UpdateTaskDB(id uint64, status string, statusCode int, latencyMs int64, resultJSON []byte, receiptJSON []byte, errStr string) error {
	if dbInstance == nil {
		return fmt.Errorf("db not initialized")
	}
	var resStr, receiptStr string
	if len(resultJSON) > 0 {
		resStr = string(resultJSON)
	}
	if len(receiptJSON) > 0 {
		receiptStr = string(receiptJSON)
	}

	_, err := dbInstance.Exec(`
		UPDATE extraction_tasks 
		SET status = ?, status_code = ?, latency_ms = ?, result_json = ?, receipt_json = ?, error = ?
		WHERE id = ?
	`, status, statusCode, latencyMs, resStr, receiptStr, errStr, id)
	return err
}

// GetTaskDB fetches an extraction task record by ID
func GetTaskDB(id uint64) (*TaskRecord, error) {
	if dbInstance == nil {
		return nil, fmt.Errorf("db not initialized")
	}

	var t TaskRecord
	var resJSON, receiptJSON, errStr, country, extractScript sql.NullString

	err := dbInstance.QueryRow(`
		SELECT id, url, country, extract_script, status, status_code, latency_ms, result_json, receipt_json, error, created_at, parent_id
		FROM extraction_tasks WHERE id = ?
	`, id).Scan(&t.ID, &t.URL, &country, &extractScript, &t.Status, &t.StatusCode, &t.LatencyMs, &resJSON, &receiptJSON, &errStr, &t.CreatedAt, &t.ParentID)

	if err != nil {
		return nil, err
	}

	if country.Valid {
		t.Country = country.String
	}
	if extractScript.Valid {
		t.ExtractScript = extractScript.String
	}
	if resJSON.Valid {
		t.ResultJSON = resJSON.String
	}
	if receiptJSON.Valid {
		t.ReceiptJSON = receiptJSON.String
	}
	if errStr.Valid {
		t.Error = errStr.String
	}

	return &t, nil
}

// GetBatchTasksDB fetches all sub-tasks belonging to a parent batch job
func GetBatchTasksDB(parentID int64) ([]TaskRecord, error) {
	if dbInstance == nil {
		return nil, fmt.Errorf("db not initialized")
	}

	rows, err := dbInstance.Query(`
		SELECT id, url, country, extract_script, status, status_code, latency_ms, result_json, receipt_json, error, created_at, parent_id
		FROM extraction_tasks WHERE parent_id = ?
	`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []TaskRecord
	for rows.Next() {
		var t TaskRecord
		var resJSON, receiptJSON, errStr, country, extractScript sql.NullString

		err := rows.Scan(&t.ID, &t.URL, &country, &extractScript, &t.Status, &t.StatusCode, &t.LatencyMs, &resJSON, &receiptJSON, &errStr, &t.CreatedAt, &t.ParentID)
		if err != nil {
			return nil, err
		}

		if country.Valid {
			t.Country = country.String
		}
		if extractScript.Valid {
			t.ExtractScript = extractScript.String
		}
		if resJSON.Valid {
			t.ResultJSON = resJSON.String
		}
		if receiptJSON.Valid {
			t.ReceiptJSON = receiptJSON.String
		}
		if errStr.Valid {
			t.Error = errStr.String
		}
		records = append(records, t)
	}

	return records, nil
}

type PayoutRequestRecord struct {
	ID        int64   `json:"id"`
	WorkerID  string  `json:"worker_id"`
	Amount    float64 `json:"amount"`
	Status    string  `json:"status"`
	CreatedAt int64   `json:"created_at"`
}

// CreatePayoutRequestDB inserts a new payout request
func CreatePayoutRequestDB(workerID string, amount float64) error {
	if dbInstance == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := dbInstance.Exec(`
		INSERT INTO payout_requests (worker_id, amount, status, created_at)
		VALUES (?, ?, 'pending', ?)
	`, workerID, amount, time.Now().UnixNano())
	return err
}

// GetPayoutRequestsDB retrieves all payout requests
func GetPayoutRequestsDB() ([]PayoutRequestRecord, error) {
	if dbInstance == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	rows, err := dbInstance.Query("SELECT id, worker_id, amount, status, created_at FROM payout_requests ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []PayoutRequestRecord
	for rows.Next() {
		var r PayoutRequestRecord
		if err := rows.Scan(&r.ID, &r.WorkerID, &r.Amount, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, nil
}

// UpdatePayoutRequestStatusDB updates a payout request status
func UpdatePayoutRequestStatusDB(id int64, status string) error {
	if dbInstance == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := dbInstance.Exec("UPDATE payout_requests SET status = ? WHERE id = ?", status, id)
	return err
}

type APIKeyRecord struct {
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	Credits   float64 `json:"credits"`
	CreatedAt int64   `json:"created_at"`
	IsActive  bool    `json:"is_active"`
}

// CreateAPIKeyDB inserts a new API key with initial credits
func CreateAPIKeyDB(key, name string, initialCredits float64) error {
	if dbInstance == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := dbInstance.Exec(`
		INSERT INTO api_keys (key, name, credits, created_at, is_active)
		VALUES (?, ?, ?, ?, 1)
	`, key, name, initialCredits, time.Now().UnixNano())
	return err
}

// GetAPIKeyDB retrieves an API key by key string
func GetAPIKeyDB(key string) (*APIKeyRecord, error) {
	if dbInstance == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	var r APIKeyRecord
	var isActiveInt int
	err := dbInstance.QueryRow(`
		SELECT key, name, credits, created_at, is_active
		FROM api_keys WHERE key = ?
	`, key).Scan(&r.Key, &r.Name, &r.Credits, &r.CreatedAt, &isActiveInt)
	if err != nil {
		return nil, err
	}
	r.IsActive = (isActiveInt == 1)
	return &r, nil
}

// DeductAPIKeyCreditDB decrements an API key's balance by specified amount
func DeductAPIKeyCreditDB(key string, amount float64) error {
	if dbInstance == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := dbInstance.Exec(`
		UPDATE api_keys
		SET credits = credits - ?
		WHERE key = ? AND credits >= ?
	`, amount, key, amount)
	return err
}

// ListAPIKeysDB returns all registered API keys
func ListAPIKeysDB() ([]APIKeyRecord, error) {
	if dbInstance == nil {
		return nil, fmt.Errorf("db not initialized")
	}
	rows, err := dbInstance.Query("SELECT key, name, credits, created_at, is_active FROM api_keys ORDER BY created_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []APIKeyRecord
	for rows.Next() {
		var r APIKeyRecord
		var isActiveInt int
		if err := rows.Scan(&r.Key, &r.Name, &r.Credits, &r.CreatedAt, &isActiveInt); err != nil {
			return nil, err
		}
		r.IsActive = (isActiveInt == 1)
		records = append(records, r)
	}
	return records, nil
}

