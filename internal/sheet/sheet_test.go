// Tests of the bulk upload: rows applied, problems per row, the header, XLSX against CSV and 10,000 users.
//
// openMCX administration (no 3GPP specification).
package sheet

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/openMCX/openmcx-admin/internal/config"
)

// ------------------------------------------------------------------------------
// base is the document the tests upload to: two templates and the operations group.
func base(test *testing.T) *config.Document {
	test.Helper()
	doc, err := config.Parse([]byte(`{"schema_version":1,"system":{"domain":"mcx.test"},
		"templates":{"field-user":{"private_call":{"allow":true}},"dispatcher":{"regroup":true}},
		"users":[],"groups":[{"id":"sip:operations@mcx.test","members":[],"protect_media":false,"protect_floor":false}]}`))
	if err != nil {
		test.Fatal(err)
	}
	return doc
}

// ------------------------------------------------------------------------------
// csvRows reads CSV text into rows against the columns.
func csvRows(test *testing.T, text string, columns []Column) []Row {
	test.Helper()
	rows, err := ReadCSV(strings.NewReader(text), columns)
	if err != nil {
		test.Fatal(err)
	}
	return rows
}

const usersCSV = "login*,display_name,email,template*,groups,services,status\n" +
	"alice,Alice A,alice@example.org,field-user,operations,mcptt;mcvideo,active\n" +
	"bob,Bob B,,dispatcher,operations;sip:ops2@mcx.test,,\n"
const groupsCSV = "id*,services,members\n" +
	"operations,mcptt;mcvideo,alice;bob\n" +
	"ops2,,bob\n"

// ------------------------------------------------------------------------------
// TestApply: user and group rows make the users with their generated identities, services, status and groups, and
// the groups with their members; the base is not changed and the same upload again changes nothing.
func TestApply(test *testing.T) {
	doc := base(test)
	res := Apply(doc, csvRows(test, usersCSV, UserColumns), csvRows(test, groupsCSV, GroupColumns))
	if len(res.Problems) > 0 {
		test.Fatalf("problems %v", res.Problems)
	}
	if problems := res.Document.Validate(); len(problems) > 0 {
		test.Fatalf("document problems %v", problems)
	}
	alice := res.Document.Users[0]
	if alice.MCPTTID != "sip:alice.mcptt@mcx.test" || alice.MCVideoID != "sip:alice.mcvideo@mcx.test" || alice.MCDataID != "" ||
		alice.Status != "active" || alice.Template != "field-user" || len(alice.ClientIDs) != 1 {
		test.Fatalf("alice %+v", alice)
	}
	if bob := res.Document.Users[1]; bob.Status != "pending" || len(bob.Groups) != 2 || bob.MCVideoID != "" {
		test.Fatalf("bob %+v", bob)
	}
	ops := res.Document.Groups[0]
	if fmt.Sprint(ops["members"]) != "[sip:alice.mcptt@mcx.test sip:bob.mcptt@mcx.test]" ||
		fmt.Sprint(ops["mcvideo"]) != "map[members:[sip:alice.mcvideo@mcx.test]]" {
		test.Fatalf("operations %v", ops)
	}
	if len(res.Document.Groups) != 2 {
		test.Fatalf("ops2 not created: %v", res.Document.Groups)
	}
	// The base is not touched, and the same upload again changes nothing.
	if len(doc.Users) != 0 {
		test.Fatal("base changed")
	}
	again := Apply(res.Document, csvRows(test, usersCSV, UserColumns), csvRows(test, groupsCSV, GroupColumns))
	if changes := config.Diff(res.Document, again.Document); len(changes) != 0 {
		test.Fatalf("a rerun changed %v", changes)
	}
}

