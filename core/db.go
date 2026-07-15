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

	db, err := sql.Open("sqlite", "mesh_zero.db")
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
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY,
			wasm_bytes BLOB NOT NULL,
			data_bytes BLOB NOT NULL,
			status TEXT NOT NULL,
			stdout BLOB,
			receipt_json TEXT,
			error TEXT,
			tier INTEGER NOT NULL,
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
	}

	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			db.Close()
			return fmt.Errorf("migration failure: %v", err)
		}
	}

	// Schema migration fallback: add parent_id if table already exists
	_, _ = db.Exec("ALTER TABLE tasks ADD COLUMN parent_id INTEGER DEFAULT 0;")

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
		// Auto-initialize balance to 100.0 for new users in prototype
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

// CreateTaskDB inserts a new task record in pending state
func CreateTaskDB(id uint64, wasmBytes []byte, dataBytes []byte, tier int) error {
	return CreateTaskDBWithParent(id, wasmBytes, dataBytes, tier, 0)
}

// CreateTaskDBWithParent inserts a new task record with a batch parent ID
func CreateTaskDBWithParent(id uint64, wasmBytes []byte, dataBytes []byte, tier int, parentID int64) error {
	if dbInstance == nil {
		return fmt.Errorf("db not initialized")
	}
	_, err := dbInstance.Exec(`
		INSERT INTO tasks (id, wasm_bytes, data_bytes, status, tier, created_at, parent_id)
		VALUES (?, ?, ?, 'pending', ?, ?, ?)
	`, id, wasmBytes, dataBytes, tier, time.Now().UnixNano(), parentID)
	return err
}

// UpdateTaskDB updates a task record with result data and status
func UpdateTaskDB(id uint64, status string, stdout []byte, receiptJSON []byte, errStr string) error {
	if dbInstance == nil {
		return fmt.Errorf("db not initialized")
	}
	var receipt interface{}
	if len(receiptJSON) > 0 {
		receipt = string(receiptJSON)
	}

	_, err := dbInstance.Exec(`
		UPDATE tasks 
		SET status = ?, stdout = ?, receipt_json = ?, error = ?
		WHERE id = ?
	`, status, stdout, receipt, errStr, id)
	return err
}

type TaskRecord struct {
	ID          uint64
	WasmBytes   []byte
	DataBytes   []byte
	Status      string
	Stdout      []byte
	ReceiptJSON string
	Error       string
	Tier        int
	CreatedAt   int64
	ParentID    int64
}

// GetTaskDB fetches a task record by ID
func GetTaskDB(id uint64) (*TaskRecord, error) {
	if dbInstance == nil {
		return nil, fmt.Errorf("db not initialized")
	}

	var t TaskRecord
	var stdout []byte
	var receipt sql.NullString
	var errStr sql.NullString

	err := dbInstance.QueryRow(`
		SELECT id, wasm_bytes, data_bytes, status, stdout, receipt_json, error, tier, created_at, parent_id
		FROM tasks WHERE id = ?
	`, id).Scan(&t.ID, &t.WasmBytes, &t.DataBytes, &t.Status, &stdout, &receipt, &errStr, &t.Tier, &t.CreatedAt, &t.ParentID)

	if err != nil {
		return nil, err
	}

	t.Stdout = stdout
	if receipt.Valid {
		t.ReceiptJSON = receipt.String
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
		SELECT id, wasm_bytes, data_bytes, status, stdout, receipt_json, error, tier, created_at, parent_id
		FROM tasks WHERE parent_id = ?
	`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []TaskRecord
	for rows.Next() {
		var t TaskRecord
		var stdout []byte
		var receipt sql.NullString
		var errStr sql.NullString

		err := rows.Scan(&t.ID, &t.WasmBytes, &t.DataBytes, &t.Status, &stdout, &receipt, &errStr, &t.Tier, &t.CreatedAt, &t.ParentID)
		if err != nil {
			return nil, err
		}

		t.Stdout = stdout
		if receipt.Valid {
			t.ReceiptJSON = receipt.String
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
