// Tests of the GMS projection: writes per revision, retry after a refusal, and the import of XCAP changes.
//
// Specifications: TS 24.481 V19.3.0 (Rel-19).
package project

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/openMCX/openmcx-admin/internal/config"
	"github.com/openMCX/openmcx-admin/internal/store/storetest"
)

// ------------------------------------------------------------------------------
// TestGMS: the first run writes every group, a later one only the changed groups; a refused write fails the
// revision, which is retried; a user's XCAP change at the GMS becomes a revision of source xcap that users' group
// lists follow, and is not imported twice.
func TestGMS(test *testing.T) {
	st, _ := storetest.Open(test)
	ctx := context.Background()
	st.Consumers = []string{"gms"}
	fakeGMS := &fakeGMS{groups: map[string]*fakeGroup{}}
	fake := httptest.NewServer(fakeGMS)
	defer fake.Close()
	raw, _ := os.ReadFile("../config/testdata/site.json")
	doc, _, _ := config.ImportSite(raw)
	rev, _, err := st.Commit(ctx, 0, doc, "import", "seed", "import")
	if err != nil {
		test.Fatal(err)
	}
	projector := &GMS{Store: st, Domain: "mcx.test", URL: fake.URL, ClientID: "mcx-server", Secret: "server-secret"}
	if err := projector.Once(ctx); err != nil {
		test.Fatal(err)
	}
	if strings.Join(fakeGMS.calls, ", ") != "PUT sip:operations@mcx.test" {
		test.Fatalf("first run %v", fakeGMS.calls)
	}
	// A new group and a changed user: only the new group is written.
	fakeGMS.calls = nil
	doc.Groups = append(doc.Groups, map[string]any{"id": "sip:night@mcx.test", "members": []any{"sip:bob.mcptt@mcx.test"},
		"protect_media": false, "protect_floor": false})
	doc.Users[0].DisplayName = "Alice"
	if rev, _, err = st.Commit(ctx, rev, doc, "admin", "night shift", "api"); err != nil {
		test.Fatal(err)
	}
	if err := projector.Once(ctx); err != nil || strings.Join(fakeGMS.calls, ", ") != "PUT sip:night@mcx.test" {
		test.Fatalf("second run %v %v", fakeGMS.calls, err)
	}
	// The GMS refuses: the revision is failed and retried; nothing is lost.
	fakeGMS.calls = nil
	doc.Groups = doc.Groups[:1]
	if rev, _, err = st.Commit(ctx, rev, doc, "admin", "no night shift", "api"); err != nil {
		test.Fatal(err)
	}
	fakeGMS.broken = true
	if err := projector.Once(ctx); err == nil {
		test.Fatal("a refused write was not reported")
	}
	fakeGMS.broken = false
	if err := projector.Once(ctx); err != nil || strings.Join(fakeGMS.calls, ", ") != "DELETE sip:night@mcx.test" {
		test.Fatalf("retry %v %v", fakeGMS.calls, err)
	}
	if pending, _ := st.Pending(ctx, "gms"); pending != 0 {
		test.Fatalf("pending %d", pending)
	}
	if last, _ := st.LastApplied(ctx, "gms"); last != rev {
		test.Fatalf("applied %d, want %d", last, rev)
	}

	checkXCAPImport(test, projector, fakeGMS, rev)
}

// ------------------------------------------------------------------------------
// checkXCAPImport, the second half of TestGMS: with nothing changed at the GMS there is no import; a user's XCAP
// change becomes one revision of source xcap that users' group lists follow, goes back to the GMS unchanged and is
// not imported again.
func checkXCAPImport(test *testing.T, projector *GMS, fakeGMS *fakeGMS, rev int64) {
	test.Helper()
	ctx, st := context.Background(), projector.Store
	// Nothing changed at the GMS: no import.
	if err := projector.Import(ctx); err != nil {
		test.Fatal(err)
	}
	if now, _, _ := st.Current(ctx, "mcx.test"); now != rev {
		test.Fatalf("an import without a change: revision %d", now)
	}
	// A user's XCAP write removes bob from operations and creates a group of hers.
	ops := fakeGMS.groups["sip:operations@mcx.test"]
	ops.group = map[string]any{"id": "sip:operations@mcx.test", "members": []any{"sip:alice.mcptt@mcx.test"},
		"protect_media": false, "protect_floor": false}
	ops.version++
	fakeGMS.groups["sip:alice-team@mcx.test"] = &fakeGroup{version: 1, group: map[string]any{"id": "sip:alice-team@mcx.test",
		"members": []any{"sip:alice.mcptt@mcx.test", "sip:bob.mcptt@mcx.test"}, "protect_media": false, "protect_floor": false}}
	if err := projector.Import(ctx); err != nil {
		test.Fatal(err)
	}
	now, imported, _ := st.Current(ctx, "mcx.test")
	if now != rev+1 || len(imported.Groups) != 2 {
		test.Fatalf("import: revision %d, groups %v", now, imported.Groups)
	}
	var bob config.User
	for _, user := range imported.Users {
		if user.Login == "bob" {
			bob = user
		}
	}
	if slices.Contains(bob.Groups, "sip:operations@mcx.test") || !slices.Contains(bob.Groups, "sip:alice-team@mcx.test") {
		test.Fatalf("bob's groups do not follow the membership: %v", bob.Groups)
	}
	revisions, _ := st.Revisions(ctx, rev, 10)
	if len(revisions) != 1 || revisions[0].Source != "xcap" {
		test.Fatalf("the imported revision %+v", revisions)
	}
	// The imported revision goes back to the GMS unchanged, and is not imported again.
	fakeGMS.calls = nil
	if err := projector.Once(ctx); err != nil {
		test.Fatal(err)
	}
	if err := projector.Import(ctx); err != nil {
		test.Fatal(err)
	}
	if again, _, _ := st.Current(ctx, "mcx.test"); again != now || ops.version != 2 {
		test.Fatalf("a second import: revision %d, GMS version %d, calls %v", again, ops.version, fakeGMS.calls)
	}
}