// ------------------------------------------------------------------------------
// TestServicesColumnAbsent: a sheet without a services column leaves users' and groups' services as they are; a
// services column with an empty cell means mcptt only; a short record (a workbook row ending early) still has the
// column.
func TestServicesColumnAbsent(test *testing.T) {
	first := Apply(base(test), csvRows(test, usersCSV, UserColumns), csvRows(test, groupsCSV, GroupColumns))
	if len(first.Problems) > 0 {
		test.Fatalf("problems %v", first.Problems)
	}
	// Members only, no services column: alice keeps MCVideo, the group keeps its MCVideo section.
	again := Apply(first.Document, csvRows(test, "login*,template*\nalice,field-user\n", UserColumns),
		csvRows(test, "id*,members\noperations,alice\n", GroupColumns))
	alice := again.Document.Users[0]
	if len(again.Problems) > 0 || alice.MCVideoID != "sip:alice.mcvideo@mcx.test" {
		test.Fatalf("alice lost MCVideo without a services column: %v %+v", again.Problems, alice)
	}
	if _, ok := again.Document.Groups[0]["mcvideo"]; !ok {
		test.Fatalf("the group lost its mcvideo section without a services column: %v", again.Document.Groups[0])
	}
	// The services column present but the record short: mcptt only.
	cleared := Apply(first.Document, csvRows(test, "login*,template*,services\nalice,field-user\n", UserColumns),
		csvRows(test, "id*,members,services\noperations,alice\n", GroupColumns))
	if cleared.Document.Users[0].MCVideoID != "" {
		test.Fatalf("an empty services cell kept MCVideo: %+v", cleared.Document.Users[0])
	}
	if _, ok := cleared.Document.Groups[0]["mcvideo"]; ok {
		test.Fatalf("an empty services cell kept the mcvideo section: %v", cleared.Document.Groups[0])
	}
}

// ------------------------------------------------------------------------------
// TestBatchesServiceMembers: each step of a batched upload validates, its MCVideo and MCData group members limited
// to the users that step holds.
func TestBatchesServiceMembers(test *testing.T) {
	users := csvRows(test, "login*,template*,services,status\n"+
		"alice,field-user,mcptt;mcdata;mcvideo,active\n"+
		"bob,field-user,mcptt;mcdata;mcvideo,active\n", UserColumns)
	start := base(test)
	final := Apply(start, users, nil).Document
	var data, video []any
	for _, user := range final.Users {
		data, video = append(data, user.MCDataID), append(video, user.MCVideoID)
	}
	final.Groups[0]["mcdata"] = map[string]any{"members": data}
	final.Groups[0]["mcvideo"] = map[string]any{"members": video}
	steps := Batches(start, final, users, 1)
	if len(steps) != 2 {
		test.Fatalf("%d steps", len(steps))
	}
	for index, step := range steps {
		if problems := step.Validate(); len(problems) > 0 {
			test.Fatalf("step %d does not validate: %v", index, problems)
		}
	}
	first := steps[0].Groups[0]
	if got := first["mcdata"].(map[string]any)["members"]; fmt.Sprint(got) != "[sip:alice.mcdata@mcx.test]" {
		test.Fatalf("first step MCData members %v", got)
	}
}

// ------------------------------------------------------------------------------
// TestProblemsPerRow: each bad row is reported by sheet, line and column, and the report CSV carries the errors.
func TestProblemsPerRow(test *testing.T) {
	users := "login,template,groups,services,status\n" +
		"alice,field-user,operations,,\n" +
		"alice,field-user,,,\n" + // line 3: duplicate login
		"b@d,field-user,,,\n" + // line 4: bad login
		"carol,nobody,,,\n" + // line 5: unknown template
		"dave,field-user,missing,,\n" + // line 6: unknown group
		"erin,field-user,,sms,enabled\n" // line 7: unknown service, bad status
	groups := "id,members\noperations,alice;zed\n" // line 2: unknown member
	res := Apply(base(test), csvRows(test, users, UserColumns), csvRows(test, groups, GroupColumns))
	got := map[string]bool{}
	for _, problem := range res.Problems {
		got[fmt.Sprintf("%s %d %s", problem.Sheet, problem.Line, problem.Column)] = true
	}
	for _, want := range []string{"users 3 login", "users 4 login", "users 5 template", "users 6 groups",
		"users 7 services", "users 7 status", "groups 2 members"} {
		if !got[want] {
			test.Errorf("missing %q in %v", want, res.Problems)
		}
	}
	documentProblem := Problem{Sheet: DocumentSheet, Column: "group sip:x@mcx.test members", Reason: "unknown MCPTT ID"}
	raw, err := ReportCSV(UserColumns, csvRows(test, users, UserColumns), UsersSheet, append(res.Problems, documentProblem))
	report := string(raw)
	if err != nil || !strings.Contains(report, "error") || !strings.Contains(report, "unknown template") ||
		!strings.Contains(report, "document: group sip:x@mcx.test members: unknown MCPTT ID") {
		test.Fatalf("report %v %s", err, report)
	}
	// The report uploads again: its error column is ignored, the document problem rows are blank.
	if rows, err := ReadCSV(strings.NewReader(report), UserColumns); err != nil || len(rows) != 6 {
		test.Fatalf("the report does not upload again: %d rows, %v", len(rows), err)
	}
}

