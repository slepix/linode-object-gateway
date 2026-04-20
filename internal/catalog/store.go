package catalog

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Entry struct {
	Key      string
	Parent   string
	Name     string
	Size     int64
	ETag     string
	Modified time.Time
	IsDir    bool
}

type Store struct {
	db *sql.DB
}

func NewStore(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000&_synchronous=NORMAL")
	if err != nil {
		return nil, fmt.Errorf("open catalog db: %w", err)
	}

	if _, err := db.Exec(`PRAGMA cache_size = -64000`); err != nil {
		db.Close()
		return nil, fmt.Errorf("set cache_size: %w", err)
	}

	if err := createSchema(db); err != nil {
		db.Close()
		return nil, err
	}

	db.SetMaxOpenConns(1)

	return &Store{db: db}, nil
}

func createSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS objects (
			bucket TEXT NOT NULL,
			key    TEXT NOT NULL,
			parent TEXT NOT NULL,
			name   TEXT NOT NULL,
			size   INTEGER NOT NULL DEFAULT 0,
			etag   TEXT NOT NULL DEFAULT '',
			modified INTEGER NOT NULL DEFAULT 0,
			is_dir INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (bucket, key)
		);

		CREATE INDEX IF NOT EXISTS idx_objects_parent
			ON objects(bucket, parent);

		CREATE TABLE IF NOT EXISTS sync_cursors (
			bucket TEXT NOT NULL,
			prefix TEXT NOT NULL,
			synced_at INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (bucket, prefix)
		);
	`)
	if err != nil {
		return fmt.Errorf("create catalog schema: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) ListDir(bucket, parent string) ([]Entry, error) {
	rows, err := s.db.Query(
		`SELECT key, parent, name, size, etag, modified, is_dir
		 FROM objects WHERE bucket = ? AND parent = ?
		 ORDER BY is_dir DESC, name ASC`,
		bucket, parent,
	)
	if err != nil {
		return nil, fmt.Errorf("list dir: %w", err)
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var modUnix int64
		if err := rows.Scan(&e.Key, &e.Parent, &e.Name, &e.Size, &e.ETag, &modUnix, &e.IsDir); err != nil {
			return nil, fmt.Errorf("scan entry: %w", err)
		}
		e.Modified = time.Unix(modUnix, 0)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *Store) Lookup(bucket, key string) (*Entry, error) {
	var e Entry
	var modUnix int64
	err := s.db.QueryRow(
		`SELECT key, parent, name, size, etag, modified, is_dir
		 FROM objects WHERE bucket = ? AND key = ?`,
		bucket, key,
	).Scan(&e.Key, &e.Parent, &e.Name, &e.Size, &e.ETag, &modUnix, &e.IsDir)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	e.Modified = time.Unix(modUnix, 0)
	return &e, nil
}

func (s *Store) Put(bucket string, e *Entry) error {
	_, err := s.db.Exec(
		`INSERT INTO objects (bucket, key, parent, name, size, etag, modified, is_dir)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(bucket, key) DO UPDATE SET
		   parent=excluded.parent, name=excluded.name, size=excluded.size,
		   etag=excluded.etag, modified=excluded.modified, is_dir=excluded.is_dir`,
		bucket, e.Key, e.Parent, e.Name, e.Size, e.ETag, e.Modified.Unix(), boolToInt(e.IsDir),
	)
	if err != nil {
		return fmt.Errorf("put entry: %w", err)
	}
	return nil
}

func (s *Store) PutBatch(bucket string, entries []Entry) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin batch: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(
		`INSERT INTO objects (bucket, key, parent, name, size, etag, modified, is_dir)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(bucket, key) DO UPDATE SET
		   parent=excluded.parent, name=excluded.name, size=excluded.size,
		   etag=excluded.etag, modified=excluded.modified, is_dir=excluded.is_dir`,
	)
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	defer stmt.Close()

	for i := range entries {
		e := &entries[i]
		if _, err := stmt.Exec(bucket, e.Key, e.Parent, e.Name, e.Size, e.ETag, e.Modified.Unix(), boolToInt(e.IsDir)); err != nil {
			return fmt.Errorf("batch insert: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) Delete(bucket, key string) error {
	_, err := s.db.Exec(`DELETE FROM objects WHERE bucket = ? AND key = ?`, bucket, key)
	if err != nil {
		return fmt.Errorf("delete entry: %w", err)
	}
	return nil
}

func (s *Store) DeletePrefix(bucket, prefix string) error {
	_, err := s.db.Exec(`DELETE FROM objects WHERE bucket = ? AND key LIKE ?`, bucket, prefix+"%")
	if err != nil {
		return fmt.Errorf("delete prefix: %w", err)
	}
	return nil
}

func (s *Store) HasDir(bucket, parent string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM objects WHERE bucket = ? AND parent = ? LIMIT 1`,
		bucket, parent,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("has dir: %w", err)
	}
	return count > 0, nil
}

func (s *Store) GetSyncCursor(bucket, prefix string) (time.Time, error) {
	var ts int64
	err := s.db.QueryRow(
		`SELECT synced_at FROM sync_cursors WHERE bucket = ? AND prefix = ?`,
		bucket, prefix,
	).Scan(&ts)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("get sync cursor: %w", err)
	}
	return time.Unix(ts, 0), nil
}

func (s *Store) SetSyncCursor(bucket, prefix string, t time.Time) error {
	_, err := s.db.Exec(
		`INSERT INTO sync_cursors (bucket, prefix, synced_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(bucket, prefix) DO UPDATE SET synced_at=excluded.synced_at`,
		bucket, prefix, t.Unix(),
	)
	if err != nil {
		return fmt.Errorf("set sync cursor: %w", err)
	}
	return nil
}

func (s *Store) Count(bucket string) (int64, error) {
	var count int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM objects WHERE bucket = ?`, bucket).Scan(&count)
	return count, err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
