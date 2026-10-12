//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Package project keeps the consumers of the store up to date (slice C): after each
// revision, a consumer receives the configuration it needs, and its outbox rows are
// marked applied. This file: the users file of the Diameter user database.
//
// OpenMCX administration (no 3GPP specification); the user database's refusal of an unknown user is TS 29.283's
// (cited, its text not kept locally).
package project

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/openMCX/openmcx-admin/internal/closer"
	"github.com/openMCX/openmcx-admin/internal/store"
)

// UserDB writes the users file of the Diameter user database (mcx-user-db --watch):
// the active accounts as the servers read them from site.json, never a secret. A
// suspended, pending or retired account is not in it, so the user database answers
// that user unknown (TS 29.283, the standard refusal).
type UserDB struct {
	Store  *store.Store
	Domain string
	Path   string
}

const userDBConsumer = "userdb"

// ------------------------------------------------------------------------------
// Once applies the latest pending revision, if any.
func (userDB *UserDB) Once(ctx context.Context) error {
	pending, err := userDB.Store.Pending(ctx, userDBConsumer)
	if err != nil || pending == 0 {
		return err
	}
	rev, doc, err := userDB.Store.Current(ctx, userDB.Domain)
	if err != nil {
		return err
	}
	users := []map[string]any{}
	for _, user := range doc.Users {
		if user.Status == "active" {
			users = append(users, doc.SiteUser(user))
		}
	}
	raw, err := json.MarshalIndent(map[string]any{"revision": rev, "users": users}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(userDB.Path, raw); err != nil {
		recordFailure(ctx, userDB.Store, userDBConsumer, rev, err)
		return err
	}
	return userDB.Store.Applied(ctx, userDBConsumer, rev)
}

// ------------------------------------------------------------------------------
// Run applies pending revisions every interval until the context ends.
func (userDB *UserDB) Run(ctx context.Context, interval time.Duration) {
	for {
		if err := userDB.Once(ctx); err != nil {
			log.Printf("userdb projection: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// ------------------------------------------------------------------------------
// writeAtomic writes a whole new file and renames it in place, so a reader sees the
// old file or the new one, never a part.
func writeAtomic(path string, raw []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".userdb-*.json")
	if err != nil {
		return err
	}
	// After the rename the temporary name is gone; only a failure before it leaves the file to remove.
	defer func() {
		if err := os.Remove(tmp.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("userdb: remove %s: %v", tmp.Name(), err)
		}
	}()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		closer.Quietly("userdb temporary file", tmp)
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		closer.Quietly("userdb temporary file", tmp)
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
