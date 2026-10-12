//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Revoke devices: the Credential Service revokes a user's device certificates and SIP credential, and IdMS the
// user's refresh tokens.
//
// OpenMCX administration (no 3GPP specification).
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openMCX/openmcx-admin/internal/closer"
)

// CredentialService is the Credential Service as MCX Admin calls it, with the server
// client credentials (slice F2).
type CredentialService struct {
	URL, ClientID, Secret string
	Client                *http.Client
}

// ------------------------------------------------------------------------------
// revoke asks the Credential Service to revoke a SIP user's client certificates and remove its SIP credential, with
// the server client credentials; it returns the service's answer (at most 4 KiB) for the audit.
func (service *CredentialService) revoke(ctx context.Context, sipUser string) (string, error) {
	target := strings.TrimSuffix(service.URL, "/") + "/internal/client-certificates/" + url.PathEscape(sipUser) + "/revoke"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(service.ClientID, service.Secret)
	client := service.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer closer.Quietly("credential service answer", resp.Body)
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("credential service: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("credential service: status %d", resp.StatusCode)
	}
	return string(body), nil
}

// ------------------------------------------------------------------------------
// revokeDevices is Revoke devices, for a lost or stolen device: the Credential Service
// records the user's certificates revoked and removes the SIP credential, so no device
// of the user registers until one is activated again, and IdMS revokes the user's
// refresh tokens, so none renews its access token. The account stays as it is.
func (server *Server) revokeDevices(writer http.ResponseWriter, request *http.Request, actor string) {
	if server.Credentials == nil {
		fail(writer, http.StatusNotFound, "no Credential Service is configured", nil)
		return
	}
	_, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	login := request.PathValue("login")
	for _, user := range doc.Users {
		if user.Login != login {
			continue
		}
		sipUser, _, _ := strings.Cut(strings.TrimPrefix(user.SIPURI, "sip:"), "@")
		result, err := server.Credentials.revoke(request.Context(), sipUser)
		if err != nil {
			fail(writer, http.StatusBadGateway, err.Error(), nil)
			return
		}
		detail := map[string]any{"credentials": answerDetail(result)}
		if server.Sessions != nil {
			count, err := server.Sessions.RevokeSessions(request.Context(), login)
			if err != nil {
				detail["idms_error"] = err.Error()
				server.record(request.Context(), actor, "devices-revoked", login, detail)
				fail(writer, http.StatusBadGateway, "the devices' credentials are revoked, but IdMS: "+err.Error(), nil)
				return
			}
			detail["idms_sessions_revoked"] = count
		}
		server.record(request.Context(), actor, "devices-revoked", login, detail)
		writeJSON(writer, http.StatusOK, map[string]any{"login": login, "revoked": true})
		return
	}
	fail(writer, http.StatusNotFound, "no such user", nil)
}

// ------------------------------------------------------------------------------
// answerDetail is a service's answer as it goes into an audit detail: the JSON value itself when the answer is
// JSON, otherwise the text as a JSON string, so the detail stays valid JSON either way.
func answerDetail(answer string) any {
	trimmed := strings.TrimSpace(answer)
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed)
	}
	return trimmed
}
