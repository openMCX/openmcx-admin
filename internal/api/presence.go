//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Per-user presence for the dashboard: registrations at the SIP core and the last sign-in at IdMS.
//
// OpenMCX administration (no 3GPP specification).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/openMCX/openmcx-admin/internal/closer"
)

// Per-user presence for the dashboard: whether a user's devices are
// registered at the SIP core now (its registrations, read with ul.dump over the SIP
// core's operator RPC), and when the user last signed in at IdMS (IdMS asks MCX Admin
// for the account by login at every sign-in). Read-only: it changes nothing in the
// servers' behaviour.

// SIPCoreRPC is the SIP core's operator RPC (-A CORE_RPC): JSON-RPC over HTTPS, with
// this service's certificate as its credential.
type SIPCoreRPC struct {
	URL    string
	Client *http.Client
}

type registration struct {
	Contacts  int    `json:"contacts"`
	ExpiresIn int    `json:"expires_in"`
	UserAgent string `json:"user_agent,omitempty"`
}

// ------------------------------------------------------------------------------
// Registrations is the registered AoRs (user@domain) with their contacts.
func (rpc *SIPCoreRPC) Registrations(ctx context.Context) (map[string]registration, error) {
	body := []byte(`{"jsonrpc":"2.0","method":"ul.dump","id":1}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpc.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := rpc.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer closer.Quietly("response body", resp.Body)
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SIP core RPC: status %d", resp.StatusCode)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	out := map[string]registration{}
	collectAoRs(doc, out)
	return out, nil
}

// ------------------------------------------------------------------------------
// collectAoRs walks ul.dump's answer for {"AoR": ..., "Contacts": [{"Contact": {...}}]},
// whatever the nesting of its domains.
func collectAoRs(value any, out map[string]registration) {
	switch typed := value.(type) {
	case map[string]any:
		if aor, ok := typed["AoR"].(string); ok {
			entry := registration{}
			contacts, _ := typed["Contacts"].([]any)
			for _, item := range contacts {
				contact, _ := item.(map[string]any)
				if inner, ok := contact["Contact"].(map[string]any); ok {
					contact = inner
				}
				entry.Contacts++
				if expires, ok := contact["Expires"].(float64); ok && int(expires) > entry.ExpiresIn {
					entry.ExpiresIn = int(expires)
				}
				if ua, ok := contact["User-Agent"].(string); ok && ua != "" && ua != "n/a" {
					entry.UserAgent = ua
				}
			}
			if entry.Contacts > 0 {
				out[strings.ToLower(aor)] = entry
			}
			return
		}
		for _, child := range typed {
			collectAoRs(child, out)
		}
	case []any:
		for _, child := range typed {
			collectAoRs(child, out)
		}
	}
}

type signIns struct {
	mu sync.Mutex
	at map[string]time.Time
}

// ------------------------------------------------------------------------------
// note records that a user signed in now (UTC).
func (tracker *signIns) note(login string) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.at == nil {
		tracker.at = map[string]time.Time{}
	}
	tracker.at[login] = time.Now().UTC()
}

// ------------------------------------------------------------------------------
// last is the time a user last signed in, if MCX Admin has seen a sign-in since it started (it keeps them in memory
// only).
func (tracker *signIns) last(login string) (time.Time, bool) {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	at, ok := tracker.at[login]
	return at, ok
}

// ------------------------------------------------------------------------------
// presence answers each user's registration and last sign-in; registrations are
// absent (not false) when the SIP core cannot be asked.
func (server *Server) presence(writer http.ResponseWriter, request *http.Request, _ string) {
	_, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	var regs map[string]registration
	var regErr string
	if server.SIPCore != nil {
		if regs, err = server.SIPCore.Registrations(request.Context()); err != nil {
			regErr = err.Error()
		}
	}
	users := map[string]any{}
	registered := 0
	for _, user := range doc.Users {
		entry := map[string]any{}
		if at, ok := server.signIns.last(user.Login); ok {
			entry["last_sign_in"] = at
		}
		if regs != nil {
			aor := strings.ToLower(strings.TrimPrefix(user.SIPURI, "sip:"))
			reg, ok := regs[aor]
			entry["registered"] = ok
			if ok {
				registered++
				entry["contacts"], entry["expires_in"], entry["user_agent"] = reg.Contacts, reg.ExpiresIn, reg.UserAgent
			}
		}
		users[user.Login] = entry
	}
	answer := map[string]any{"users": users, "sip_core": server.SIPCore != nil, "registered": registered}
	if regErr != "" {
		answer["sip_core_error"] = regErr
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, answer)
}
