//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Tests of the API: sign-in, writes, uploads, rollback, accounts and links, activation, the console session,
// status, groups and templates, audit, load, device revocation and presence.
//
// OpenMCX administration (no 3GPP specification).
package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/openMCX/openmcx-admin/internal/activation"
	"github.com/openMCX/openmcx-admin/internal/closer"
	"github.com/openMCX/openmcx-admin/internal/config"
	"github.com/openMCX/openmcx-admin/internal/store"
	"github.com/openMCX/openmcx-admin/internal/store/storetest"
	"github.com/openMCX/openmcx-admin/internal/web"
)

// fakeIdMS answers introspection from a token table.
type fakeIdMS map[string]string

// ------------------------------------------------------------------------------
// Introspect answers a token active, with its MCPTT ID, when the table holds it.
func (fake fakeIdMS) Introspect(_ context.Context, token string) (string, bool, error) {
	id, ok := fake[token]
	return id, ok, nil
}

// ------------------------------------------------------------------------------
// PasswordLink answers a fixed one-time link for the login.
func (fake fakeIdMS) PasswordLink(_ context.Context, login, purpose string) (string, int64, error) {
	return "https://idms.mcx.test/set-password?token=one-time-" + login, 1, nil
}

// ------------------------------------------------------------------------------
// setup opens a fresh database on the MySQL of MCXADMIN_TEST_MYSQL (a DSN without
// a database, e.g. root:pw@tcp(mysql:3306)/?parseTime=true) and seeds it.
func setup(test *testing.T) (*httptest.Server, *store.Store, *sql.DB) {
	test.Helper()
	st, db := storetest.Open(test)
	seed, err := config.Parse([]byte(`{"schema_version":1,"system":{"domain":"mcx.test"},
		"templates":{"field-user":{"private_call":{"allow":true}}},
		"users":[
		 {"login":"admin","status":"active","mc_id":"sip:admin.mc@mcx.test","mcptt_id":"sip:admin.mcptt@mcx.test","sip_uri":"sip:admin@mcx.test","client_ids":["c-admin"],"groups":[],"profile":{"config_management":true}},
		 {"login":"bob","status":"active","template":"field-user","mc_id":"sip:bob.mc@mcx.test","mcptt_id":"sip:bob.mcptt@mcx.test","sip_uri":"sip:bob@mcx.test","client_ids":["c-bob"],"groups":["sip:ops@mcx.test"]}],
		"groups":[{"id":"sip:ops@mcx.test","members":["sip:bob.mcptt@mcx.test"],"protect_media":false,"protect_floor":false}]}`))
	if err != nil {
		test.Fatal(err)
	}
	if rev, _, err := st.Commit(context.Background(), 0, seed, "import", "seed", "import"); err != nil || rev != 1 {
		test.Fatalf("seed %d %v", rev, err)
	}
	idms := fakeIdMS{"admin-token": "sip:admin.mcptt@mcx.test", "bob-token": "sip:bob.mcptt@mcx.test",
		"ghost-token": "sip:ghost.mcptt@mcx.test"}
	srv := httptest.NewServer((&Server{Store: st, IdMS: idms, Links: idms, Domain: "mcx.test",
		ServerClientID: "mcx-server", ServerSecret: "server-secret", Activation: testActivation(test)}).Handler())
	test.Cleanup(srv.Close)
	return srv, st, db
}

type reply struct {
	status int
	etag   string
	body   map[string]any
}

// ------------------------------------------------------------------------------
// call sends one API request with an optional bearer token, If-Match revision and content type, and returns the
// status, the ETag without quotes and the JSON body.
func call(test *testing.T, srv *httptest.Server, method, path, token, ifMatch, contentType string, body []byte) reply {
	test.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", `"`+ifMatch+`"`)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		test.Fatal(err)
	}
	defer closer.Quietly("response body", resp.Body)
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return reply{resp.StatusCode, strings.Trim(resp.Header.Get("ETag"), `"`), out}
}