// fakeGroup is a group as the fake GMS keeps it.
type fakeGroup struct {
	version int64
	deleted bool
	group   map[string]any
}

// fakeGMS keeps versions like the real GMS: a write that changes a group bumps it; a test simulates a user's XCAP
// change by changing groups directly. With broken set it refuses every request; calls lists its writes.
type fakeGMS struct {
	groups map[string]*fakeGroup
	calls  []string
	broken bool
}

// ------------------------------------------------------------------------------
// ServeHTTP answers the listing (GET), a group write (PUT) and a deletion (DELETE), with the server client
// credentials only.
func (fake *fakeGMS) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if username, pw, ok := request.BasicAuth(); !ok || username != "mcx-server" || pw != "server-secret" {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	if fake.broken {
		writer.WriteHeader(http.StatusConflict)
		return
	}
	id := request.URL.Query().Get("id")
	var answer any
	switch request.Method {
	case http.MethodGet:
		list := []map[string]any{}
		for gid, record := range fake.groups {
			list = append(list, map[string]any{"id": gid, "version": record.version, "deleted": record.deleted, "group": record.group})
		}
		answer = map[string]any{"groups": list}
	case http.MethodPut:
		var group map[string]any
		if err := json.NewDecoder(request.Body).Decode(&group); err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		record := fake.groups[id]
		if record == nil {
			record = &fakeGroup{}
			fake.groups[id] = record
		}
		if record.deleted || string(mustJSON(record.group)) != string(mustJSON(group)) {
			record.version++
		}
		record.group, record.deleted = group, false
		answer = map[string]any{"version": record.version}
	case http.MethodDelete:
		record := fake.groups[id]
		record.version++
		record.deleted, record.group = true, nil
		answer = map[string]any{"version": record.version}
	}
	if request.Method != http.MethodGet {
		fake.calls = append(fake.calls, request.Method+" "+id)
	}
	if err := json.NewEncoder(writer).Encode(answer); err != nil {
		panic(err) // a test server writing to its recorder
	}
}

// ------------------------------------------------------------------------------
// mustJSON is a value as JSON.
func mustJSON(value any) []byte {
	raw, _ := json.Marshal(value)
	return raw
}

// ------------------------------------------------------------------------------
// TestGMSFirstRun, an existing deployment: the GMS already holds a user's change to a group when
// MCX Admin, seeded from site.json, first writes to it; the change is taken in, not
// overwritten. A consumer added to a store with revisions starts from the latest.
func TestGMSFirstRun(test *testing.T) {
	st, _ := storetest.Open(test)
	ctx := context.Background()
	st.Consumers = []string{"userdb"}
	raw, _ := os.ReadFile("../config/testdata/site.json")
	doc, _, _ := config.ImportSite(raw)
	if _, _, err := st.Commit(ctx, 0, doc, "import", "seed", "import"); err != nil {
		test.Fatal(err)
	}
	// Upgrade: gms is added; it has no revision yet.
	st.Consumers = []string{"userdb", "gms"}
	if err := st.EnsureConsumers(ctx); err != nil {
		test.Fatal(err)
	}
	if pending, _ := st.Pending(ctx, "gms"); pending != 1 {
		test.Fatalf("gms pending %d", pending)
	}
	edited := map[string]any{"id": "sip:operations@mcx.test", "members": []any{"sip:alice.mcptt@mcx.test"}, "protect_media": false, "protect_floor": false}
	var puts []string
	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			_ = json.NewEncoder(writer).Encode(map[string]any{"groups": []map[string]any{{"id": "sip:operations@mcx.test", "version": 4, "group": edited}}})
		case http.MethodPut:
			body, _ := io.ReadAll(request.Body)
			puts = append(puts, string(body))
			_ = json.NewEncoder(writer).Encode(map[string]any{"version": 4})
		}
	}))
	defer fake.Close()
	projector := &GMS{Store: st, Domain: "mcx.test", URL: fake.URL, ClientID: "mcx-server", Secret: "server-secret"}
	if err := projector.Once(ctx); err != nil {
		test.Fatal(err)
	}
	_, now, _ := st.Current(ctx, "mcx.test")
	if members := config.Members(now.Groups[0]); len(members) != 1 || len(puts) != 1 || !strings.Contains(puts[0], `"members":["sip:alice.mcptt@mcx.test"]`) {
		test.Fatalf("the GMS's change was not kept: members %v, writes %v", members, puts)
	}
	if pending, _ := st.Pending(ctx, "gms"); pending != 0 {
		test.Fatalf("pending %d", pending)
	}
}
