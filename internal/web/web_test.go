//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Tests of the console sign-in: the pages, the code flow with PKCE, the callback and the logout.
//
// Specifications: TS 33.180 V19.4.0 (Rel-19), RFC 7636.
package web

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ------------------------------------------------------------------------------
// TestSignIn: the page and its script are served with their security headers; login redirects with the state and
// S256 challenge of the saved verifier and acr_values 3gpp:acr:password (TS 33.180 annex B.4.2.2); a callback with another state or an IdMS error
// opens no session; a good one redeems the code with the verifier and sets the session cookie; logout needs the
// console header.
func TestSignIn(test *testing.T) {
	idms, gotVerifier := fakeTokenEndpoint(test)
	console := &Console{Config: Config{AuthorizeURL: "https://idms.test/authorize", ClientID: "mcx-client",
		RedirectURI: "https://admin.test/ui/callback"}, TokenURL: idms.URL + "/token"}
	mux := http.NewServeMux()
	console.Routes(mux)
	do := func(method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		return serve(mux, method, target, cookies...)
	}
	page := do("GET", "/")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "openMCX Admin") ||
		!strings.Contains(page.Header().Get("Content-Security-Policy"), "default-src 'self'") {
		test.Fatalf("page %d %v", page.Code, page.Header())
	}
	if do("GET", "/ui/static/app.js").Code != 200 {
		test.Fatal("no script")
	}

	start := do("GET", "/ui/login")
	to, _ := url.Parse(start.Header().Get("Location"))
	query := to.Query()
	login := cookieNamed(start, loginCookie)
	if start.Code != http.StatusFound || !strings.HasPrefix(to.String(), "https://idms.test/authorize?") || query.Get("code_challenge_method") != "S256" ||
		query.Get("acr_values") != "3gpp:acr:password" || login == nil || !login.HttpOnly || !login.Secure {
		test.Fatalf("login %d %s", start.Code, to)
	}
	state, verifier, _ := strings.Cut(login.Value, ".")
	digest := sha256.Sum256([]byte(verifier))
	if query.Get("state") != state || query.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) {
		test.Fatal("state or challenge do not match the saved verifier")
	}

	// Another state: refused, no session.
	bad := do("GET", "/ui/callback?code=good-code&state=other", login)
	if cookieNamed(bad, SessionCookie) != nil || bad.Header().Get("Location") != "/?signin=expired" {
		test.Fatalf("state mismatch: %s", bad.Header().Get("Location"))
	}
	// IdMS refused the sign-in.
	denied := do("GET", "/ui/callback?error=access_denied&state="+state, login)
	if cookieNamed(denied, SessionCookie) != nil || denied.Header().Get("Location") != "/?signin=failed" {
		test.Fatalf("denied: %s", denied.Header().Get("Location"))
	}
	done := do("GET", "/ui/callback?code=good-code&state="+state, login)
	session := cookieNamed(done, SessionCookie)
	if done.Header().Get("Location") != "/" || session == nil || session.Value != "tok-1" || !session.HttpOnly || session.MaxAge != 600 ||
		*gotVerifier != verifier {
		test.Fatalf("callback %s %+v", done.Header().Get("Location"), session)
	}

	if do("POST", "/ui/logout", session).Code != http.StatusForbidden {
		test.Fatal("logout without the console header")
	}
	req := httptest.NewRequest("POST", "/ui/logout", nil)
	req.Header.Set(CSRFHeader, "1")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if out := cookieNamed(rr, SessionCookie); rr.Code != http.StatusNoContent || out == nil || out.MaxAge >= 0 {
		test.Fatalf("logout %d", rr.Code)
	}
}

// ------------------------------------------------------------------------------
// fakeTokenEndpoint is an IdMS token endpoint that redeems "good-code" of client mcx-client for the access token
// tok-1 (600 s), and keeps the code_verifier it was sent.
func fakeTokenEndpoint(test *testing.T) (*httptest.Server, *string) {
	test.Helper()
	verifier := new(string)
	idms := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := request.ParseForm(); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		*verifier = request.Form.Get("code_verifier")
		if request.Form.Get("code") != "good-code" || request.Form.Get("client_id") != "mcx-client" {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if _, err := writer.Write([]byte(`{"access_token":"tok-1","expires_in":600}`)); err != nil {
			test.Error(err)
		}
	}))
	test.Cleanup(idms.Close)
	return idms, verifier
}

// ------------------------------------------------------------------------------
// serve runs one request with cookies through mux and returns its recorded answer.
func serve(mux *http.ServeMux, method, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ------------------------------------------------------------------------------
// cookieNamed is the cookie of that name an answer sets, nil when it sets none.
func cookieNamed(rr *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, ck := range rr.Result().Cookies() {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}