// ------------------------------------------------------------------------------
// TestSignIn: no token or an unknown one is 401; a user who is not an active administrator is 403 and audited
// without the token; a suspended administrator is refused too.
func TestSignIn(test *testing.T) {
	srv, st, _ := setup(test)
	for token, want := range map[string]int{"": 401, "unknown-token": 401, "ghost-token": 403, "bob-token": 403, "admin-token": 200} {
		if got := call(test, srv, "GET", "/v1/config", token, "", "", nil); got.status != want {
			test.Errorf("token %q: %d, want %d", token, got.status, want)
		}
	}
	trail, _ := st.AuditTrail(context.Background(), 0, 100)
	raw, _ := json.Marshal(trail)
	if !strings.Contains(string(raw), "sign-in-refused") || strings.Contains(string(raw), "-token") {
		test.Fatalf("audit %s", raw)
	}
	// A suspended administrator is refused too.
	call(test, srv, "POST", "/v1/users/admin/suspend", "admin-token", "1", "", nil)
	if got := call(test, srv, "GET", "/v1/config", "admin-token", "", "", nil); got.status != 403 {
		test.Fatalf("suspended admin: %d", got.status)
	}
}

// ------------------------------------------------------------------------------
// userJSON is a pending field-user of the ops group, as a PUT body.
func userJSON(login, mcptt string) []byte {
	return []byte(fmt.Sprintf(`{"login":%q,"status":"pending","template":"field-user","mc_id":"sip:%s.mc@mcx.test","mcptt_id":%q,
		"sip_uri":"sip:%s@mcx.test","client_ids":["c-%s"],"groups":["sip:ops@mcx.test"]}`, login, login, mcptt, login, login))
}

