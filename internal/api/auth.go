// IdMS for MCX Admin: access token introspection, the administrator check, one-time password links and the
// revocation of a user's sessions.
//
// Specifications: TS 33.180 V19.4.0 (Rel-19), TS 24.484 V19.6.0 (Rel-19), RFC 7662.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openMCX/openmcx-admin/internal/closer"
	"github.com/openMCX/openmcx-admin/internal/config"
)

// Introspector checks an IdMS access token and returns the MCPTT ID it names (RFC 7662 token introspection;
// the mcptt_id claim of TS 33.180 table B.2.2.3-1), with the server client credentials IdMS knows.
type Introspector interface {
	Introspect(ctx context.Context, token string) (mcpttID string, active bool, err error)
}

// IdMS introspects tokens at an IdMS.
type IdMS struct {
	URL, ClientID, Secret string
	Client                *http.Client
}

// ------------------------------------------------------------------------------
// Introspect posts the token to IdMS's /introspect (RFC 7662 section 2.1) and reads "active" and the mcptt_id
// claim (TS 33.180 table B.2.2.3-1) from the answer (RFC 7662 section 2.2).
func (idms *IdMS) Introspect(ctx context.Context, token string) (string, bool, error) {
	body := url.Values{"token": {token}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(idms.URL, "/")+"/introspect", strings.NewReader(body))
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(idms.ClientID, idms.Secret)
	client := idms.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer closer.Quietly("response body", resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("IdMS introspection: status %d", resp.StatusCode)
	}
	var out struct {
		Active  bool   `json:"active"`
		MCPTTID string `json:"mcptt_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false, err
	}
	return out.MCPTTID, out.Active, nil
}

var errNotAdmin = errors.New("not an administrator")

// ------------------------------------------------------------------------------
// administrator admits the MCPTT ID of an active token only if that user is active and its effective profile
// grants config_management: the MCS service administrator of TS 24.484 clause 4.1. The config_management right
// itself is openMCX's own, not a TS 24.484 element. It returns the user's login, the actor recorded in revisions
// and audit.
func administrator(doc *config.Document, mcpttID string) (string, error) {
	for _, user := range doc.Users {
		if user.MCPTTID != mcpttID {
			continue
		}
		if user.Status == "active" {
			if granted, _ := doc.EffectiveProfile(user)["config_management"].(bool); granted {
				return user.Login, nil
			}
		}
		return "", errNotAdmin
	}
	return "", errNotAdmin
}

// LinkIssuer asks IdMS for a one-time set-password or reset link (slice B2). The
// link goes to the administrator once and is never stored or logged here.
type LinkIssuer interface {
	PasswordLink(ctx context.Context, login, purpose string) (url string, expires int64, err error)
}

// ------------------------------------------------------------------------------
// PasswordLink posts to IdMS's /internal/password-links with the server client credentials.
func (idms *IdMS) PasswordLink(ctx context.Context, login, purpose string) (string, int64, error) {
	body, _ := json.Marshal(map[string]string{"login": login, "purpose": purpose})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(idms.URL, "/")+"/internal/password-links", strings.NewReader(string(body)))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(idms.ClientID, idms.Secret)
	client := idms.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer closer.Quietly("response body", resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("IdMS password link: status %d", resp.StatusCode)
	}
	var out struct {
		URL     string `json:"url"`
		Expires int64  `json:"expires"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", 0, err
	}
	return out.URL, out.Expires, nil
}

// SessionRevoker ends a user's IdMS sessions: their refresh tokens (TS 33.180 annex B.5), so
// that no device of the user renews its access token.
type SessionRevoker interface {
	RevokeSessions(ctx context.Context, login string) (int, error)
}

// ------------------------------------------------------------------------------
// RevokeSessions posts to IdMS's /internal/refresh-tokens/{login}/revoke with the server
// client credentials.
func (idms *IdMS) RevokeSessions(ctx context.Context, login string) (int, error) {
	target := strings.TrimSuffix(idms.URL, "/") + "/internal/refresh-tokens/" + url.PathEscape(login) + "/revoke"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(idms.ClientID, idms.Secret)
	client := idms.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer closer.Quietly("response body", resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("IdMS session revocation: status %d", resp.StatusCode)
	}
	var out struct {
		Revoked int `json:"revoked"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.Revoked, nil
}
