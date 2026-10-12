//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Package api is the /v1 HTTP API of MCX Admin (docs/SLICE_B.md).
//
// OpenMCX administration (no 3GPP specification).
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"

	"github.com/openMCX/openmcx-admin/internal/activation"
	"github.com/openMCX/openmcx-admin/internal/config"
	"github.com/openMCX/openmcx-admin/internal/sheet"
	"github.com/openMCX/openmcx-admin/internal/store"
	"github.com/openMCX/openmcx-admin/internal/web"
)

// BatchSize is how many user rows an upload applies per revision.
var BatchSize = 500

// Server serves the API.
type Server struct {
	Store  *store.Store
	IdMS   Introspector
	Links  LinkIssuer
	Domain string
	// The server client credentials IdMS authenticates with when it asks for an
	// account (the same IdMS and MCX Admin use for introspection).
	ServerClientID, ServerSecret string
	// Ready, when set, holds /healthz until the projections the services start
	// from are written.
	Ready func(context.Context) bool
	// Activation, when set, issues one-time activation URLs (slice F1).
	Activation *activation.Config
	// Credentials, when set, is the Credential Service: Revoke devices (slice F2).
	Credentials *CredentialService
	// Sessions, when set, is IdMS ending a user's sessions (refresh tokens) at Revoke devices.
	Sessions SessionRevoker
	// SIPCore, when set, reads the SIP core's registrations for the dashboard.
	SIPCore *SIPCoreRPC
	signIns signIns
	// Console, when set, serves the web console (slice E1).
	Console *web.Console
	// Probes are the services the dashboard checks, with ProbeClient (the lab CA).
	Probes      []Probe
	ProbeClient *http.Client
}

// ------------------------------------------------------------------------------
// Handler routes the API.
func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", server.healthz)
	admin := func(pattern string, handler func(http.ResponseWriter, *http.Request, string)) {
		mux.HandleFunc(pattern, server.signedIn(handler))
	}
	admin("GET /v1/config", server.getConfig)
	admin("GET /v1/users", server.listUsers)
	admin("GET /v1/users/{login}", server.getUser)
	admin("PUT /v1/users/{login}", server.putUser)
	admin("POST /v1/users/{login}/suspend", server.setStatus("suspended"))
	admin("POST /v1/users/{login}/restore", server.setStatus("active"))
	admin("POST /v1/uploads", server.upload)
	admin("GET /v1/upload-template.xlsx", server.template)
	admin("GET /v1/revisions", server.revisions)
	admin("GET /v1/revisions/{n}/export", server.export)
	admin("POST /v1/revisions/{n}/rollback", server.rollback)
	admin("GET /v1/audit", server.audit)
	admin("GET /v1/health/projections", server.projections)
	admin("POST /v1/users/{login}/password-link", server.passwordLink)
	mux.HandleFunc("GET /internal/v1/accounts", server.account)
	mux.HandleFunc("GET /internal/v1/site-users", server.siteUser)
	admin("POST /v1/users/{login}/activation", server.newActivation)
	admin("POST /v1/users/{login}/revoke-devices", server.revokeDevices)
	mux.HandleFunc("GET /activate/{code}", server.activate)
	admin("GET /v1/me", server.me)
	admin("GET /v1/groups", server.listGroups)
	admin("PUT /v1/groups/{id}", server.putGroup)
	admin("DELETE /v1/groups/{id}", server.deleteGroup)
	admin("GET /v1/templates", server.listTemplates)
	admin("PUT /v1/templates/{name}", server.putTemplate)
	admin("DELETE /v1/templates/{name}", server.deleteTemplate)
	admin("GET /v1/status", server.status)
	admin("GET /v1/presence", server.presence)
	if server.Console != nil {
		server.Console.Routes(mux)
	}
	return mux
}

// ------------------------------------------------------------------------------
// writeJSON answers with the status and the value as JSON.
func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