// ------------------------------------------------------------------------------
// TestWrites: a write needs the current revision as If-Match (428, 412); a duplicate identity is refused (422); of
// two concurrent writes on one revision one wins; every revision has its outbox row; no secret is stored.
func TestWrites(test *testing.T) {
	srv, st, db := setup(test)
	carol := userJSON("carol", "sip:carol.mcptt@mcx.test")
	if got := call(test, srv, "PUT", "/v1/users/carol", "admin-token", "", "", carol); got.status != 428 {
		test.Fatalf("without If-Match: %d", got.status)
	}
	if got := call(test, srv, "PUT", "/v1/users/carol", "admin-token", "0", "", carol); got.status != 412 {
		test.Fatalf("stale If-Match: %d", got.status)
	}
	got := call(test, srv, "PUT", "/v1/users/carol", "admin-token", "1", "", carol)
	if got.status != 200 || got.etag != "2" {
		test.Fatalf("create: %d %v", got.status, got.body)
	}
	// An invalid document is refused and changes nothing.
	dup := userJSON("dave", "sip:carol.mcptt@mcx.test")
	if got := call(test, srv, "PUT", "/v1/users/dave", "admin-token", "2", "", dup); got.status != 422 {
		test.Fatalf("duplicate identity: %d %v", got.status, got.body)
	}
	// Two writes on the same revision: one is refused.
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, login := range []string{"erin", "frank"} {
		wg.Add(1)
		go func(login string) {
			defer wg.Done()
			statuses <- call(test, srv, "PUT", "/v1/users/"+login, "admin-token", "2", "", userJSON(login, "sip:"+login+".mcptt@mcx.test")).status
		}(login)
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[412] != 1 {
		test.Fatalf("concurrent writes: %v", counts)
	}
	// Each revision has its outbox rows, written with it.
	health, _ := st.Outbox(context.Background())
	if health["userdb"]["pending"] != 3 || len(health) != 1 {
		test.Fatalf("outbox %v", health)
	}
	// Suspend, then the change list says so.
	got = call(test, srv, "POST", "/v1/users/carol/suspend", "admin-token", "3", "", nil)
	if got.status != 200 || !strings.Contains(fmt.Sprint(got.body["changes"]), "suspended") {
		test.Fatalf("suspend %d %v", got.status, got.body)
	}
	// No secret is stored.
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM revisions WHERE document LIKE '%password%' OR document LIKE '%token%'`).Scan(&count)
	if count != 0 {
		test.Fatalf("%d revisions hold a secret", count)
	}
}

// ------------------------------------------------------------------------------
// TestUploadAndRollback: a bad CSV upload is refused; validation applies nothing; an apply in batches of 2 makes
// three revisions; a revision exports; a rollback to the seed restores it as a new revision with source rollback.
func TestUploadAndRollback(test *testing.T) {
	srv, st, _ := setup(test)
	var builder strings.Builder
	builder.WriteString("login,template,groups,services\n")
	for index := 0; index < 5; index++ {
		fmt.Fprintf(&builder, "u%d,field-user,ops,mcptt\n", index)
	}
	csv := []byte(builder.String())
	bad := []byte("login,template\nb@d,field-user\n")
	if got := call(test, srv, "POST", "/v1/uploads?mode=validate", "admin-token", "", "text/csv", bad); got.status != 422 {
		test.Fatalf("bad upload: %d", got.status)
	}
	got := call(test, srv, "POST", "/v1/uploads?mode=validate", "admin-token", "", "text/csv", csv)
	// Five users, and ops gaining them as members (one membership, slice D4).
	if got.status != 200 || len(got.body["changes"].([]any)) != 6 {
		test.Fatalf("validate: %d %v", got.status, got.body)
	}
	if rev, _, _ := st.Current(context.Background(), "mcx.test"); rev != 1 {
		test.Fatal("validation applied something")
	}
	BatchSize = 2
	defer func() { BatchSize = 500 }()
	got = call(test, srv, "POST", "/v1/uploads?mode=apply", "admin-token", "1", "text/csv", csv)
	if got.status != 200 || len(got.body["revisions"].([]any)) != 3 {
		test.Fatalf("apply in batches: %d %v", got.status, got.body)
	}
	_, doc, _ := st.Current(context.Background(), "mcx.test")
	if len(doc.Users) != 7 {
		test.Fatalf("%d users after the upload", len(doc.Users))
	}
	// Export and roll back to the seed.
	if got := call(test, srv, "GET", "/v1/revisions/1/export", "admin-token", "", "", nil); got.status != 200 {
		test.Fatalf("export %d", got.status)
	}
	got = call(test, srv, "POST", "/v1/revisions/1/rollback?preview=1", "admin-token", "", "", nil)
	if count := len(got.body["changes"].([]any)); count != 6 {
		test.Fatalf("rollback preview %d changes", count)
	}
	got = call(test, srv, "POST", "/v1/revisions/1/rollback", "admin-token", "4", "", nil)
	if got.status != 200 {
		test.Fatalf("rollback %d %v", got.status, got.body)
	}
	_, doc, _ = st.Current(context.Background(), "mcx.test")
	seed, _ := st.Document(context.Background(), 1)
	if len(config.Diff(seed, doc)) != 0 {
		test.Fatal("rollback did not restore revision 1")
	}
	list, _ := st.Revisions(context.Background(), 0, 100)
	if last := list[len(list)-1]; last.Source != "rollback" || last.Actor != "admin" {
		test.Fatalf("last revision %+v", last)
	}
}

// ------------------------------------------------------------------------------
// Slice B2: IdMS reads accounts with the server client credentials, and an
// administrator gets a set-password link once, never recorded.
func TestAccountsAndLinks(test *testing.T) {
	srv, st, _ := setup(test)
	get := func(query, user, pw string) (int, map[string]any) {
		req, _ := http.NewRequest("GET", srv.URL+"/internal/v1/accounts?"+query, nil)
		if user != "" {
			req.SetBasicAuth(user, pw)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			test.Fatal(err)
		}
		defer closer.Quietly("response body", resp.Body)
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, _ := get("login=bob", "", ""); code != 401 {
		test.Fatalf("without credentials: %d", code)
	}
	if code, _ := get("login=bob", "mcx-server", "wrong"); code != 401 {
		test.Fatalf("wrong credentials: %d", code)
	}
	if code, account := get("login=bob", "mcx-server", "server-secret"); code != 200 || account["mcptt_id"] != "sip:bob.mcptt@mcx.test" || account["status"] != "active" {
		test.Fatalf("by login: %d %v", code, account)
	}
	if code, account := get("mcptt_id=sip:bob.mcptt@mcx.test", "mcx-server", "server-secret"); code != 200 || account["login"] != "bob" {
		test.Fatalf("by MCPTT ID: %d %v", code, account)
	}
	if code, _ := get("login=nobody", "mcx-server", "server-secret"); code != 404 {
		test.Fatalf("unknown: %d", code)
	}
	if got := call(test, srv, "POST", "/v1/users/bob/password-link", "bob-token", "", "", nil); got.status != 403 {
		test.Fatalf("a non-administrator asks for a link: %d", got.status)
	}
	got := call(test, srv, "POST", "/v1/users/bob/password-link?purpose=reset", "admin-token", "", "", nil)
	if got.status != 200 || got.body["url"] != "https://idms.mcx.test/set-password?token=one-time-bob" {
		test.Fatalf("link %d %v", got.status, got.body)
	}
	trail, _ := st.AuditTrail(context.Background(), 0, 100)
	raw, _ := json.Marshal(trail)
	if !strings.Contains(string(raw), "password-link") || strings.Contains(string(raw), "one-time") {
		test.Fatalf("audit %s", raw)
	}
}

// ------------------------------------------------------------------------------
// testActivation is the activation configuration of the tests, with a placeholder trust root and pin.
func testActivation(test *testing.T) *activation.Config {
	root := filepath.Join(test.TempDir(), "ca.crt")
	_ = os.WriteFile(root, []byte("-----BEGIN CERTIFICATE-----\nTEST\n-----END CERTIFICATE-----\n"), 0o600)
	activationConfig := &activation.Config{BaseURL: "https://mcxadmin.mcx.test:8443", SIPCoreURI: "sip:kamailio:5060;transport=tcp",
		CredentialsURL: "https://credentials:8443", TrustRootFile: root,
		Endpoints: map[string]string{"cms": "https://cms:8443", "gms": "https://gms:8443", "kms": "https://kms:8443"}}
	activationConfig.IdMS.Issuer, activationConfig.IdMS.ClientID, activationConfig.IdMS.RedirectURI = "https://idms:8443", "mcx-client", "http://127.0.0.1/cb"
	activationConfig.Pin = "cGlucGlucGlucGlucGlucGlucGlucGlucGlucGlucGlu"
	activationConfig.Services.MCPTT, activationConfig.Services.MCData = "sip:mcptt@mcx.test", "sip:mcdata@mcx.test"
	return activationConfig
}

// ------------------------------------------------------------------------------
// TestActivation: only an administrator issues an activation, for a known user; a new code supersedes the earlier
// one; the package names the user's identities, group and UE initial configuration, and no password; a code works
// once; the audit records issue and use without the code.
func TestActivation(test *testing.T) {
	srv, st, _ := setup(test)
	if got := call(test, srv, "POST", "/v1/users/bob/activation", "bob-token", "", "", nil); got.status != 403 {
		test.Fatalf("a non-administrator issues an activation: %d", got.status)
	}
	if got := call(test, srv, "POST", "/v1/users/nobody/activation", "admin-token", "", "", nil); got.status != 404 {
		test.Fatalf("unknown user: %d", got.status)
	}
	first := call(test, srv, "POST", "/v1/users/bob/activation", "admin-token", "", "", nil)
	link, _ := first.body["url"].(string)
	if first.status != 200 || !strings.HasPrefix(link, "https://mcxadmin.mcx.test:8443/activate/") || first.body["qr_png"] == "" ||
		!strings.HasSuffix(link, "#pin=cGlucGlucGlucGlucGlucGlucGlucGlucGlucGlucGlu") {
		test.Fatalf("issue %d %v", first.status, first.body)
	}
	second := call(test, srv, "POST", "/v1/users/bob/activation", "admin-token", "", "", nil)
	fetch := func(url string) reply {
		return call(test, srv, "GET", strings.Replace(strings.Split(url, "#")[0], "https://mcxadmin.mcx.test:8443", "", 1), "", "", "", nil)
	}
	if got := fetch(link); got.status != 404 {
		test.Fatalf("a superseded code still works: %d", got.status)
	}
	pkg := fetch(second.body["url"].(string))
	sip, _ := pkg.body["sip"].(map[string]any)
	mcptt, _ := pkg.body["mcptt"].(map[string]any)
	initial, _ := pkg.body["ue_initial_configuration"].(string)
	if pkg.status != 200 || pkg.body["format"] != "openmcx-activation-1" || pkg.body["login"] != "bob" ||
		sip["identity"] != "sip:bob@mcx.test" || mcptt["user_id"] != "sip:bob.mcptt@mcx.test" || mcptt["group_id"] != "sip:ops@mcx.test" ||
		!strings.Contains(initial, "mcptt-UE-initial-configuration") || !strings.Contains(initial, "https://cms:8443/xcap-root") ||
		!strings.Contains(initial, `<Default-user-profile User-ID="sip:bob.mcptt@mcx.test" user-profile-index="1">`) ||
		mcptt["profile_index"] != "1" {
		test.Fatalf("package %d %v", pkg.status, pkg.body)
	}
	if _, ok := pkg.body["mcdata"]; ok {
		test.Fatal("bob has no MCData ID but got an mcdata section")
	}
	raw, _ := json.Marshal(pkg.body)
	if strings.Contains(strings.ToLower(string(raw)), "password") {
		test.Fatalf("the package holds a password: %s", raw)
	}
	if got := fetch(second.body["url"].(string)); got.status != 404 {
		test.Fatalf("a code works twice: %d", got.status)
	}
	trail, _ := st.AuditTrail(context.Background(), 0, 100)
	audit, _ := json.Marshal(trail)
	code := strings.Split(second.body["url"].(string)[strings.LastIndex(second.body["url"].(string), "/")+1:], "#")[0]
	if !strings.Contains(string(audit), "activation-issued") || !strings.Contains(string(audit), "activated") || strings.Contains(string(audit), code) {
		test.Fatalf("audit %s", audit)
	}
}

// ------------------------------------------------------------------------------
// TestActivationRefusedKeepsCode: a link opened while the account is suspended is refused without being used up;
// once the account is restored the same link activates the device, once.
func TestActivationRefusedKeepsCode(test *testing.T) {
	srv, _, _ := setup(test)
	issued := call(test, srv, "POST", "/v1/users/bob/activation", "admin-token", "", "", nil)
	link, _ := issued.body["url"].(string)
	path := strings.Replace(strings.Split(link, "#")[0], "https://mcxadmin.mcx.test:8443", "", 1)
	if got := call(test, srv, "POST", "/v1/users/bob/suspend", "admin-token", "1", "", nil); got.status != 200 {
		test.Fatalf("suspend %d %v", got.status, got.body)
	}
	if got := call(test, srv, "GET", path, "", "", "", nil); got.status != 404 || !strings.Contains(fmt.Sprint(got.body), "not active") {
		test.Fatalf("a suspended account activates: %d %v", got.status, got.body)
	}
	if got := call(test, srv, "POST", "/v1/users/bob/restore", "admin-token", "2", "", nil); got.status != 200 {
		test.Fatalf("restore %d %v", got.status, got.body)
	}
	if got := call(test, srv, "GET", path, "", "", "", nil); got.status != 200 || got.body["login"] != "bob" {
		test.Fatalf("the refused attempt used up the code: %d %v", got.status, got.body)
	}
	if got := call(test, srv, "GET", path, "", "", "", nil); got.status != 404 {
		test.Fatalf("a code works twice: %d", got.status)
	}
}

// ------------------------------------------------------------------------------
// TestConsoleSessionAndStatus: the console's session cookie signs in, a console write needs the CSRF header, the
// status reports accounts, probes and activity, and the console page is served.
func TestConsoleSessionAndStatus(test *testing.T) {
	_, st, _ := setup(test)
	health := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/down" {
			writer.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer health.Close()
	idms := fakeIdMS{"admin-token": "sip:admin.mcptt@mcx.test", "bob-token": "sip:bob.mcptt@mcx.test"}
	srv := httptest.NewServer((&Server{Store: st, IdMS: idms, Links: idms, Domain: "mcx.test", Activation: testActivation(test),
		Console: &web.Console{Config: web.Config{AuthorizeURL: "https://idms/authorize", ClientID: "c", RedirectURI: "https://a/ui/callback"}},
		Probes:  []Probe{{Name: "up", URL: health.URL + "/ok"}, {Name: "down", URL: health.URL + "/down"}, {Name: "closed", TCP: "127.0.0.1:1"}}}).Handler())
	defer srv.Close()
	send := func(method, path, cookie string, header bool) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.AddCookie(&http.Cookie{Name: web.SessionCookie, Value: cookie})
		if header {
			req.Header.Set(web.CSRFHeader, "1")
		}
		req.Header.Set("If-Match", `"1"`)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			test.Fatal(err)
		}
		defer closer.Quietly("response body", resp.Body)
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	if code, me := send("GET", "/v1/me", "admin-token", false); code != 200 || me["login"] != "admin" || me["console"] != true {
		test.Fatalf("me %d %v", code, me)
	}
	if code, _ := send("GET", "/v1/me", "bob-token", false); code != 403 {
		test.Fatalf("a non-administrator's session: %d", code)
	}
	if code, _ := send("POST", "/v1/users/bob/suspend", "admin-token", false); code != 403 {
		test.Fatalf("a console write without the header: %d", code)
	}
	if code, _ := send("POST", "/v1/users/bob/suspend", "admin-token", true); code != 200 {
		test.Fatalf("a console write: %d", code)
	}
	code, status := send("GET", "/v1/status", "admin-token", false)
	checkStatus(test, code, status)
	// The console is served; the API without a console has no pages.
	resp, err := http.Get(srv.URL + "/")
	if err != nil || resp.StatusCode != 200 {
		test.Fatalf("console page: %v %v", err, resp)
	}
	closer.Quietly("response body", resp.Body)
}

// ------------------------------------------------------------------------------
// checkStatus checks the dashboard status of TestConsoleSessionAndStatus: revision 2 with one active and one
// suspended user, the three probes (only "up" up) and the latest activity a commit.
func checkStatus(test *testing.T, code int, status map[string]any) {
	test.Helper()
	users, _ := status["users"].(map[string]any)
	services, _ := status["services"].([]any)
	if code != 200 || status["revision"] != float64(2) || users["active"] != float64(1) || users["suspended"] != float64(1) || len(services) != 3 {
		test.Fatalf("status %d %v", code, status)
	}
	ups := map[string]bool{}
	for _, service := range services {
		service := service.(map[string]any)
		ups[service["name"].(string)] = service["up"].(bool)
	}
	if !ups["up"] || ups["down"] || ups["closed"] {
		test.Fatalf("probes %v", ups)
	}
	activity, _ := status["activity"].([]any)
	if len(activity) == 0 || activity[0].(map[string]any)["action"] != "commit" {
		test.Fatalf("activity %v", activity)
	}
}

// ------------------------------------------------------------------------------
// currentUser is a user of the store's latest revision, empty when there is none of that login.
func currentUser(ctx context.Context, st *store.Store, login string) config.User {
	_, doc, _ := st.Current(ctx, "mcx.test")
	for _, candidate := range doc.Users {
		if candidate.Login == login {
			return candidate
		}
	}
	return config.User{}
}

// ------------------------------------------------------------------------------
// currentGroup is a group of the store's latest revision, nil when there is none of that ID.
func currentGroup(ctx context.Context, st *store.Store, id string) map[string]any {
	_, doc, _ := st.Current(ctx, "mcx.test")
	for _, candidate := range doc.Groups {
		if candidate["id"] == id {
			return candidate
		}
	}
	return nil
}

// ------------------------------------------------------------------------------
// TestGroupsAndTemplates: only an administrator creates a group, and only of this domain; group members and users'
// group lists follow each other; a template in use cannot be deleted, an unused one can.
func TestGroupsAndTemplates(test *testing.T) {
	srv, st, _ := setup(test)
	ctx := context.Background()
	user := func(login string) config.User { return currentUser(ctx, st, login) }
	group := func(id string) map[string]any { return currentGroup(ctx, st, id) }
	night := url.PathEscape("sip:night@mcx.test")
	body := []byte(`{"id":"sip:night@mcx.test","members":["sip:bob.mcptt@mcx.test"]}`)
	if got := call(test, srv, "PUT", "/v1/groups/"+night, "bob-token", "1", "application/json", body); got.status != 403 {
		test.Fatalf("a non-administrator: %d", got.status)
	}
	if got := call(test, srv, "PUT", "/v1/groups/"+url.PathEscape("sip:night@other.test"), "admin-token", "1", "application/json",
		[]byte(`{"id":"sip:night@other.test"}`)); got.status != 400 {
		test.Fatalf("another domain: %d", got.status)
	}
	got := call(test, srv, "PUT", "/v1/groups/"+night, "admin-token", "1", "application/json", body)
	if got.status != 200 || !slices.Contains(user("bob").Groups, "sip:night@mcx.test") {
		test.Fatalf("create: %d %v; bob's groups %v", got.status, got.body, user("bob").Groups)
	}
	// Adding the group to a user's list makes the user a member.
	admin := user("admin")
	admin.Groups = append(admin.Groups, "sip:night@mcx.test")
	raw, _ := json.Marshal(admin)
	if got := call(test, srv, "PUT", "/v1/users/admin", "admin-token", got.etag, "application/json", raw); got.status != 200 ||
		!slices.Contains(config.Members(group("sip:night@mcx.test")), "sip:admin.mcptt@mcx.test") {
		test.Fatalf("user edit: %d; members %v", got.status, group("sip:night@mcx.test"))
	}
	list := call(test, srv, "GET", "/v1/groups", "admin-token", "", "", nil)
	if list.status != 200 || len(list.body["groups"].([]any)) != 2 {
		test.Fatalf("list %v", list.body)
	}
	if got := call(test, srv, "DELETE", "/v1/groups/"+night, "admin-token", list.etag, "", nil); got.status != 200 ||
		slices.Contains(user("bob").Groups, "sip:night@mcx.test") || slices.Contains(user("admin").Groups, "sip:night@mcx.test") {
		test.Fatalf("delete: %d; bob %v admin %v", got.status, user("bob").Groups, user("admin").Groups)
	}
	// Templates: one in use cannot be deleted; a new one can.
	tl := call(test, srv, "GET", "/v1/templates", "admin-token", "", "", nil)
	if tl.status != 200 || len(tl.body["templates"].([]any)) != 1 {
		test.Fatalf("templates %v", tl.body)
	}
	if got := call(test, srv, "DELETE", "/v1/templates/field-user", "admin-token", tl.etag, "", nil); got.status != 409 {
		test.Fatalf("delete a template in use: %d", got.status)
	}
	got = call(test, srv, "PUT", "/v1/templates/dispatcher", "admin-token", tl.etag, "application/json", []byte(`{"private_call":{"allow":true}}`))
	if got.status != 200 {
		test.Fatalf("new template: %d %v", got.status, got.body)
	}
	if got := call(test, srv, "DELETE", "/v1/templates/dispatcher", "admin-token", got.etag, "", nil); got.status != 200 {
		test.Fatalf("delete an unused template: %d", got.status)
	}
}

// ------------------------------------------------------------------------------
// TestAuditLatest: ?latest=1 answers the newest audit entry, the last commit.
func TestAuditLatest(test *testing.T) {
	srv, _, _ := setup(test)
	call(test, srv, "POST", "/v1/users/bob/suspend", "admin-token", "1", "", nil)
	got := call(test, srv, "GET", "/v1/audit?latest=1", "admin-token", "", "", nil)
	list, _ := got.body["audit"].([]any)
	if got.status != 200 || len(list) != 1 || list[0].(map[string]any)["action"] != "commit" ||
		!strings.Contains(list[0].(map[string]any)["detail"].(string), "suspended") {
		test.Fatalf("latest audit %d %v", got.status, got.body)
	}
}

// ------------------------------------------------------------------------------
// TestLoad measures the enterprise load profile of plan slice H (MCXADMIN_LOAD=1):
// a 10,000-user upload checked and applied, search latency over the result, and the
// time for a suspension to be committed with its outbox rows.
func TestLoad(test *testing.T) {
	if os.Getenv("MCXADMIN_LOAD") == "" {
		test.Skip("MCXADMIN_LOAD not set")
	}
	srv, st, _ := setup(test)
	const users = 10000
	var builder strings.Builder
	builder.WriteString("login,display_name,template,groups,services\n")
	for index := 0; index < users; index++ {
		fmt.Fprintf(&builder, "user%05d,User %d,field-user,ops,mcptt;mcdata\n", index, index)
	}
	csv := []byte(builder.String())
	start := time.Now()
	got := call(test, srv, "POST", "/v1/uploads?mode=validate", "admin-token", "", "text/csv", csv)
	validated := time.Since(start)
	if got.status != 200 {
		test.Fatalf("validate %d", got.status)
	}
	start = time.Now()
	got = call(test, srv, "POST", "/v1/uploads?mode=apply", "admin-token", "1", "text/csv", csv)
	applied := time.Since(start)
	if got.status != 200 {
		test.Fatalf("apply %d %v", got.status, got.body["error"])
	}
	var lat []time.Duration
	for index := 0; index < 50; index++ {
		search := fmt.Sprintf("user%05d", (index*197)%users)
		searchStart := time.Now()
		if response := call(test, srv, "GET", "/v1/users?q="+search, "admin-token", "", "", nil); response.status != 200 || response.body["total"] != float64(1) {
			test.Fatalf("search %s: %d %v", search, response.status, response.body["total"])
		}
		lat = append(lat, time.Since(searchStart))
	}
	slices.Sort(lat)
	rev, _, _ := st.Current(context.Background(), "mcx.test")
	start = time.Now()
	if response := call(test, srv, "POST", "/v1/users/user00042/suspend", "admin-token", fmt.Sprint(rev), "", nil); response.status != 200 {
		test.Fatalf("suspend %d", response.status)
	}
	suspended := time.Since(start)
	test.Logf("LOAD %d users: validate %v, apply %v (%d revisions), search p50 %v p95 %v, suspension committed in %v",
		users, validated.Round(time.Millisecond), applied.Round(time.Millisecond), len(got.body["revisions"].([]any)),
		lat[len(lat)/2].Round(time.Millisecond), lat[len(lat)*95/100].Round(time.Millisecond), suspended.Round(time.Millisecond))
	if applied > 5*time.Minute || lat[len(lat)*95/100] > 2*time.Second || suspended > 10*time.Second {
		test.Fatal("outside the provisional limits: apply 5 min, search p95 2 s, suspension 10 s")
	}
}

// ------------------------------------------------------------------------------
// TestRevokeDevices: only an administrator revokes devices; the Credential Service and IdMS are both asked and the
// audit records both; with IdMS failing the answer is 502.
func TestRevokeDevices(test *testing.T) {
	_, st, _ := setup(test)
	var asked []string
	credentials := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if username, pw, ok := request.BasicAuth(); !ok || username != "mcx-server" || pw != "server-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		asked = append(asked, request.Method+" "+request.URL.Path)
		_, _ = writer.Write([]byte(`{"certificates_revoked":1}`))
	}))
	defer credentials.Close()
	idms := fakeIdMS{"admin-token": "sip:admin.mcptt@mcx.test", "bob-token": "sip:bob.mcptt@mcx.test"}
	sessions := &fakeSessions{}
	srv := httptest.NewServer((&Server{Store: st, IdMS: idms, Links: idms, Domain: "mcx.test", ServerClientID: "mcx-server", ServerSecret: "server-secret",
		Credentials: &CredentialService{URL: credentials.URL, ClientID: "mcx-server", Secret: "server-secret"}, Sessions: sessions}).Handler())
	defer srv.Close()
	if got := call(test, srv, "POST", "/v1/users/bob/revoke-devices", "bob-token", "", "", nil); got.status != 403 {
		test.Fatalf("a non-administrator: %d", got.status)
	}
	if got := call(test, srv, "POST", "/v1/users/bob/revoke-devices", "admin-token", "", "", nil); got.status != 200 ||
		strings.Join(asked, " ") != "POST /internal/client-certificates/bob/revoke" || strings.Join(sessions.revoked, " ") != "bob" {
		test.Fatalf("revoke: %d %v %v", got.status, asked, sessions.revoked)
	}
	trail, _ := st.RecentAudit(context.Background(), 1)
	if len(trail) != 1 || trail[0].Action != "devices-revoked" || trail[0].Target != "bob" || !strings.Contains(trail[0].Detail, `"idms_sessions_revoked":2`) {
		test.Fatalf("audit %+v", trail)
	}
	// IdMS unreachable: the credentials are revoked, and the administrator is told the rest failed.
	sessions.fail = true
	if got := call(test, srv, "POST", "/v1/users/bob/revoke-devices", "admin-token", "", "", nil); got.status != 502 {
		test.Fatalf("IdMS failing: %d", got.status)
	}
}

type fakeSessions struct {
	revoked []string
	fail    bool
}

// ------------------------------------------------------------------------------
// RevokeSessions records the login and answers 2 sessions revoked, or fails when told to.
func (fake *fakeSessions) RevokeSessions(_ context.Context, login string) (int, error) {
	if fake.fail {
		return 0, errors.New("unreachable")
	}
	fake.revoked = append(fake.revoked, login)
	return 2, nil
}

// ------------------------------------------------------------------------------
// TestPresence: a sign-in seen through the account lookup and a registration in the SIP core's ul.dump show in the
// presence answer; a user with neither shows unregistered and without a sign-in.
func TestPresence(test *testing.T) {
	_, st, _ := setup(test)
	core := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if !strings.Contains(string(body), `"ul.dump"`) {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","result":{"Domains":[{"Domain":{"Domain":"location","AoRs":[
			{"Info":{"AoR":"bob@mcx.test","Contacts":[{"Contact":{"Address":"sip:bob@10.0.0.2","Expires":3400,"User-Agent":"openMCX Android"}}]}}]}}]},"id":1}`))
	}))
	defer core.Close()
	idms := fakeIdMS{"admin-token": "sip:admin.mcptt@mcx.test"}
	server := &Server{Store: st, IdMS: idms, Links: idms, Domain: "mcx.test", ServerClientID: "mcx-server", ServerSecret: "server-secret",
		SIPCore: &SIPCoreRPC{URL: core.URL}}
	srv := httptest.NewServer(server.Handler())
	defer srv.Close()
	// IdMS asks for bob's account when he signs in.
	req, _ := http.NewRequest("GET", srv.URL+"/internal/v1/accounts?login=bob", nil)
	req.SetBasicAuth("mcx-server", "server-secret")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		test.Fatalf("account lookup %v %v", err, resp)
	}
	got := call(test, srv, "GET", "/v1/presence", "admin-token", "", "", nil)
	users, _ := got.body["users"].(map[string]any)
	bob, _ := users["bob"].(map[string]any)
	admin, _ := users["admin"].(map[string]any)
	if got.status != 200 || got.body["registered"] != float64(1) || bob["registered"] != true || bob["user_agent"] != "openMCX Android" ||
		bob["last_sign_in"] == nil || admin["registered"] != false || admin["last_sign_in"] != nil {
		test.Fatalf("presence %d %v", got.status, got.body)
	}
}
