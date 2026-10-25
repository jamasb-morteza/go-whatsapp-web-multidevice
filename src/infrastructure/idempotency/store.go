package idempotency

import (
	"database/sql"
	"fmt"
	sqlite "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/sqlite"
)

// Store records a claim before an external side effect. Pending claims survive
// crashes and are deliberately not replayed: delivery may already have happened.
type Store struct{ db *sql.DB }
type Result struct {
	Fresh  bool
	State  string
	Status int
	Body   []byte
}

func Open(path string) (*Store, error) {
	db, err := sql.Open(sqlite.DriverName, sqlite.FormatChatStorageURI(path, true, false))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS send_operations(operation_key TEXT PRIMARY KEY, fingerprint TEXT NOT NULL, state TEXT NOT NULL, status INTEGER NOT NULL DEFAULT 0, response BLOB, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Claim(key, fingerprint string) (Result, error) {
	inserted, err := s.db.Exec(`INSERT OR IGNORE INTO send_operations(operation_key,fingerprint,state) VALUES(?,?,'pending')`, key, fingerprint)
	if err != nil {
		return Result{}, err
	}
	n, err := inserted.RowsAffected()
	if err != nil {
		return Result{}, err
	}
	if n == 1 {
		return Result{Fresh: true, State: "pending"}, nil
	}
	var actual string
	var r Result
	err = s.db.QueryRow(`SELECT fingerprint,state,status,response FROM send_operations WHERE operation_key=?`, key).Scan(&actual, &r.State, &r.Status, &r.Body)
	if err != nil {
		return Result{}, err
	}
	if actual != fingerprint {
		return Result{}, fmt.Errorf("idempotency key already used for different content")
	}
	return r, nil
}
func (s *Store) Complete(key string, status int, body []byte) error {
	_, err := s.db.Exec(`UPDATE send_operations SET state='complete',status=?,response=? WHERE operation_key=? AND state='pending'`, status, body, key)
	return err
}