// ------------------------------------------------------------------------------
// fail answers with the status and a JSON error body: the reason, and the detail when there is one.
func fail(writer http.ResponseWriter, status int, reason string, detail any) {
	body := map[string]any{"error": reason}
	if detail != nil {
		body["detail"] = detail
	}
	writeJSON(writer, status, body)
}

// ------------------------------------------------------------------------------
// signedIn admits a request whose bearer token (an access token of TS 33.180 annex B.2.2, or the console's
// session cookie holding one) IdMS finds active and whose user is an administrator (auth.go). The token is used
// for this check only, never stored or logged.
func (server *Server) signedIn(handler func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		token, ok := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
		if !ok && server.Console != nil {
			// The console's session: a write needs the console's header too.
			if cookie, err := request.Cookie(web.SessionCookie); err == nil && cookie.Value != "" {
				if request.Method != http.MethodGet && request.Method != http.MethodHead && request.Header.Get(web.CSRFHeader) != "1" {
					fail(writer, http.StatusForbidden, "a console write needs the "+web.CSRFHeader+" header", nil)
					return
				}
				token, ok = cookie.Value, true
			}
		}
		if !ok || token == "" {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="mcxadmin"`)
			fail(writer, http.StatusUnauthorized, "sign-in required", nil)
			return
		}
		mcpttID, active, err := server.IdMS.Introspect(request.Context(), token)
		if err != nil {
			fail(writer, http.StatusServiceUnavailable, "IdMS unavailable", nil)
			return
		}
		if !active {
			fail(writer, http.StatusUnauthorized, "token not active", nil)
			return
		}
		_, doc, err := server.Store.Current(request.Context(), server.Domain)
		if err != nil {
			fail(writer, http.StatusInternalServerError, "store unavailable", nil)
			return
		}
		login, err := administrator(doc, mcpttID)
		if err != nil {
			server.record(request.Context(), mcpttID, "sign-in-refused", request.Method+" "+request.URL.Path, map[string]any{"reason": "not an active administrator"})
			fail(writer, http.StatusForbidden, "not an administrator", nil)
			return
		}
		handler(writer, request, login)
	}
}

// ------------------------------------------------------------------------------
// healthz answers 200 once the database answers and, with Ready set, the projections the services start from are
// written; 503 otherwise.
func (server *Server) healthz(writer http.ResponseWriter, request *http.Request) {
	if err := server.Store.Ping(request.Context()); err != nil {
		fail(writer, http.StatusServiceUnavailable, "database unreachable", nil)
		return
	}
	if server.Ready != nil && !server.Ready(request.Context()) {
		fail(writer, http.StatusServiceUnavailable, "projections not written yet", nil)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

// ------------------------------------------------------------------------------
// etag is the ETag of a revision: its number, quoted.
func etag(rev int64) string { return `"` + strconv.FormatInt(rev, 10) + `"` }

// ------------------------------------------------------------------------------
// ifMatch is the revision a write is based on; writes without one are refused.
func ifMatch(request *http.Request) (int64, bool) {
	value := strings.Trim(request.Header.Get("If-Match"), `" `)
	revision, err := strconv.ParseInt(value, 10, 64)
	return revision, err == nil && revision >= 0
}

// ------------------------------------------------------------------------------
// getConfig answers the current document, with its revision as the ETag.
func (server *Server) getConfig(writer http.ResponseWriter, request *http.Request, _ string) {
	rev, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	writer.Header().Set("ETag", etag(rev))
	writeJSON(writer, http.StatusOK, doc)
}

// ------------------------------------------------------------------------------
// listUsers answers a page of users sorted by login, filtered by a search over login, display name, e-mail and
// MCPTT ID (?q=) and by status; limit defaults to 100 and is at most 1000.
func (server *Server) listUsers(writer http.ResponseWriter, request *http.Request, _ string) {
	rev, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	search := strings.ToLower(request.URL.Query().Get("q"))
	status := request.URL.Query().Get("status")
	offset, _ := strconv.Atoi(request.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(request.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var found []config.User
	for _, user := range doc.Users {
		text := strings.ToLower(user.Login + " " + user.DisplayName + " " + user.Email + " " + user.MCPTTID)
		if (search == "" || strings.Contains(text, search)) && (status == "" || user.Status == status) {
			found = append(found, user)
		}
	}
	sort.Slice(found, func(left, right int) bool { return found[left].Login < found[right].Login })
	total := len(found)
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	writer.Header().Set("ETag", etag(rev))
	writeJSON(writer, http.StatusOK, map[string]any{"total": total, "offset": offset, "users": orEmpty(found[offset:end])})
}

// ------------------------------------------------------------------------------
// orEmpty is the users, or an empty list for none, so that the JSON holds [] and not null.
func orEmpty(users []config.User) []config.User {
	if users == nil {
		return []config.User{}
	}
	return users
}

// ------------------------------------------------------------------------------
// getUser answers one user and its effective profile (its template with its own profile merged over it).
func (server *Server) getUser(writer http.ResponseWriter, request *http.Request, _ string) {
	rev, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	for _, user := range doc.Users {
		if user.Login == request.PathValue("login") {
			writer.Header().Set("ETag", etag(rev))
			writeJSON(writer, http.StatusOK, map[string]any{"user": user, "effective_profile": doc.EffectiveProfile(user)})
			return
		}
	}
	fail(writer, http.StatusNotFound, "no such user", nil)
}

// ------------------------------------------------------------------------------
// commit writes a changed document and answers with the revision and its changes.
func (server *Server) commit(writer http.ResponseWriter, request *http.Request, expected int64, doc *config.Document, actor, reason, source string) {
	rev, changes, err := server.Store.Commit(request.Context(), expected, doc, actor, reason, source)
	var invalid *store.ErrInvalid
	switch {
	case errors.Is(err, store.ErrStale):
		fail(writer, http.StatusPreconditionFailed, err.Error(), nil)
	case errors.As(err, &invalid):
		fail(writer, http.StatusUnprocessableEntity, "invalid configuration", invalid.Problems)
	case err != nil:
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
	default:
		writer.Header().Set("ETag", etag(rev))
		writeJSON(writer, http.StatusOK, map[string]any{"revision": rev, "changes": orChanges(changes)})
	}
}

// ------------------------------------------------------------------------------
// orChanges is the changes, or an empty list for none.
func orChanges(changes []config.Change) []config.Change {
	if changes == nil {
		return []config.Change{}
	}
	return changes
}

// ------------------------------------------------------------------------------
// base reads the current document for a write, refusing a missing or stale If-Match.
func (server *Server) base(writer http.ResponseWriter, request *http.Request) (int64, *config.Document, bool) {
	expected, ok := ifMatch(request)
	if !ok {
		fail(writer, http.StatusPreconditionRequired, "If-Match with the revision the change is based on is required", nil)
		return 0, nil, false
	}
	rev, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return 0, nil, false
	}
	if rev != expected {
		fail(writer, http.StatusPreconditionFailed, store.ErrStale.Error(), map[string]int64{"current": rev})
		return 0, nil, false
	}
	return rev, doc, true
}

// ------------------------------------------------------------------------------
// putUser creates or replaces a user from the JSON body (unknown fields refused, the login the path's) on the
// If-Match revision; the groups on the user's list gain it as a member.
func (server *Server) putUser(writer http.ResponseWriter, request *http.Request, actor string) {
	rev, doc, ok := server.base(writer, request)
	if !ok {
		return
	}
	var user config.User
	dec := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&user); err != nil {
		fail(writer, http.StatusBadRequest, "user: "+err.Error(), nil)
		return
	}
	if user.Login != request.PathValue("login") {
		fail(writer, http.StatusBadRequest, "the login in the body differs from the path", nil)
		return
	}
	before := doc.Clone()
	replaced := false
	for index := range doc.Users {
		if doc.Users[index].Login == user.Login {
			doc.Users[index], replaced = user, true
		}
	}
	if !replaced {
		doc.Users = append(doc.Users, user)
	}
	// The groups on the user's list gain the user as a member (slice D4).
	doc.FollowUserGroups(before)
	server.commit(writer, request, rev, doc, actor, "user "+user.Login+" saved", "api")
}

// ------------------------------------------------------------------------------
// setStatus is the handler that sets a user's account status on the If-Match revision (suspend: suspended, restore:
// active).
func (server *Server) setStatus(status string) func(http.ResponseWriter, *http.Request, string) {
	return func(writer http.ResponseWriter, request *http.Request, actor string) {
		rev, doc, ok := server.base(writer, request)
		if !ok {
			return
		}
		for index := range doc.Users {
			if doc.Users[index].Login == request.PathValue("login") {
				doc.Users[index].Status = status
				server.commit(writer, request, rev, doc, actor, "user "+doc.Users[index].Login+" "+status, "api")
				return
			}
		}
		fail(writer, http.StatusNotFound, "no such user", nil)
	}
}

// ------------------------------------------------------------------------------
// readUpload reads an upload's rows: an XLSX workbook, or CSV users with Content-Type text/csv; at most 64 MiB.
func readUpload(request *http.Request) (users, groups []sheet.Row, err error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 64<<20))
	if err != nil {
		return nil, nil, err
	}
	if strings.HasPrefix(request.Header.Get("Content-Type"), "text/csv") {
		users, err = sheet.ReadCSV(bytes.NewReader(body), sheet.UserColumns)
		return users, nil, err
	}
	return sheet.ReadXLSX(bytes.NewReader(body))
}