// ------------------------------------------------------------------------------
// TestHeader: an unknown or missing required column is refused; a byte-order mark and the * of the template are
// accepted.
func TestHeader(test *testing.T) {
	if _, err := ReadCSV(strings.NewReader("login,colour\n"), UserColumns); err == nil {
		test.Fatal("an unknown column accepted")
	}
	if _, err := ReadCSV(strings.NewReader("email,template\n"), UserColumns); err == nil {
		test.Fatal("a missing required column accepted")
	}
	// A header saved by Excel with a byte-order mark and the * of the template.
	if rows, err := ReadCSV(strings.NewReader("\ufefflogin*,template*\nalice,field-user\n"), UserColumns); err != nil || len(rows) != 1 {
		test.Fatalf("template header %v %v", rows, err)
	}
}

// ------------------------------------------------------------------------------
// TestXLSX: the downloaded workbook, filled in and uploaded, gives the same result as CSV.
func TestXLSX(test *testing.T) {
	doc := base(test)
	raw, err := TemplateXLSX(doc)
	if err != nil {
		test.Fatal(err)
	}
	book, err := excelize.OpenReader(bytes.NewReader(raw))
	if err != nil {
		test.Fatal(err)
	}
	if help, _ := book.GetRows(HelpSheet); !strings.Contains(fmt.Sprint(help), "field-user") {
		test.Fatalf("help sheet lacks the templates: %v", help)
	}
	for sheet, text := range map[string]string{UsersSheet: usersCSV, GroupsSheet: groupsCSV} {
		for index, line := range strings.Split(strings.TrimSpace(text), "\n")[1:] {
			for column, value := range strings.Split(line, ",") {
				cell, _ := excelize.CoordinatesToCellName(column+1, index+2)
				_ = book.SetCellValue(sheet, cell, value)
			}
		}
	}
	var buf bytes.Buffer
	if err := book.Write(&buf); err != nil {
		test.Fatal(err)
	}
	users, groups, err := ReadXLSX(&buf)
	if err != nil {
		test.Fatal(err)
	}
	fromXLSX := Apply(doc, users, groups)
	fromCSV := Apply(doc, csvRows(test, usersCSV, UserColumns), csvRows(test, groupsCSV, GroupColumns))
	if len(fromXLSX.Problems) > 0 || len(config.Diff(fromCSV.Document, fromXLSX.Document)) != 0 {
		test.Fatalf("XLSX differs from CSV: %v %v", fromXLSX.Problems, config.Diff(fromCSV.Document, fromXLSX.Document))
	}
}

// ------------------------------------------------------------------------------
// TestTenThousand: 10,000 users in one upload are read, applied and validated in reasonable time.
func TestTenThousand(test *testing.T) {
	var builder strings.Builder
	builder.WriteString("login,template,groups,services\n")
	for index := 0; index < 10000; index++ {
		fmt.Fprintf(&builder, "user%05d,field-user,operations,mcptt;mcdata\n", index)
	}
	start := time.Now()
	res := Apply(base(test), csvRows(test, builder.String(), UserColumns), nil)
	problems := res.Document.Validate()
	if len(res.Problems)+len(problems) > 0 || len(res.Document.Users) != 10000 {
		test.Fatalf("%d users, problems %v %v", len(res.Document.Users), res.Problems, problems)
	}
	test.Logf("10,000 users read, applied and validated in %v", time.Since(start))
	if time.Since(start) > 20*time.Second {
		test.Fatalf("too slow: %v", time.Since(start))
	}
}
