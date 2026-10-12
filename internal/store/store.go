//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Package store keeps the configuration in MySQL (docs/SLICE_B.md): each applied
// change is a revision holding the whole document and its change list, written
// with its audit and outbox rows in one transaction.
//
// OpenMCX administration (no 3GPP specification).
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/openMCX/openmcx-admin/internal/closer"
	"github.com/openMCX/openmcx-admin/internal/config"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DefaultConsumers are the services a revision is projected to; each gets an outbox
// row per revision. "userdb" is the Diameter user database (slice C); the
// Credential Service adds "sip" (slice F1).
var DefaultConsumers = []string{"userdb"}

// ErrStale is a write based on a revision that is no longer the latest.
var ErrStale = errors.New("the configuration changed since that revision")

// ErrInvalid is a write whose document does not validate.
type ErrInvalid struct{ Problems []config.Problem }

// ------------------------------------------------------------------------------
// Error counts the validation problems.
func (invalid *ErrInvalid) Error() string {
	return fmt.Sprintf("%d validation problems", len(invalid.Problems))
}

// Revision is one applied change.
type Revision struct {
	ID        int64           `json:"revision"`
	CreatedAt time.Time       `json:"created_at"`
	Actor     string          `json:"actor"`
	Reason    string          `json:"reason"`
	Source    string          `json:"source"`
	Changes   []config.Change `json:"changes"`
}

// AuditEntry is one line of the audit trail.
type AuditEntry struct {
	ID         int64     `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	Target     string    `json:"target"`
	RevisionID *int64    `json:"revision,omitempty"`
	Detail     string    `json:"detail"`
}

// Store is the MySQL store.
type Store struct {
	db        *sql.DB
	now       func() time.Time
	Consumers []string
}

// ------------------------------------------------------------------------------
// Open connects and applies the migrations not applied yet.
func Open(ctx context.Context, db *sql.DB) (*Store, error) {
	storage := &Store{db: db, now: func() time.Time { return time.Now().UTC() }, Consumers: DefaultConsumers}
	if err := storage.migrate(ctx); err != nil {
		return nil, err
	}
	return storage, nil
}

// ------------------------------------------------------------------------------
// migrate applies, in name order, the embedded migrations not recorded in schema_migrations yet, statement by
// statement, and records each.
func (storage *Store) migrate(ctx context.Context) error {
	if _, err := storage.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name VARCHAR(255) PRIMARY KEY, applied_at DATETIME(3) NOT NULL)`); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		var done int
		if err := storage.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			continue
		}
		raw, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		// MySQL runs DDL one statement at a time and commits each; a migration is
		// recorded only after all its statements ran.
		for _, statement := range splitStatements(string(raw)) {
			if _, err := storage.db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
		}
		if _, err := storage.db.ExecContext(ctx, `INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, storage.now()); err != nil {
			return err
		}
	}
	return nil
}

// ------------------------------------------------------------------------------
// splitStatements cuts a migration script into its statements at each ";" outside quotes ('...', "...", `...`)
// and outside "--" line comments, and drops the comments; empty statements are left out.
func splitStatements(script string) []string {
	var statements []string
	var current strings.Builder
	flush := func() {
		if statement := strings.TrimSpace(current.String()); statement != "" {
			statements = append(statements, statement)
		}
		current.Reset()
	}
	var quote byte
	for index := 0; index < len(script); index++ {
		char := script[index]
		switch {
		case quote != 0:
			current.WriteByte(char)
			if char == '\\' && quote != '`' && index+1 < len(script) {
				index++
				current.WriteByte(script[index])
			} else if char == quote {
				quote = 0
			}
		case char == '\'' || char == '"' || char == '`':
			quote = char
			current.WriteByte(char)
		case char == '-' && strings.HasPrefix(script[index:], "--"):
			for index < len(script) && script[index] != '\n' {
				index++
			}
			current.WriteByte('\n')
		case char == ';':
			flush()
		default:
			current.WriteByte(char)
		}
	}
	flush()
	return statements
}

