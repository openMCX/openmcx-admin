// Tests of the user database and Credential Service projections.
//
// openMCX administration (no 3GPP specification).
package project

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/go-sql-driver/mysql"

	"github.com/openMCX/openmcx-admin/internal/config"
	"github.com/openMCX/openmcx-admin/internal/store/storetest"
)

// ------------------------------------------------------------------------------
// TestUserDB: the users file holds the active users, without a password, at the latest revision; a suspended user
// leaves it; a failed write is recorded in the outbox.
func TestUserDB(test *testing.T) {
	st, _ := storetest.Open(test)
	ctx := context.Background()
	raw, _ := os.ReadFile("../config/testdata/site.json")
	doc, _, err := config.ImportSite(raw)
	if err != nil {
		test.Fatal(err)
	}
	rev, _, err := st.Commit(ctx, 0, doc, "import", "seed", "import")
	if err != nil {
		test.Fatal(err)
	}
	path := filepath.Join(test.TempDir(), "userdb.json")
	userDB := &UserDB{Store: st, Domain: "mcx.test", Path: path}
	read := func() (int64, []map[string]any) {
		var out struct {
			Revision int64            `json:"revision"`
			Users    []map[string]any `json:"users"`
		}
		data, err := os.ReadFile(path)
		if err != nil {
			test.Fatal(err)
		}
		if strings.Contains(string(data), "password") {
			test.Fatal("the users file holds a password")
		}
		_ = json.Unmarshal(data, &out)
		return out.Revision, out.Users
	}
	if err := userDB.Once(ctx); err != nil {
		test.Fatal(err)
	}
	if revision, users := read(); revision != rev || len(users) != 2 || users[0]["mcptt_id"] != "sip:alice.mcptt@mcx.test" {
		test.Fatalf("revision %d users %v", revision, users)
	}
	if pending, _ := st.Pending(ctx, "userdb"); pending != 0 {
		test.Fatalf("still pending %d", pending)
	}
	// A suspended user leaves the file with the next revision.
	doc.Users[1].Status = "suspended"
	rev, _, err = st.Commit(ctx, rev, doc, "admin", "suspend bob", "api")
	if err != nil {
		test.Fatal(err)
	}
	if err := userDB.Once(ctx); err != nil {
		test.Fatal(err)
	}
	if revision, users := read(); revision != rev || len(users) != 1 || users[0]["username"] != "alice" {
		test.Fatalf("after suspension: revision %d users %v", revision, users)
	}
	// A write that fails is recorded and retried.
	userDB.Path = filepath.Join(test.TempDir(), "missing", "userdb.json")
	doc.Users[1].Status = "active"
	if _, _, err = st.Commit(ctx, rev, doc, "admin", "restore bob", "api"); err != nil {
		test.Fatal(err)
	}
	if err := userDB.Once(ctx); err == nil {
		test.Fatal("a write to a missing directory succeeded")
	}
	health, _ := st.Outbox(ctx)
	if health["userdb"]["failed"] != 1 {
		test.Fatalf("outbox %v", health)
	}
}

// ------------------------------------------------------------------------------
// TestSIP: the sip consumer tells the Credential Service each account's status change once.
func TestSIP(test *testing.T) {
	st, _ := storetest.Open(test)
	ctx := context.Background()
	st.Consumers = []string{"sip"}
	fake, calls := fakeCredentialService(test)
	raw, _ := os.ReadFile("../config/testdata/site.json")
	doc, _, _ := config.ImportSite(raw)
	rev, _, err := st.Commit(ctx, 0, doc, "import", "seed", "import")
	if err != nil {
		test.Fatal(err)
	}
	sip := &SIP{Store: st, Domain: "mcx.test", URL: fake.URL, ClientID: "mcx-server", Secret: "server-secret"}
	if err := sip.Once(ctx); err != nil {
		test.Fatal(err)
	}
	if strings.Join(*calls, " ") != "alice/resume bob/resume" {
		test.Fatalf("first run %v", calls)
	}
	*calls = nil
	doc.Users[1].Status = "suspended"
	if rev, _, err = st.Commit(ctx, rev, doc, "admin", "suspend bob", "api"); err != nil {
		test.Fatal(err)
	}
	doc.Users[0].DisplayName = "Alice"
	if _, _, err = st.Commit(ctx, rev, doc, "admin", "rename alice", "api"); err != nil {
		test.Fatal(err)
	}
	if err := sip.Once(ctx); err != nil {
		test.Fatal(err)
	}
	if strings.Join(*calls, " ") != "bob/suspend" {
		test.Fatalf("after suspension %v", calls)
	}
	if pending, _ := st.Pending(ctx, "sip"); pending != 0 {
		test.Fatalf("pending %d", pending)
	}
	// A user removed from the document (as by a rollback) has its credential retired.
	*calls = nil
	latest, current, _ := st.Current(ctx, "mcx.test")
	removeUser(current, 1) // as a rollback past bob's creation would
	if _, _, err = st.Commit(ctx, latest, current, "admin", "rollback", "api"); err != nil {
		test.Fatal(err)
	}
	if err := sip.Once(ctx); err != nil {
		test.Fatal(err)
	}
	if strings.Join(*calls, " ") != "bob/retire" {
		test.Fatalf("after removal %v", calls)
	}
}

// ------------------------------------------------------------------------------
// removeUser takes a document's user at index off the document and off every group's MCPTT, MCVideo and MCData
// members.
func removeUser(doc *config.Document, index int) {
	removed := doc.Users[index]
	doc.Users = append(doc.Users[:index:index], doc.Users[index+1:]...)
	for _, group := range doc.Groups {
		for _, holder := range []map[string]any{group, asMap(group["mcvideo"]), asMap(group["mcdata"])} {
			if holder == nil {
				continue
			}
			var kept []any
			for _, member := range config.Members(holder) {
				if member != removed.MCPTTID && member != removed.MCVideoID && member != removed.MCDataID {
					kept = append(kept, member)
				}
			}
			holder["members"] = kept
		}
	}
}

// ------------------------------------------------------------------------------
// asMap is a group's service section as a map, nil when the group has none.
func asMap(value any) map[string]any {
	section, _ := value.(map[string]any)
	return section
}

// ------------------------------------------------------------------------------
// fakeCredentialService answers the Credential Service's SIP credential operations for the server client
// credentials and lists them ("bob/suspend") in calls.
func fakeCredentialService(test *testing.T) (*httptest.Server, *[]string) {
	test.Helper()
	calls := &[]string{}
	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if username, pw, ok := request.BasicAuth(); !ok || username != "mcx-server" || pw != "server-secret" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		*calls = append(*calls, strings.TrimPrefix(request.URL.Path, "/internal/sip-credentials/"))
	}))
	test.Cleanup(fake.Close)
	return fake, calls
}