// ------------------------------------------------------------------------------
// upload validates a spreadsheet (mode=validate), or validates and applies it in
// batches (mode=apply). The body is an XLSX workbook, or CSV users with
// Content-Type text/csv (groups need the workbook).
func (server *Server) upload(writer http.ResponseWriter, request *http.Request, actor string) {
	mode := request.URL.Query().Get("mode")
	if mode != "validate" && mode != "apply" {
		fail(writer, http.StatusBadRequest, "mode must be validate or apply", nil)
		return
	}
	users, groups, err := readUpload(request)
	if err != nil {
		fail(writer, http.StatusBadRequest, err.Error(), nil)
		return
	}
	var rev int64
	var doc *config.Document
	if mode == "apply" {
		var ok bool
		if rev, doc, ok = server.base(writer, request); !ok {
			return
		}
	} else if rev, doc, err = server.Store.Current(request.Context(), server.Domain); err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	// The whole upload is validated first; with any problem nothing is applied.
	res := sheet.Prepare(doc, users, groups)
	preview := config.Diff(doc, res.Document)
	if len(res.Problems) > 0 || mode == "validate" {
		status := http.StatusOK
		if len(res.Problems) > 0 {
			status = http.StatusUnprocessableEntity
		}
		writeJSON(writer, status, map[string]any{"revision": rev, "users": res.Users, "groups": res.Groups,
			"problems": res.Problems, "changes": orChanges(preview)})
		return
	}
	// Apply in batches of users, one revision each: each step validates, so
	// a failed batch leaves the earlier ones applied and can be retried alone.
	var revisions []int64
	for index, step := range sheet.Batches(doc, res.Document, users, BatchSize) {
		reason := fmt.Sprintf("upload batch %d", index+1)
		next, _, err := server.Store.Commit(request.Context(), rev, step, actor, reason, "upload")
		if err != nil {
			fail(writer, http.StatusConflict, "batch failed: "+err.Error(), map[string]any{"applied_revisions": revisions, "failed_batch": index + 1})
			return
		}
		if next != rev {
			revisions = append(revisions, next)
		}
		rev = next
	}
	writer.Header().Set("ETag", etag(rev))
	writeJSON(writer, http.StatusOK, map[string]any{"revision": rev, "revisions": revisions, "changes": orChanges(preview)})
}