// ------------------------------------------------------------------------------
// Current is the latest revision's number and document; revision 0 with an empty
// document of the given domain when nothing is stored yet.
func (storage *Store) Current(ctx context.Context, domain string) (int64, *config.Document, error) {
	return current(ctx, storage.db, domain)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// ------------------------------------------------------------------------------
// current is the latest revision's number and document, read through a database or a transaction; revision 0 with
// an empty document of the domain when there is none.
func current(ctx context.Context, querier queryer, domain string) (int64, *config.Document, error) {
	var id int64
	var raw string
	err := querier.QueryRowContext(ctx, `SELECT id, document FROM revisions ORDER BY id DESC LIMIT 1`).Scan(&id, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, &config.Document{SchemaVersion: config.SchemaVersion, System: config.System{Domain: domain},
			Users: []config.User{}, Groups: []map[string]any{}}, nil
	}
	if err != nil {
		return 0, nil, err
	}
	doc, err := config.Parse([]byte(raw))
	return id, doc, err
}

// ------------------------------------------------------------------------------
// Commit applies a new document as the next revision, if expected is still the
// latest revision and the document validates. The audit and outbox rows are
// written in the same transaction. It returns the new revision; with no change,
// it commits nothing and returns expected.
func (storage *Store) Commit(ctx context.Context, expected int64, doc *config.Document, actor, reason, source string) (int64, []config.Change, error) {
	if problems := doc.Validate(); len(problems) > 0 {
		return 0, nil, &ErrInvalid{Problems: problems}
	}
	// Concurrent writers can deadlock in InnoDB; the victim is rolled back and tries
	// again, and then finds the other's revision (ErrStale).
	var id int64
	var changes []config.Change
	err := retryTransient(ctx, func() error {
		var err error
		id, changes, err = storage.commit(ctx, expected, doc, actor, reason, source)
		return err
	})
	return id, changes, err
}

// ------------------------------------------------------------------------------
// commit is one attempt of Commit: in a serializable transaction holding the write lock, it checks that expected is
// the latest revision, diffs the document against it, and writes the revision, one outbox row per consumer and the
// audit row.
func (storage *Store) commit(ctx context.Context, expected int64, doc *config.Document, actor, reason, source string) (int64, []config.Change, error) {
	tx, err := storage.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return 0, nil, err
	}
	defer rollback(tx)
	// The write lock serialises writers; the latest revision is read after it.
	if _, err := tx.ExecContext(ctx, `SELECT id FROM write_lock WHERE id = 1 FOR UPDATE`); err != nil {
		return 0, nil, err
	}
	var latest int64
	var raw sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id, document FROM revisions ORDER BY id DESC LIMIT 1 FOR UPDATE`).Scan(&latest, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, nil, err
	}
	if latest != expected {
		return 0, nil, ErrStale
	}
	before := &config.Document{SchemaVersion: config.SchemaVersion, System: doc.System, Users: []config.User{}, Groups: []map[string]any{}}
	if raw.Valid {
		if before, err = config.Parse([]byte(raw.String)); err != nil {
			return 0, nil, err
		}
	}
	changes := config.Diff(before, doc)
	if len(changes) == 0 {
		return expected, nil, nil
	}
	document, err := json.Marshal(doc)
	if err != nil {
		return 0, nil, err
	}
	list, _ := json.Marshal(changes)
	now := storage.now()
	res, err := tx.ExecContext(ctx, `INSERT INTO revisions (created_at, actor, reason, source, document, changes) VALUES (?, ?, ?, ?, ?, ?)`,
		now, actor, reason, source, document, list)
	if err != nil {
		return 0, nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, nil, err
	}
	for _, consumer := range storage.Consumers {
		if _, err := tx.ExecContext(ctx, `INSERT INTO outbox (revision_id, consumer, status, updated_at) VALUES (?, ?, 'pending', ?)`, id, consumer, now); err != nil {
			return 0, nil, err
		}
	}
	detail, _ := json.Marshal(map[string]any{"reason": reason, "source": source, "changes": len(changes)})
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit (created_at, actor, action, target, revision_id, detail) VALUES (?, ?, 'commit', ?, ?, ?)`,
		now, actor, source, id, string(detail)); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return id, changes, nil
}

