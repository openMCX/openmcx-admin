// The Credential Service projection: account status changes turned into suspending, resuming or removing the
// account's SIP credential.
//
// openMCX administration (no 3GPP specification).
package project

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/openMCX/openmcx-admin/internal/closer"
	"github.com/openMCX/openmcx-admin/internal/config"
	"github.com/openMCX/openmcx-admin/internal/store"
)

// SIP keeps the Credential Service in step with account status (slice F1): after a
// revision, an account that became suspended has its SIP credential suspended, one
// that became active again resumed, and a retired or removed one removed. It never handles a
// credential: the Credential Service keeps them.
type SIP struct {
	Store    *store.Store
	Domain   string
	URL      string // the Credential Service
	ClientID string // the server client credentials it accepts
	Secret   string
	Client   *http.Client
}

const sipConsumer = "sip"

// ------------------------------------------------------------------------------
// action is the Credential Service operation for an account status: resume, suspend or retire; none for pending.
func action(status string) string {
	switch status {
	case "active":
		return "resume"
	case "suspended":
		return "suspend"
	case "retired":
		return "retire"
	}
	return "" // pending: no credential yet
}

// ------------------------------------------------------------------------------
// Once applies the latest pending revision, if any: the status changes since the
// last applied revision, or every account's status the first time.
func (sip *SIP) Once(ctx context.Context) error {
	pending, err := sip.Store.Pending(ctx, sipConsumer)
	if err != nil || pending == 0 {
		return err
	}
	last, err := sip.Store.LastApplied(ctx, sipConsumer)
	if err != nil {
		return err
	}
	rev, doc, err := sip.Store.Current(ctx, sip.Domain)
	if err != nil {
		return err
	}
	before := map[string]config.User{}
	if last > 0 {
		old, err := sip.Store.Document(ctx, last)
		if err != nil {
			return err
		}
		for _, user := range old.Users {
			before[user.Login] = user
		}
	}
	type step struct {
		user      config.User
		operation string
	}
	var steps []step
	for _, user := range doc.Users {
		if prev, known := before[user.Login]; last > 0 && known && prev.Status == user.Status {
			continue
		}
		if operation := action(user.Status); operation != "" {
			steps = append(steps, step{user, operation})
		}
	}
	// A user no longer in the document (a rollback past its creation) loses its credential as a retired one does.
	present := map[string]bool{}
	for _, user := range doc.Users {
		present[user.Login] = true
	}
	for _, login := range sortedLogins(before) {
		if user := before[login]; !present[login] && user.Status != "retired" && user.Status != "pending" {
			steps = append(steps, step{user, action("retired")})
		}
	}
	for _, next := range steps {
		if err := sip.change(ctx, next.user, next.operation); err != nil {
			recordFailure(ctx, sip.Store, sipConsumer, rev, err)
			return err
		}
	}
	return sip.Store.Applied(ctx, sipConsumer, rev)
}

// ------------------------------------------------------------------------------
// change asks the Credential Service to apply an operation to the SIP credential of a user (the user part of its
// SIP URI), with the server client credentials.
func (sip *SIP) change(ctx context.Context, user config.User, action string) error {
	login := strings.TrimPrefix(user.SIPURI, "sip:")
	login, _, _ = strings.Cut(login, "@")
	target := strings.TrimSuffix(sip.URL, "/") + "/internal/sip-credentials/" + url.PathEscape(login) + "/" + action
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(sip.ClientID, sip.Secret)
	client := sip.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	closer.Quietly("credential service answer", resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("credential service: %s %s: status %d", action, login, resp.StatusCode)
	}
	return nil
}

// ------------------------------------------------------------------------------
// Run applies pending revisions every interval until the context ends.
func (sip *SIP) Run(ctx context.Context, interval time.Duration) {
	for {
		if err := sip.Once(ctx); err != nil {
			log.Printf("sip projection: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// ------------------------------------------------------------------------------
// sortedLogins is the logins of a user map in order, so the projection's calls are repeatable.
func sortedLogins(users map[string]config.User) []string {
	logins := make([]string, 0, len(users))
	for login := range users {
		logins = append(logins, login)
	}
	sort.Strings(logins)
	return logins
}

// ------------------------------------------------------------------------------
// recordFailure marks a consumer's revision failed, for the dashboard; if even that write fails it is logged (the
// revision stays pending and is retried either way).
func recordFailure(ctx context.Context, storage *store.Store, consumer string, rev int64, cause error) {
	if err := storage.Failed(ctx, consumer, rev, cause.Error()); err != nil {
		log.Printf("%s projection: recording the failure of revision %d: %v", consumer, rev, err)
	}
}
