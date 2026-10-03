// Tests of the configuration model: the site.json round trip, validation, the change preview and templates.
//
// openMCX administration (no 3GPP specification).
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// ------------------------------------------------------------------------------
// load imports testdata/site.json: its raw bytes, the document and the import report.
func load(test *testing.T) ([]byte, *Document, ImportReport) {
	test.Helper()
	raw, err := os.ReadFile("testdata/site.json")
	if err != nil {
		test.Fatal(err)
	}
	doc, report, err := ImportSite(raw)
	if err != nil {
		test.Fatal(err)
	}
	return raw, doc, report
}

// ------------------------------------------------------------------------------
// TestImportRoundTrip, golden: the users and groups given back equal those imported (SLICE_A Evidence),
// so admin mode can serve the documents file mode serves.
func TestImportRoundTrip(test *testing.T) {
	raw, doc, report := load(test)
	if problems := doc.Validate(); len(problems) > 0 {
		test.Fatalf("problems %v", problems)
	}
	assertRoundTrip(test, raw, doc)
	if !reflect.DeepEqual(report.SecretsRemoved, []string{"alice.password", "alice.sip_password", "bob.password", "bob.sip_password"}) {
		test.Fatalf("secrets removed %v", report.SecretsRemoved)
	}
	encoded, _ := json.Marshal(doc)
	for _, secret := range []string{"fake-password", "fake-sip", "fake-server-secret"} {
		if strings.Contains(string(encoded), secret) {
			test.Fatalf("the document holds a secret: %s", secret)
		}
	}
	// The document survives its own encoding.
	again, err := Parse(encoded)
	if err != nil || !reflect.DeepEqual(again, doc) {
		test.Fatalf("reparse %v", err)
	}
}

// ------------------------------------------------------------------------------
// assertRoundTrip checks that the document gives back the users and groups of the site.json it was imported from.
func assertRoundTrip(test *testing.T, raw []byte, doc *Document) {
	test.Helper()
	users, groups, err := doc.ToSiteUsers(raw)
	if err != nil {
		test.Fatal(err)
	}
	var site struct {
		Users  []map[string]any `json:"users"`
		Groups []map[string]any `json:"groups"`
	}
	if err := json.Unmarshal(raw, &site); err != nil {
		test.Fatal(err)
	}
	if !equalJSON(users, site.Users) {
		got, _ := json.Marshal(users)
		want, _ := json.Marshal(site.Users)
		test.Fatalf("users differ:\n%s\n%s", got, want)
	}
	if !equalJSON(groups, site.Groups) {
		test.Fatalf("groups differ")
	}
}

// ------------------------------------------------------------------------------
// TestImportRoundTripLabs: the golden check against real lab files, when MCXADMIN_SITES names a directory
// tree holding site.json files (for example a regression's artifacts).
func TestImportRoundTripLabs(test *testing.T) {
	root := os.Getenv("MCXADMIN_SITES")
	if root == "" {
		test.Skip("MCXADMIN_SITES not set")
	}
	count := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Name() != "site.json" {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		doc, _, err := ImportSite(raw)
		if err != nil {
			test.Errorf("%s: %v", path, err)
			return nil
		}
		if problems := doc.Validate(); len(problems) > 0 {
			test.Errorf("%s: %v", path, problems)
		}
		test.Run(path, func(test *testing.T) { assertRoundTrip(test, raw, doc) })
		count++
		return nil
	})
	if err != nil {
		test.Fatal(err)
	}
	test.Logf("%d site.json files", count)
}

// ------------------------------------------------------------------------------
// TestImportTemplates: a site.json "templates" object becomes the document's templates, is not reported as a
// site-level key outside the model, validates, and a template that is not an object is refused.
func TestImportTemplates(test *testing.T) {
	raw := []byte(`{"domain":"mcx.test","users":[],"groups":[],
		"templates":{"phone-user":{"private_call":{"allow":true,"callable":true,"incoming_any":true},"mcdata":{"incoming_any":true}}}}`)
	doc, report, err := ImportSite(raw)
	if err != nil {
		test.Fatal(err)
	}
	call, _ := doc.Templates["phone-user"]["private_call"].(map[string]any)
	if call["incoming_any"] != true || len(report.SystemNotImported) != 0 {
		test.Fatalf("templates %v, not imported %v", doc.Templates, report.SystemNotImported)
	}
	if problems := doc.Validate(); len(problems) > 0 {
		test.Fatalf("problems %v", problems)
	}
	if _, _, err := ImportSite([]byte(`{"domain":"mcx.test","templates":{"x":1}}`)); err == nil {
		test.Fatal("a template that is not an object was accepted")
	}
}

