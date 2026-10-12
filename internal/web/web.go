//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Package web is the MCX Admin console (slice E1): pages embedded in the binary,
// which call the /v1 API in the browser. Sign-in is the IdMS authorization code
// flow with PKCE, run by this server: it sees the code, never the password, and
// keeps the access token in an HttpOnly cookie. Writes from the console also carry
// the X-MCX-Admin header, which a cross-site form cannot set.
//
// Specifications: TS 33.180 V19.4.0 (Rel-19), RFC 7636.
package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openMCX/openmcx-admin/internal/closer"
)

//go:embed static
var static embed.FS

// SessionCookie holds the administrator's access token.
const SessionCookie = "mcxadmin_session"

// CSRFHeader must accompany a write authorised by the session cookie.
const CSRFHeader = "X-MCX-Admin"

const loginCookie = "mcxadmin_login"

// Config is the console's sign-in: the IdMS OAuth client it uses.
type Config struct {
	// The IdMS authorize endpoint as browsers reach it.
	AuthorizeURL string `json:"authorize_url"`
	ClientID     string `json:"client_id"`
	// This server's /ui/callback as browsers reach it; registered at IdMS.
	RedirectURI string `json:"redirect_uri"`
}

// Console serves the pages and the sign-in.
type Console struct {
	Config
	// The IdMS token endpoint as this server reaches it, and the client for it.
	TokenURL string
	Client   *http.Client
}

// ------------------------------------------------------------------------------
// Routes adds the console to mux.
func (console *Console) Routes(mux *http.ServeMux) {
	files, err := fs.Sub(static, "static")
	if err != nil {
		panic("web: the embedded static directory: " + err.Error()) // a build error, not a run-time condition
	}
	assets := http.StripPrefix("/ui/static/", http.FileServer(http.FS(files)))
	mux.Handle("GET /ui/static/", pageHeaders(assets))
	mux.Handle("GET /{$}", pageHeaders(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.ServeFileFS(writer, request, files, "index.html")
	})))
	mux.HandleFunc("GET /ui/login", console.login)
	mux.HandleFunc("GET /ui/callback", console.callback)
	mux.HandleFunc("POST /ui/logout", console.logout)
}

// ------------------------------------------------------------------------------
// pageHeaders adds the console pages' security headers: a self-only content security policy, no framing, no
// sniffing and no referrer.
func pageHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(writer, request)
	})
}

// ------------------------------------------------------------------------------
// random is 32 random bytes, base64url without padding (state, PKCE verifier, nonce). crypto/rand.Read never
// returns an error (Go 1.24 and later end the program instead).
func random() string {
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}

// ------------------------------------------------------------------------------
// cookie is an HttpOnly, Secure, SameSite=Lax cookie; a negative maxAge deletes it.
func cookie(name, value, path string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}
}

// ------------------------------------------------------------------------------
// login starts the code flow with the authentication request of TS 33.180 annex B.4.2.2 (response_type code,
// scope openid, state, acr_values 3gpp:acr:password, and the S256 code_challenge of RFC 7636 section 4.2): state
// and PKCE verifier in a short-lived cookie. The console uses only the access token, which the API introspects on
// every request, never the ID token; the nonce (not in TS 33.180 table B.4.2.2-1) is sent only because earlier
// IdMS versions require it, so there is nothing to check it against.
func (console *Console) login(writer http.ResponseWriter, request *http.Request) {
	state, verifier := random(), random()
	digest := sha256.Sum256([]byte(verifier))
	query := url.Values{"response_type": {"code"}, "client_id": {console.ClientID}, "redirect_uri": {console.RedirectURI},
		"scope": {"openid"}, "state": {state}, "acr_values": {"3gpp:acr:password"}, "nonce": {random()},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}}
	http.SetCookie(writer, cookie(loginCookie, state+"."+verifier, "/ui/", 300))
	writer.Header().Set("Cache-Control", "no-store")
	http.Redirect(writer, request, console.AuthorizeURL+"?"+query.Encode(), http.StatusFound)
}

// ------------------------------------------------------------------------------
// callback checks the state of the authentication response (TS 33.180 annex B.4.2.3), redeems the code with the
// access token request of TS 33.180 annex B.4.2.4 (with the PKCE code_verifier) and opens the session with the
// access token of the response (TS 33.180 annex B.4.2.5).
func (console *Console) callback(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	http.SetCookie(writer, cookie(loginCookie, "", "/ui/", -1))
	saved, err := request.Cookie(loginCookie)
	state, verifier, _ := strings.Cut(func() string {
		if err != nil {
			return ""
		}
		return saved.Value
	}(), ".")
	query := request.URL.Query()
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(query.Get("state"))) != 1 {
		http.Redirect(writer, request, "/?signin=expired", http.StatusFound)
		return
	}
	if query.Get("error") != "" || query.Get("code") == "" {
		http.Redirect(writer, request, "/?signin=failed", http.StatusFound)
		return
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {query.Get("code")}, "client_id": {console.ClientID},
		"redirect_uri": {console.RedirectURI}, "code_verifier": {verifier}}
	client := console.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.PostForm(console.TokenURL, form)
	if err != nil {
		http.Redirect(writer, request, "/?signin=unavailable", http.StatusFound)
		return
	}
	defer closer.Quietly("response body", resp.Body)
	var tokens struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&tokens) != nil || tokens.AccessToken == "" {
		http.Redirect(writer, request, "/?signin=failed", http.StatusFound)
		return
	}
	if tokens.ExpiresIn <= 0 {
		tokens.ExpiresIn = 3600
	}
	http.SetCookie(writer, cookie(SessionCookie, tokens.AccessToken, "/", tokens.ExpiresIn))
	http.Redirect(writer, request, "/", http.StatusFound)
}

// ------------------------------------------------------------------------------
// logout ends the console session: it deletes the session cookie, for a request carrying the console header.
func (console *Console) logout(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get(CSRFHeader) != "1" {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return
	}
	http.SetCookie(writer, cookie(SessionCookie, "", "/", -1))
	writer.WriteHeader(http.StatusNoContent)
}