// ------------------------------------------------------------------------------
// template answers the bulk-upload workbook of the current document (sheet.TemplateXLSX).
func (server *Server) template(writer http.ResponseWriter, request *http.Request, _ string) {
	_, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	raw, err := sheet.TemplateXLSX(doc)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	writer.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	writer.Header().Set("Content-Disposition", `attachment; filename="openmcx-upload.xlsx"`)
	_, _ = writer.Write(raw)
}

// ------------------------------------------------------------------------------
// revisions lists up to 200 revisions after ?after=, oldest first.
func (server *Server) revisions(writer http.ResponseWriter, request *http.Request, _ string) {
	after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	list, err := server.Store.Revisions(request.Context(), after, 200)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"revisions": list})
}

// ------------------------------------------------------------------------------
// revisionParam is the revision number of the path ({n}); it answers 400 for one that is not a positive number.
func revisionParam(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	revision, err := strconv.ParseInt(request.PathValue("n"), 10, 64)
	if err != nil || revision <= 0 {
		fail(writer, http.StatusBadRequest, "revision must be a positive number", nil)
		return 0, false
	}
	return revision, true
}

// ------------------------------------------------------------------------------
// export answers the document of one revision as a JSON attachment.
func (server *Server) export(writer http.ResponseWriter, request *http.Request, _ string) {
	revision, ok := revisionParam(writer, request)
	if !ok {
		return
	}
	doc, err := server.Store.Document(request.Context(), revision)
	if err != nil {
		fail(writer, http.StatusNotFound, "no such revision", nil)
		return
	}
	writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="mcxadmin-revision-%d.json"`, revision))
	writeJSON(writer, http.StatusOK, doc)
}

// ------------------------------------------------------------------------------
// rollback applies an earlier revision's document as a new revision; with ?preview= it
// only answers the changes it would make.
func (server *Server) rollback(writer http.ResponseWriter, request *http.Request, actor string) {
	revision, ok := revisionParam(writer, request)
	if !ok {
		return
	}
	target, err := server.Store.Document(request.Context(), revision)
	if err != nil {
		fail(writer, http.StatusNotFound, "no such revision", nil)
		return
	}
	if request.URL.Query().Get("preview") != "" {
		rev, doc, err := server.Store.Current(request.Context(), server.Domain)
		if err != nil {
			fail(writer, http.StatusInternalServerError, err.Error(), nil)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"revision": rev, "changes": orChanges(config.Diff(doc, target))})
		return
	}
	rev, _, ok := server.base(writer, request)
	if !ok {
		return
	}
	server.commit(writer, request, rev, target, actor, fmt.Sprintf("rollback to revision %d", revision), "rollback")
}

// ------------------------------------------------------------------------------
// audit answers the audit trail: with ?latest=, the newest entries first (at most 1000); otherwise up to 500
// entries after ?after=, oldest first.
func (server *Server) audit(writer http.ResponseWriter, request *http.Request, _ string) {
	if latest, _ := strconv.Atoi(request.URL.Query().Get("latest")); latest > 0 {
		// The newest entries first, for the console's audit view.
		list, err := server.Store.RecentAudit(request.Context(), min(latest, 1000))
		if err != nil {
			fail(writer, http.StatusInternalServerError, err.Error(), nil)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"audit": list})
		return
	}
	after, _ := strconv.ParseInt(request.URL.Query().Get("after"), 10, 64)
	list, err := server.Store.AuditTrail(request.Context(), after, 500)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"audit": list})
}

// ------------------------------------------------------------------------------
// projections answers the outbox rows counted by consumer and status: how far each projection has applied the
// revisions.
func (server *Server) projections(writer http.ResponseWriter, request *http.Request, _ string) {
	counts, err := server.Store.Outbox(request.Context())
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"outbox": counts})
}

// ------------------------------------------------------------------------------
// account answers IdMS (slice B2): the status and identities of an account, by
// login or MCPTT ID. Only IdMS, with the server client credentials, may ask.
func (server *Server) account(writer http.ResponseWriter, request *http.Request) {
	user, pw, ok := request.BasicAuth()
	if !ok || server.ServerClientID == "" || !secureEqual(user, server.ServerClientID) || !secureEqual(pw, server.ServerSecret) {
		fail(writer, http.StatusUnauthorized, "unauthorized", nil)
		return
	}
	_, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusServiceUnavailable, "store unavailable", nil)
		return
	}
	login, mcptt := request.URL.Query().Get("login"), request.URL.Query().Get("mcptt_id")
	if login != "" {
		// IdMS asks by login when the user signs in: the dashboard's last sign-in.
		server.signIns.note(login)
	}
	for _, user := range doc.Users {
		if (login != "" && user.Login == login) || (mcptt != "" && user.MCPTTID == mcptt) {
			writeJSON(writer, http.StatusOK, map[string]any{"login": user.Login, "status": user.Status, "mc_id": user.MCID,
				"mcptt_id": user.MCPTTID, "sip_uri": user.SIPURI, "client_ids": user.ClientIDs, "mcdata_id": user.MCDataID,
				"mcvideo_id": user.MCVideoID})
			return
		}
	}
	fail(writer, http.StatusNotFound, "no such account", nil)
}

// ------------------------------------------------------------------------------
// passwordLink gets a one-time link from IdMS on which the user sets (or, with
// ?purpose=reset, resets) its password. The link is returned once; the audit
// records that one was issued, not the link.
func (server *Server) passwordLink(writer http.ResponseWriter, request *http.Request, actor string) {
	purpose := request.URL.Query().Get("purpose")
	if purpose == "" {
		purpose = "set"
	}
	if purpose != "set" && purpose != "reset" {
		fail(writer, http.StatusBadRequest, "purpose must be set or reset", nil)
		return
	}
	_, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	login := request.PathValue("login")
	found := false
	for _, user := range doc.Users {
		found = found || (user.Login == login && user.Status != "retired")
	}
	if !found {
		fail(writer, http.StatusNotFound, "no such user", nil)
		return
	}
	link, expires, err := server.Links.PasswordLink(request.Context(), login, purpose)
	if err != nil {
		fail(writer, http.StatusBadGateway, "IdMS could not issue the link", nil)
		return
	}
	server.record(request.Context(), actor, "password-link", login, map[string]any{"purpose": purpose})
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{"url": link, "expires": expires})
}

// ------------------------------------------------------------------------------
// secureEqual compares two strings in constant time (the server client credentials).
func secureEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

// ------------------------------------------------------------------------------
// siteUser answers the CMS (slice C): an active user as the servers read it from
// site.json (identities and effective profile, no secret), by MCPTT, MCData or
// MCVideo ID, from which the CMS renders the user profile documents (TS 24.484 clause 8.3, clause 9.3 and
// clause 10.3). Only the server client credentials may ask.
func (server *Server) siteUser(writer http.ResponseWriter, request *http.Request) {
	user, pw, ok := request.BasicAuth()
	if !ok || server.ServerClientID == "" || !secureEqual(user, server.ServerClientID) || !secureEqual(pw, server.ServerSecret) {
		fail(writer, http.StatusUnauthorized, "unauthorized", nil)
		return
	}
	_, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusServiceUnavailable, "store unavailable", nil)
		return
	}
	query := request.URL.Query()
	for _, user := range doc.Users {
		match := (query.Get("mcptt_id") != "" && user.MCPTTID == query.Get("mcptt_id")) ||
			(query.Get("mcdata_id") != "" && user.MCDataID == query.Get("mcdata_id")) ||
			(query.Get("mcvideo_id") != "" && user.MCVideoID == query.Get("mcvideo_id"))
		if match && user.Status == "active" {
			out := doc.SiteUser(user)
			if video := doc.MCVideoGroups(user); len(video) > 0 {
				out["mcvideo_groups"] = video
			}
			if data := doc.MCDataGroups(user); len(data) > 0 {
				out["mcdata_groups"] = data
			}
			writeJSON(writer, http.StatusOK, out)
			return
		}
	}
	fail(writer, http.StatusNotFound, "no such active user", nil)
}

// ------------------------------------------------------------------------------
// newActivation issues a one-time activation URL for a user's client, with its QR
// code (PNG, base64). The code is returned once and stored only as a hash; a new
// one ends the user's earlier unused ones.
func (server *Server) newActivation(writer http.ResponseWriter, request *http.Request, actor string) {
	if server.Activation == nil {
		fail(writer, http.StatusNotFound, "activation is not configured", nil)
		return
	}
	_, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	login := request.PathValue("login")
	found := false
	for _, user := range doc.Users {
		found = found || (user.Login == login && (user.Status == "active" || user.Status == "pending"))
	}
	if !found {
		fail(writer, http.StatusNotFound, "no such user to activate", nil)
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		fail(writer, http.StatusInternalServerError, "random", nil)
		return
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	ttl := time.Duration(server.Activation.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 72 * time.Hour
	}
	expires := time.Now().Add(ttl)
	if err := server.Store.NewActivation(request.Context(), codeHash(code), login, actor, expires); err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	link := strings.TrimSuffix(server.Activation.BaseURL, "/") + "/activate/" + code
	if server.Activation.Pin != "" {
		link += "#pin=" + server.Activation.Pin
	}
	image, err := qr.Encode(link, qr.M)
	if err != nil {
		fail(writer, http.StatusInternalServerError, "QR code", nil)
		return
	}
	server.record(request.Context(), actor, "activation-issued", login, map[string]any{"expires": expires.UTC().Format(time.RFC3339)})
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{"url": link, "expires": expires.Unix(),
		"qr_png": base64.StdEncoding.EncodeToString(image.PNG())})
}

// ------------------------------------------------------------------------------
// activate answers a client's activation URL, once: the user's bootstrap package, with the MCS UE initial
// configuration document of TS 24.484 clause 7.2. The code in the URL is the only credential; the package holds
// no secret.
func (server *Server) activate(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if server.Activation == nil {
		fail(writer, http.StatusNotFound, "not found", nil)
		return
	}
	// The code is used up only once the account is known to be active and its package is built: a refused
	// request leaves the link valid for a later attempt.
	var pkg map[string]any
	login, ok, err := server.Store.UseActivation(request.Context(), codeHash(request.PathValue("code")), func(login string) error {
		_, doc, err := server.Store.Current(request.Context(), server.Domain)
		if err != nil {
			return errStoreUnavailable
		}
		for _, user := range doc.Users {
			if user.Login == login && (user.Status == "active" || user.Status == "pending") {
				pkg, err = activation.Package(server.Activation, doc, user)
				return err
			}
		}
		return errAccountNotActive
	})
	switch {
	case errors.Is(err, errAccountNotActive):
		fail(writer, http.StatusNotFound, "this activation link is not valid: the account is not active", nil)
	case errors.Is(err, errStoreUnavailable):
		fail(writer, http.StatusServiceUnavailable, "store unavailable", nil)
	case err != nil && login != "":
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
	case err != nil:
		fail(writer, http.StatusServiceUnavailable, "store unavailable", nil)
	case !ok:
		fail(writer, http.StatusNotFound, "this activation link is not valid: used, expired or unknown", nil)
	default:
		server.record(request.Context(), login, "activated", login, map[string]any{})
		writeJSON(writer, http.StatusOK, pkg)
	}
}

// errAccountNotActive and errStoreUnavailable refuse an activation without using up its code.
var (
	errAccountNotActive = errors.New("account not active")
	errStoreUnavailable = errors.New("store unavailable")
)

// ------------------------------------------------------------------------------
// codeHash is the SHA-256 (hex) of an activation code, the only form of it the store keeps.
func codeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// ------------------------------------------------------------------------------
// record writes an audit entry; detail is marshalled to JSON. The request has already had its effect, so a failed
// write does not undo it: it is logged, not lost silently.
func (server *Server) record(ctx context.Context, actor, action, target string, detail any) {
	raw, err := json.Marshal(detail)
	if err == nil {
		err = server.Store.Audit(ctx, actor, action, target, string(raw))
	}
	if err != nil {
		log.Printf("audit %s %s by %s not recorded: %v", action, target, actor, err)
	}
}
