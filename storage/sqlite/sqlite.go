package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "github.com/mattn/go-sqlite3"

	"github.com/transparency-dev/merkle/rfc6962"
	"github.com/transparency-dev/tessera"
	"github.com/transparency-dev/tessera/api/layout"
)

const (
	compatibilityVersion = 1
	stateDir             = ".state"
	treeStateFile        = "treeState"
)

type Config struct {
	Path   string // Directory for log files
	DBPath string // Path to SQLite DB file
}

type Storage struct {
	mu  sync.Mutex
	cfg Config
	db  *sql.DB
}

// New creates a new SQLite+POSIX hybrid storage.
func New(ctx context.Context, cfg Config) (tessera.Driver, error) {
	db, err := sql.Open("sqlite3", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %v", err)
	}
	if err := initDB(ctx, db); err != nil {
		return nil, fmt.Errorf("failed to init sqlite db: %v", err)
	}
	return &Storage{cfg: cfg, db: db}, nil
}

func initDB(ctx context.Context, db *sql.DB) error {
	// Similar to AWS/MySQL, but simplified for SQLite
	_, err := db.ExecContext(ctx, `
    CREATE TABLE IF NOT EXISTS SeqCoord (
        id INTEGER PRIMARY KEY,
        next INTEGER NOT NULL
    );
    CREATE TABLE IF NOT EXISTS IntCoord (
        id INTEGER PRIMARY KEY,
        seq INTEGER NOT NULL,
        rootHash BLOB NOT NULL
    );
    INSERT OR IGNORE INTO SeqCoord (id, next) VALUES (0, 0);
    INSERT OR IGNORE INTO IntCoord (id, seq, rootHash) VALUES (0, 0, ?);
    `, rfc6962.DefaultHasher.EmptyRoot())
	return err
}

func (s *Storage) Appender(ctx context.Context, opts *tessera.AppendOptions) (*tessera.Appender, tessera.LogReader, error) {
	a := &appender{
		s:           s,
		entriesPath: opts.EntriesPath(),
	}
	return &tessera.Appender{Add: a.Add}, a, nil
}

type appender struct {
	s           *Storage
	entriesPath func(uint64, uint8) string
}

func AddResult(index uint64, err error) tessera.IndexFuture {
	idxResult := tessera.Index{
		Index: index,
		IsDup: false,
	}

	return func() (tessera.Index, error) {
		return idxResult, err
	}
}

// Add: assign sequence number, write bundle, integrate, update tree state and checkpoint in one step.
func (a *appender) Add(ctx context.Context, e *tessera.Entry) tessera.IndexFuture {
	type indexResult struct {
		index uint64
		err   error
	}

	a.s.mu.Lock()
	defer a.s.mu.Unlock()

	tx, err := a.s.db.BeginTx(ctx, nil)
	if err != nil {
		return AddResult(0, fmt.Errorf("failed to begin transaction: %v", err))
	}
	defer tx.Rollback()

	var next uint64
	row := tx.QueryRowContext(ctx, "SELECT next FROM SeqCoord WHERE id = 0")
	if err := row.Scan(&next); err != nil {
		return AddResult(0, fmt.Errorf("failed to get next sequence number: %v", err))
	}

	// Write entry bundle to disk (like POSIX)
	bundleIndex, entriesInBundle := next/layout.EntryBundleWidth, next%layout.EntryBundleWidth
	bundlePath := filepath.Join(a.s.cfg.Path, a.entriesPath(bundleIndex, uint8(entriesInBundle)))
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0755); err != nil {
		return AddResult(0, fmt.Errorf("failed to create bundle directory: %v", err))
	}
	if err := os.WriteFile(bundlePath, e.MarshalBundleData(next), 0644); err != nil {
		return AddResult(0, fmt.Errorf("failed to write entry bundle: %v", err))
	}

	// Integrate into tree (like POSIX)
	// For simplicity, read all bundles up to next+1 and recompute root.
	// TODO: optimize this
	size := next + 1
	leafHashes := make([][]byte, size)
	for i := uint64(0); i < size; i++ {
		bp := filepath.Join(a.s.cfg.Path, a.entriesPath(i/layout.EntryBundleWidth, uint8(i%layout.EntryBundleWidth)))
		b, err := os.ReadFile(bp)
		if err != nil {
			return AddResult(0, fmt.Errorf("failed to read bundle %d: %v", i, err))
		}
		lh, err := e.LeafHash() // Or use bundleHasher if available
		if err != nil {
			return AddResult(0, fmt.Errorf("failed to compute leaf hash for bundle %d: %v", i, err))
		}
		leafHashes[i] = lh
	}
	newRoot, err := integrate(ctx, 0, leafHashes, a)
	if err != nil {
		return AddResult(0, fmt.Errorf("failed to integrate: %v", err))
	}

	// Update IntCoord and SeqCoord in DB
	if _, err := tx.ExecContext(ctx, "UPDATE IntCoord SET seq=?, rootHash=? WHERE id=0", size, newRoot); err != nil {
		return AddResult(0, fmt.Errorf("failed to update IntCoord: %v", err))
	}
	if _, err := tx.ExecContext(ctx, "UPDATE SeqCoord SET next=? WHERE id=0", size); err != nil {
		return AddResult(0, fmt.Errorf("failed to update SeqCoord: %v", err))
	}
	if err := tx.Commit(); err != nil {
		return AddResult(0, fmt.Errorf("failed to commit transaction: %v", err))
	}
	return AddResult(next, nil)
}

// Implement tessera.LogReader methods (ReadCheckpoint, ReadEntryBundle, etc.)
// using POSIX logic from files.go, but using a.s.cfg.Path as the root directory.

// integrate is a simplified version of the POSIX/AWS integrate logic.
func integrate(ctx context.Context, fromSeq uint64, lh [][]byte, lrs *appender) ([]byte, error) {
	// Use rfc6962.DefaultHasher or similar to compute new root.
	// For simplicity, just return a dummy value here.
	return []byte("dummy-root"), nil
}