// ------------------------------------------------------------------------------
// TestValidate: a duplicate MCPTT ID, an unknown status, template or group, a secret in a profile and an unknown
// member (MCPTT or MCData) are each reported against their record and field; a PEM value is a secret whatever its field.
func TestValidate(test *testing.T) {
	_, doc, _ := load(test)
	bad := clone(*doc)
	bad.Users[1].MCPTTID = bad.Users[0].MCPTTID
	bad.Users[1].Status = "enabled"
	bad.Users[1].Template = "none"
	bad.Users[0].Groups = append(bad.Users[0].Groups, "sip:missing@mcx.test")
	bad.Users[0].Profile["sip_password"] = "x"
	bad.Groups[0]["members"] = []any{"sip:ghost.mcptt@mcx.test"}
	bad.Groups[0]["mcdata"] = map[string]any{"members": []any{"sip:ghost.mcdata@mcx.test"}}
	got := map[string]bool{}
	for _, problem := range bad.Validate() {
		got[problem.Record+" "+problem.Field] = true
	}
	for _, want := range []string{"user bob mcptt_id", "user bob status", "user bob template", "user alice groups",
		"user alice profile.sip_password", "group sip:operations@mcx.test members",
		"group sip:operations@mcx.test mcdata.members"} {
		if !got[want] {
			test.Errorf("missing problem %q in %v", want, got)
		}
	}
	// A PEM value is a secret, whatever its field is called.
	pem := clone(*doc)
	pem.Users[0].Profile["note"] = "-----BEGIN CERTIFICATE-----"
	if len(pem.Validate()) != 1 {
		test.Fatalf("PEM not refused: %v", pem.Validate())
	}
}

// ------------------------------------------------------------------------------
// TestDiff: a document does not differ from itself; removed, added and changed users and a changed group membership
// are listed in order, lists as the entries added and removed.
func TestDiff(test *testing.T) {
	_, doc, _ := load(test)
	if changes := Diff(doc, doc); len(changes) != 0 {
		test.Fatalf("a document differs from itself: %v", changes)
	}
	after := clone(*doc)
	after.Users[0].Status = "suspended"
	after.Users[0].Profile["private_call"].(map[string]any)["allow"] = false
	after.Users[0].Profile["private_call"].(map[string]any)["targets"] = []any{"sip:carol.mcptt@mcx.test"}
	after.Users = after.Users[:1]
	after.Users = append(after.Users, User{Login: "carol", Status: "pending"})
	after.Groups[0]["members"] = []any{"sip:alice.mcptt@mcx.test", "sip:carol.mcptt@mcx.test"}
	var lines []string
	for _, change := range Diff(doc, &after) {
		lines = append(lines, change.String())
	}
	want := []string{
		`user-removed bob`,
		`user-changed alice profile.private_call.allow: true -> (none)`,
		`user-changed alice profile.private_call.targets: ["sip:bob.mcptt@mcx.test"] -> ["sip:carol.mcptt@mcx.test"]`,
		`user-changed alice status: "active" -> "suspended"`,
		`user-added carol`,
		`group-changed sip:operations@mcx.test members: ["sip:bob.mcptt@mcx.test"] -> ["sip:carol.mcptt@mcx.test"]`,
	}
	if !reflect.DeepEqual(lines, want) {
		test.Fatalf("diff\n got %q\nwant %q", lines, want)
	}
}

// ------------------------------------------------------------------------------
// TestTemplates: a template change shows on every user it affects.
func TestTemplates(test *testing.T) {
	_, doc, _ := load(test)
	doc.Templates = map[string]map[string]any{"field": {"adhoc_call": map[string]any{"allow": true, "emergency": true}}}
	doc.Users[1].Template = "field"
	if profile := doc.EffectiveProfile(doc.Users[1]); profile["adhoc_call"].(map[string]any)["emergency"] != true {
		test.Fatalf("template not applied: %v", profile)
	}
	after := clone(*doc)
	after.Templates["field"]["adhoc_call"].(map[string]any)["emergency"] = false
	changes := Diff(doc, &after)
	// On bob's effective profile, and on the template itself.
	if len(changes) != 2 || changes[0].Record != "bob" || changes[0].Field != "profile.adhoc_call.emergency" ||
		changes[1].Kind != "template-changed" || changes[1].Record != "field" {
		test.Fatalf("template change %v", changes)
	}
}