// ------------------------------------------------------------------------------
// Audit records an action that is not a revision (a refused sign-in, a validation).
func (storage *Store) Audit(ctx context.Context, actor, action, target, detail string) error {
	return storage.exec(ctx, `INSERT INTO audit (created_at, actor, action, target, detail) VALUES (?, ?, ?, ?, ?)`,
		storage.now(), actor, action, target, detail)
}

// ------------------------------------------------------------------------------
// exec runs one write statement, again when it failed only because of concurrent writers (retryTransient).
func (storage *Store) exec(ctx context.Context, statement string, arguments ...any) error {
	return retryTransient(ctx, func() error {
		_, err := storage.db.ExecContext(ctx, statement, arguments...)
		return err
	})
}

// ------------------------------------------------------------------------------
// Revisions lists the revisions after a number, oldest first, at most limit.
func (storage *Store) Revisions(ctx context.Context, after int64, limit int) ([]Revision, error) {
	rows, err := storage.db.QueryContext(ctx, `SELECT id, created_at, actor, reason, source, changes FROM revisions WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer closer.Quietly("rows", rows)
	out := []Revision{}
	for rows.Next() {
		var revision Revision
		var changes string
		if err := rows.Scan(&revision.ID, &revision.CreatedAt, &revision.Actor, &revision.Reason, &revision.Source, &changes); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(changes), &revision.Changes); err != nil {
			return nil, err
		}
		out = append(out, revision)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------------------
// Document is the document of one revision.
func (storage *Store) Document(ctx context.Context, id int64) (*config.Document, error) {
	var raw string
	if err := storage.db.QueryRowContext(ctx, `SELECT document FROM revisions WHERE id = ?`, id).Scan(&raw); err != nil {
		return nil, err
	}
	return config.Parse([]byte(raw))
}

// ------------------------------------------------------------------------------
// AuditTrail lists audit entries after an ID, oldest first.
func (storage *Store) AuditTrail(ctx context.Context, after int64, limit int) ([]AuditEntry, error) {
	rows, err := storage.db.QueryContext(ctx, `SELECT id, created_at, actor, action, target, revision_id, detail FROM audit WHERE id > ? ORDER BY id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer closer.Quietly("rows", rows)
	out := []AuditEntry{}
	for rows.Next() {
		var entry AuditEntry
		var rev sql.NullInt64
		if err := rows.Scan(&entry.ID, &entry.CreatedAt, &entry.Actor, &entry.Action, &entry.Target, &rev, &entry.Detail); err != nil {
			return nil, err
		}
		if rev.Valid {
			entry.RevisionID = &rev.Int64
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------------------
// Outbox counts outbox rows by consumer and status (the projection health).
func (storage *Store) Outbox(ctx context.Context) (map[string]map[string]int, error) {
	rows, err := storage.db.QueryContext(ctx, `SELECT consumer, status, COUNT(*) FROM outbox GROUP BY consumer, status`)
	if err != nil {
		return nil, err
	}
	defer closer.Quietly("rows", rows)
	out := map[string]map[string]int{}
	for rows.Next() {
		var consumer, status string
		var count int
		if err := rows.Scan(&consumer, &status, &count); err != nil {
			return nil, err
		}
		if out[consumer] == nil {
			out[consumer] = map[string]int{}
		}
		out[consumer][status] = count
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------------------
// Ping checks the database.
func (storage *Store) Ping(ctx context.Context) error { return storage.db.PingContext(ctx) }

// ------------------------------------------------------------------------------
// Pending is the latest revision a consumer has not applied yet, or 0.
func (storage *Store) Pending(ctx context.Context, consumer string) (int64, error) {
	var id sql.NullInt64
	err := storage.db.QueryRowContext(ctx, `SELECT MAX(revision_id) FROM outbox WHERE consumer = ? AND status <> 'applied'`, consumer).Scan(&id)
	return id.Int64, err
}

// ------------------------------------------------------------------------------
// Applied marks a consumer's rows up to a revision applied: the consumer holds that
// revision, which includes all earlier ones.
func (storage *Store) Applied(ctx context.Context, consumer string, upTo int64) error {
	return storage.exec(ctx, `UPDATE outbox SET status = 'applied', attempts = attempts + 1, last_error = NULL, updated_at = ?
		WHERE consumer = ? AND revision_id <= ? AND status <> 'applied'`, storage.now(), consumer, upTo)
}

// ------------------------------------------------------------------------------
// Failed records a failed attempt on a consumer's rows up to a revision; they stay
// to be retried.
func (storage *Store) Failed(ctx context.Context, consumer string, upTo int64, reason string) error {
	return storage.exec(ctx, `UPDATE outbox SET status = 'failed', attempts = attempts + 1, last_error = ?, updated_at = ?
		WHERE consumer = ? AND revision_id <= ? AND status <> 'applied'`, reason, storage.now(), consumer, upTo)
}

// ------------------------------------------------------------------------------
// LastApplied is the latest revision a consumer has applied, or 0.
func (storage *Store) LastApplied(ctx context.Context, consumer string) (int64, error) {
	var id sql.NullInt64
	err := storage.db.QueryRowContext(ctx, `SELECT MAX(revision_id) FROM outbox WHERE consumer = ? AND status = 'applied'`, consumer).Scan(&id)
	return id.Int64, err
}

// ------------------------------------------------------------------------------
// NewActivation records a one-time activation code of a login, by its hash, and
// ends the login's earlier unused codes.
func (storage *Store) NewActivation(ctx context.Context, codeHash, login, actor string, expires time.Time) error {
	return retryTransient(ctx, func() error { return storage.newActivation(ctx, codeHash, login, actor, expires) })
}

// ------------------------------------------------------------------------------
// newActivation is one attempt of NewActivation, in one transaction. The login's activations are serialised by a
// named lock on the transaction's connection, so two concurrent requests for one user leave one usable code; the
// transaction itself is READ COMMITTED, without the gap locks of REPEATABLE READ on the login index that made
// concurrent activations of different users deadlock.
func (storage *Store) newActivation(ctx context.Context, codeHash, login, actor string, expires time.Time) error {
	conn, err := storage.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer closer.Quietly("store connection", conn)
	name := "mcxadmin-activation:" + login
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 10)`, name).Scan(&locked); err != nil {
		return err
	}
	if locked.Int64 != 1 {
		return fmt.Errorf("activation of %s: lock not obtained", login)
	}
	defer func() {
		if _, err := conn.ExecContext(context.Background(), `SELECT RELEASE_LOCK(?)`, name); err != nil {
			log.Printf("store: release %s: %v", name, err)
		}
	}()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := storage.now()
	if _, err := tx.ExecContext(ctx, `UPDATE activations SET used_at = ? WHERE login = ? AND used_at IS NULL`, now, login); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO activations (code_hash, login, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		codeHash, login, actor, now, expires.UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// ------------------------------------------------------------------------------
// UseActivation marks a code used and returns its login, if the code exists, is
// unused and has not expired, and accept agrees; a code works once. accept runs
// while the code is locked and before it is marked used, so a refused request
// (an account not active, a package that cannot be built) leaves the code valid.
// An error from accept is returned as it is and the code stays unused.
func (storage *Store) UseActivation(ctx context.Context, codeHash string, accept func(login string) error) (string, bool, error) {
	var login string
	var used bool
	err := retryTransient(ctx, func() error {
		var err error
		login, used, err = storage.useActivation(ctx, codeHash, accept)
		return err
	})
	return login, used, err
}

// ------------------------------------------------------------------------------
// useActivation is one attempt of UseActivation, in one transaction.
func (storage *Store) useActivation(ctx context.Context, codeHash string, accept func(login string) error) (string, bool, error) {
	// READ COMMITTED, as newActivation: the code's row is locked by its primary key (FOR UPDATE).
	tx, err := storage.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return "", false, err
	}
	defer rollback(tx)
	var login string
	var expires time.Time
	var used sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT login, expires_at, used_at FROM activations WHERE code_hash = ? FOR UPDATE`, codeHash).Scan(&login, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if used.Valid || !expires.After(storage.now()) {
		return "", false, nil
	}
	if err := accept(login); err != nil {
		return login, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE activations SET used_at = ? WHERE code_hash = ?`, storage.now(), codeHash); err != nil {
		return "", false, err
	}
	return login, true, tx.Commit()
}

// ------------------------------------------------------------------------------
// RecentAudit is the latest audit entries, at most limit, newest first.
func (storage *Store) RecentAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := storage.db.QueryContext(ctx, `SELECT id, created_at, actor, action, target, revision_id, detail FROM audit ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer closer.Quietly("rows", rows)
	out := []AuditEntry{}
	for rows.Next() {
		var entry AuditEntry
		var rev sql.NullInt64
		if err := rows.Scan(&entry.ID, &entry.CreatedAt, &entry.Actor, &entry.Action, &entry.Target, &rev, &entry.Detail); err != nil {
			return nil, err
		}
		if rev.Valid {
			entry.RevisionID = &rev.Int64
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------------------
// PendingActivations counts the activation codes not used and not expired.
func (storage *Store) PendingActivations(ctx context.Context) (int, error) {
	var count int
	err := storage.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activations WHERE used_at IS NULL AND expires_at > ?`, storage.now()).Scan(&count)
	return count, err
}

// ------------------------------------------------------------------------------
// GMSVersions is the GMS version of each group after MCX Admin's last write of it.
func (storage *Store) GMSVersions(ctx context.Context) (map[string]int64, error) {
	rows, err := storage.db.QueryContext(ctx, `SELECT group_id, version FROM gms_versions`)
	if err != nil {
		return nil, err
	}
	defer closer.Quietly("rows", rows)
	out := map[string]int64{}
	for rows.Next() {
		var id string
		var version int64
		if err := rows.Scan(&id, &version); err != nil {
			return nil, err
		}
		out[id] = version
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------------------
// SetGMSVersion records a group's GMS version.
func (storage *Store) SetGMSVersion(ctx context.Context, id string, version int64) error {
	return storage.exec(ctx, `INSERT INTO gms_versions (group_id, version) VALUES (?, ?) ON DUPLICATE KEY UPDATE version = VALUES(version)`, id, version)
}

// ------------------------------------------------------------------------------
// EnsureConsumers gives a consumer that has never had a revision to apply (one added
// to a store with revisions already, such as gms after an upgrade) the current
// revision, so that its first run brings it up to date instead of waiting for the
// next change.
func (storage *Store) EnsureConsumers(ctx context.Context) error {
	var latest sql.NullInt64
	if err := storage.db.QueryRowContext(ctx, `SELECT MAX(id) FROM revisions`).Scan(&latest); err != nil || !latest.Valid {
		return err
	}
	for _, consumer := range storage.Consumers {
		var count int
		if err := storage.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM outbox WHERE consumer = ?`, consumer).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err := storage.db.ExecContext(ctx, `INSERT INTO outbox (revision_id, consumer, status, updated_at) VALUES (?, ?, 'pending', ?)`,
				latest.Int64, consumer, storage.now()); err != nil {
				return err
			}
		}
	}
	return nil
}

// ------------------------------------------------------------------------------
// transientAttempts is how often a write is tried when MySQL resolves a lock conflict by failing it.
const transientAttempts = 3

// ------------------------------------------------------------------------------
// transient tells whether MySQL failed a statement only because of concurrent writers: a deadlock (error 1213, the
// victim's transaction rolled back) or a lock wait timeout (1205). Trying the whole transaction again is safe.
func transient(err error) bool {
	var my *mysql.MySQLError
	return errors.As(err, &my) && (my.Number == 1213 || my.Number == 1205)
}

// ------------------------------------------------------------------------------
// retryTransient runs one write transaction (attempt) and runs it again, after a short jittered pause, when it failed
// only because of concurrent writers (see transient); other errors and the last attempt's are returned as they are.
// Concurrent activations and password links hit InnoDB deadlocks under load.
func retryTransient(ctx context.Context, attempt func() error) error {
	for count := 1; ; count++ {
		err := attempt()
		if count >= transientAttempts || !transient(err) {
			return err
		}
		pause := time.Duration(count*10+rand.IntN(20)) * time.Millisecond
		select {
		case <-ctx.Done():
			return err
		case <-time.After(pause):
		}
	}
}

// ------------------------------------------------------------------------------
// rollback ends a transaction that was not committed; after a commit it does nothing. A failed rollback is
// logged: MySQL then drops the transaction with the connection.
func rollback(tx *sql.Tx) {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		log.Printf("store: rollback: %v", err)
	}
}
