// Package sheet reads the bulk-upload spreadsheet (CSV or XLSX) into the
// configuration model: one row per user, one row per group. Rights are never
// written in a cell: each user row names a template, which carries them.
//
// openMCX administration (no 3GPP specification).
package sheet

import (
	"crypto/sha1"
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/openMCX/openmcx-admin/internal/config"
)

// The columns of each sheet, in template order; * marks a required one.
var (
	UserColumns = []Column{
		{"login", true, "Sign-in name, letters, digits, dot, dash or underscore; the MC IDs are made from it"},
		{"display_name", false, "Full name"},
		{"email", false, "E-mail address, for the activation"},
		{"template", false, "Policy template that carries the rights, e.g. field-user; empty: the user's own rights only"},
		{"groups", false, "Group IDs or names, separated by ;"},
		{"services", false, "mcptt;mcdata;mcvideo (mcptt always); default mcptt; without the column, unchanged"},
		{"status", false, "pending (default), active, suspended or retired"},
		{"mc_id", false, "Only to override the generated MC ID"},
		{"mcptt_id", false, "Only to override the generated MCPTT ID"},
		{"sip_uri", false, "Only to override the generated SIP URI"},
		{"mcdata_id", false, "Only to override the generated MCData ID"},
		{"mcvideo_id", false, "Only to override the generated MCVideo ID"},
	}
	GroupColumns = []Column{
		{"id", true, "Group name or sip: URI; a name becomes sip:<name>@<domain>"},
		{"services", false, "mcptt;mcvideo (mcptt always); default mcptt; without the column, unchanged"},
		{"members", false, "Logins of the members, separated by ; (replaces the group's members)"},
	}
)

// Column is one column of a sheet.
type Column struct {
	Name     string
	Required bool
	Help     string
}

// Row is one data row of a sheet: its line number (as the user sees it, header = 1)
// and its cells by column name.
type Row struct {
	Line  int
	Cells map[string]string
}

// Problem is a row's error, with the column it concerns.
type Problem struct {
	Sheet  string `json:"sheet"`
	Line   int    `json:"line"`
	Column string `json:"column"`
	Reason string `json:"reason"`
}

// ------------------------------------------------------------------------------
// String is the problem as one line: sheet, line, column and reason.
func (problem Problem) String() string {
	return fmt.Sprintf("%s line %d, %s: %s", problem.Sheet, problem.Line, problem.Column, problem.Reason)
}

// ------------------------------------------------------------------------------
// ReadCSV reads the rows of one CSV sheet, checking its header against the columns.
func ReadCSV(input io.Reader, columns []Column) ([]Row, error) {
	reader := csv.NewReader(input)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	return rowsOf(records, columns)
}

// ------------------------------------------------------------------------------
// rowsOf turns the records of a sheet into rows: the first record is the header (a byte-order mark and the * of a
// required column ignored, unknown columns refused, required ones checked); blank rows are skipped.
func rowsOf(records [][]string, columns []Column) ([]Row, error) {
	if len(records) == 0 {
		return nil, fmt.Errorf("the sheet is empty: the first row names the columns")
	}
	header := make([]string, len(records[0]))
	known := map[string]bool{}
	for _, column := range columns {
		known[column.Name] = true
	}
	for index, name := range records[0] {
		name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))
		name = strings.TrimSuffix(name, "*")
		if name == ReportColumn {
			name = "" // the error column of a report given back (ReportCSV), ignored on upload
		}
		if name != "" && !known[name] {
			return nil, fmt.Errorf("unknown column %q", name)
		}
		header[index] = name
	}
	for _, column := range columns {
		if column.Required && !slices.Contains(header, column.Name) {
			return nil, fmt.Errorf("missing column %q", column.Name)
		}
	}
	var rows []Row
	for index, record := range records[1:] {
		// Every column of the header is in the row, empty when the record is shorter (a workbook row ends at
		// its last filled cell), so "the sheet has the column" does not depend on the row.
		cells := map[string]string{}
		for _, name := range header {
			if name != "" {
				cells[name] = ""
			}
		}
		blank := true
		for column, value := range record {
			if column < len(header) && header[column] != "" {
				cells[header[column]] = strings.TrimSpace(value)
				if cells[header[column]] != "" {
					blank = false
				}
			}
		}
		if !blank {
			rows = append(rows, Row{Line: index + 2, Cells: cells})
		}
	}
	return rows, nil
}

// Result is what an upload does to a document: the new document and the problems
// of its rows. With problems, nothing is applied (the job reports them per row).
type Result struct {
	Document *config.Document
	Problems []Problem
	Users    int // rows read
	Groups   int
}

// ------------------------------------------------------------------------------
// Apply applies user and group rows to a copy of base. A user row creates the user or
// updates its account fields, template, groups and services; its rights come from the
// template. A group row creates the group or replaces its members. Nothing is removed:
// a user leaves by status retired.
func Apply(base *config.Document, users, groups []Row) Result {
	doc := cloneDoc(base)
	upload := &application{doc: doc, result: Result{Document: doc, Users: len(users), Groups: len(groups)},
		byLogin: map[string]int{}, groupIndex: map[string]int{}, seenLogin: map[string]int{}}
	for index, user := range doc.Users {
		upload.byLogin[user.Login] = index
	}
	// Groups named by this upload exist for the user rows too.
	for index, group := range doc.Groups {
		id, _ := group["id"].(string)
		upload.groupIndex[id] = index
	}
	for _, row := range users {
		upload.user(row)
	}
	for _, row := range groups {
		upload.group(row)
	}
	// The users' groups name groups of the document; membership follows from the
	// group rows (the servers read both).
	for _, row := range users {
		index, ok := upload.byLogin[row.Cells["login"]]
		if !ok {
			continue
		}
		for _, group := range doc.Users[index].Groups {
			if _, ok := upload.groupIndex[group]; !ok {
				upload.bad(UsersSheet, row.Line, "groups", fmt.Sprintf("unknown group %s", group))
			}
		}
	}
	return upload.result
}

// application is the state of one Apply: the document being changed, the result with its problems, and the
// indexes of users by login, groups by ID and the line of each login seen in the upload.
type application struct {
	doc        *config.Document
	result     Result
	byLogin    map[string]int
	groupIndex map[string]int
	seenLogin  map[string]int
}

// ------------------------------------------------------------------------------
// bad records a problem of a cell.
func (upload *application) bad(sheet string, line int, column, reason string) {
	upload.result.Problems = append(upload.result.Problems, Problem{sheet, line, column, reason})
}

// ------------------------------------------------------------------------------
// groupID is the group ID a cell names: a sip: URI as it is, a name in the system's domain.
func (upload *application) groupID(name string) string {
	if strings.HasPrefix(name, "sip:") {
		return name
	}
	return "sip:" + name + "@" + upload.doc.System.Domain
}

// ------------------------------------------------------------------------------
// user applies one user row: the login checked (valid, once in the upload), the user created when new, its
// account fields, template, status, identities, services and groups set.
func (upload *application) user(row Row) {
	cells := row.Cells
	login := cells["login"]
	if !validLogin(login) {
		upload.bad(UsersSheet, row.Line, "login", fmt.Sprintf("%q is not a valid login", login))
		return
	}
	if first, ok := upload.seenLogin[login]; ok {
		upload.bad(UsersSheet, row.Line, "login", fmt.Sprintf("%s is also on line %d", login, first))
		return
	}
	upload.seenLogin[login] = row.Line
	if name := cells["template"]; name != "" {
		if _, ok := upload.doc.Templates[name]; !ok {
			upload.bad(UsersSheet, row.Line, "template", fmt.Sprintf("unknown template %q", name))
		}
	}
	status := cells["status"]
	if status == "" {
		status = "pending"
	}
	if !config.Statuses[status] {
		upload.bad(UsersSheet, row.Line, "status", fmt.Sprintf("%q is not one of pending, active, suspended, retired", status))
	}
	services, err := parseServices(cells["services"], "mcptt", "mcdata", "mcvideo")
	if err != nil {
		upload.bad(UsersSheet, row.Line, "services", err.Error())
	}
	index, exists := upload.byLogin[login]
	if !exists {
		domain := upload.doc.System.Domain
		upload.doc.Users = append(upload.doc.Users, config.User{Login: login, ClientIDs: []string{clientID(domain, login)}})
		index = len(upload.doc.Users) - 1
		upload.byLogin[login] = index
	}
	user := &upload.doc.Users[index]
	user.Status, user.Template = status, cells["template"]
	if value := cells["display_name"]; value != "" {
		user.DisplayName = value
	}
	if value := cells["email"]; value != "" {
		user.Email = value
	}
	upload.identities(row, user, services)
	user.Groups = user.Groups[:0]
	for _, group := range splitList(cells["groups"]) {
		user.Groups = append(user.Groups, upload.groupID(group))
	}
}

// ------------------------------------------------------------------------------
// identities sets a user's identities: generated (the lab's naming: alice.mc, alice.mcptt, alice, ...) and kept
// once set, unless a cell overrides them; the MCData and MCVideo IDs only with those services. Without a services
// column an existing user keeps its services (as without a members column).
func (upload *application) identities(row Row, user *config.User, services map[string]bool) {
	cells, login, domain := row.Cells, user.Login, upload.doc.System.Domain
	set := func(field *string, column, generated string) {
		switch {
		case cells[column] != "":
			if !strings.HasPrefix(cells[column], "sip:") {
				upload.bad(UsersSheet, row.Line, column, fmt.Sprintf("%q is not a sip: URI", cells[column]))
			}
			*field = cells[column]
		case *field == "":
			*field = generated
		}
	}
	set(&user.MCID, "mc_id", "sip:"+login+".mc@"+domain)
	set(&user.MCPTTID, "mcptt_id", "sip:"+login+".mcptt@"+domain)
	set(&user.SIPURI, "sip_uri", "sip:"+login+"@"+domain)
	_, servicesGiven := cells["services"]
	for _, service := range []struct {
		name  string
		field *string
	}{{"mcdata", &user.MCDataID}, {"mcvideo", &user.MCVideoID}} {
		if services[service.name] || !servicesGiven && *service.field != "" {
			set(service.field, service.name+"_id", "sip:"+login+"."+service.name+"@"+domain)
		} else {
			*service.field = ""
		}
	}
}

// ------------------------------------------------------------------------------
// group applies one group row: the group created when new, its MCPTT members replaced when the row has a members
// column, and its MCVideo section kept, set or removed by its services. Without a services column an existing
// group keeps its services (as without a members column).
func (upload *application) group(row Row) {
	cells := row.Cells
	if cells["id"] == "" {
		upload.bad(GroupsSheet, row.Line, "id", "missing")
		return
	}
	id := upload.groupID(cells["id"])
	services, err := parseServices(cells["services"], "mcptt", "mcvideo")
	if err != nil {
		upload.bad(GroupsSheet, row.Line, "services", err.Error())
	}
	index, exists := upload.groupIndex[id]
	if !exists {
		upload.doc.Groups = append(upload.doc.Groups, map[string]any{"id": id, "members": []any{}, "protect_media": false, "protect_floor": false})
		index = len(upload.doc.Groups) - 1
		upload.groupIndex[id] = index
	}
	group := upload.doc.Groups[index]
	if _, given := cells["services"]; !given {
		_, hasVideo := group["mcvideo"]
		services["mcvideo"] = hasVideo
	}
	if _, given := cells["members"]; given {
		var members, videoMembers []any
		for _, login := range splitList(cells["members"]) {
			userIndex, ok := upload.byLogin[login]
			if !ok {
				upload.bad(GroupsSheet, row.Line, "members", fmt.Sprintf("unknown login %s", login))
				continue
			}
			members = append(members, upload.doc.Users[userIndex].MCPTTID)
			if upload.doc.Users[userIndex].MCVideoID != "" {
				videoMembers = append(videoMembers, upload.doc.Users[userIndex].MCVideoID)
			}
		}
		group["members"] = orEmpty(members)
		if services["mcvideo"] {
			video, _ := group["mcvideo"].(map[string]any)
			if video == nil {
				video = map[string]any{}
			}
			video["members"] = orEmpty(videoMembers)
			group["mcvideo"] = video
		}
	}
	if !services["mcvideo"] {
		delete(group, "mcvideo")
	}
}

// ------------------------------------------------------------------------------
// Prepare is an upload as the API and the command line apply it: Apply, then one membership (slice D4: the user
// rows' group lists and the group rows' members kept in step, in every service a group carries), then the whole
// document validated, its problems added under DocumentSheet. With any problem nothing is to be applied.
func Prepare(base *config.Document, users, groups []Row) Result {
	res := Apply(base, users, groups)
	res.Document.FollowUserGroups(base)
	res.Document.AlignServiceMembers(base)
	res.Document.FollowMembership(base)
	for _, problem := range res.Document.Validate() {
		res.Problems = append(res.Problems, Problem{Sheet: DocumentSheet, Column: problem.Record + " " + problem.Field, Reason: problem.Reason})
	}
	return res
}

// ------------------------------------------------------------------------------
// validLogin accepts 1 to 64 letters, digits, dots, dashes and underscores.
func validLogin(login string) bool {
	if login == "" || len(login) > 64 {
		return false
	}
	for _, char := range login {
		letter := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
		if !letter && (char < '0' || char > '9') && !strings.ContainsRune(".-_", char) {
			return false
		}
	}
	return true
}

// ------------------------------------------------------------------------------
// parseServices reads a services cell (mcptt always included) against the services allowed.
func parseServices(cell string, allowed ...string) (map[string]bool, error) {
	out := map[string]bool{"mcptt": true}
	for _, service := range splitList(strings.ToLower(cell)) {
		if !slices.Contains(allowed, service) {
			return out, fmt.Errorf("unknown service %q (one of %s)", service, strings.Join(allowed, ", "))
		}
		out[service] = true
	}
	return out, nil
}

// ------------------------------------------------------------------------------
// splitList splits a cell at ;, commas and line ends, trimmed, empty entries dropped.
func splitList(cell string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(cell, func(char rune) bool { return char == ';' || char == ',' || char == '\n' }) {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// ------------------------------------------------------------------------------
// orEmpty is the values, or an empty JSON list for none.
func orEmpty(values []any) []any {
	if values == nil {
		return []any{}
	}
	return values
}

// ------------------------------------------------------------------------------
// clientID is a deterministic urn:uuid (a name-based UUID, RFC 9562 version 5 form)
// of the system and login, so that uploading the same sheet again changes nothing.
func clientID(domain, login string) string {
	sum := sha1.Sum([]byte("mcxadmin-client:" + domain + ":" + login))
	sum[6] = sum[6]&0x0f | 0x50
	sum[8] = sum[8]&0x3f | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// ------------------------------------------------------------------------------
// cloneDoc is a deep copy of a document, through its JSON.
func cloneDoc(doc *config.Document) *config.Document {
	out, err := config.Parse(mustJSON(doc))
	if err != nil {
		panic(err)
	}
	return out
}

// ------------------------------------------------------------------------------
// Batches splits the change from base to final (the result of Apply with these user
// rows) into steps of size user rows, for an upload applied one revision per batch.
// Step k holds the users of the first k*size rows; the others keep their base version
// or are not there yet, and group members that do not exist yet are left out, so each
// step validates. The last step is final.
func Batches(base, final *config.Document, users []Row, size int) []*config.Document {
	if size <= 0 || len(users) <= size {
		return []*config.Document{final}
	}
	baseUsers := map[string]config.User{}
	for _, user := range base.Users {
		baseUsers[user.Login] = user
	}
	var out []*config.Document
	for start := size; start < len(users); start += size {
		later := map[string]bool{}
		for _, row := range users[start:] {
			later[row.Cells["login"]] = true
		}
		step := cloneDoc(final)
		kept := step.Users[:0]
		for _, user := range step.Users {
			switch baseUser, existed := baseUsers[user.Login]; {
			case !later[user.Login]:
				kept = append(kept, user)
			case existed:
				kept = append(kept, baseUser)
			}
		}
		step.Users = kept
		keepPresentMembers(step)
		out = append(out, step)
	}
	return append(out, final)
}

// ------------------------------------------------------------------------------
// keepPresentMembers takes off a step's group members (MCPTT, MCVideo and MCData) that are users of this system
// not in the step yet; members of other systems stay.
func keepPresentMembers(step *config.Document) {
	present := map[string]bool{}
	for _, user := range step.Users {
		for _, id := range []string{user.MCPTTID, user.MCVideoID, user.MCDataID} {
			if id != "" {
				present[id] = true
			}
		}
	}
	domain := "@" + step.System.Domain
	keep := func(value any) []any {
		out := []any{}
		for _, member := range stringsOf(value) {
			if present[member] || !strings.HasSuffix(member, domain) {
				out = append(out, member)
			}
		}
		return out
	}
	for _, group := range step.Groups {
		group["members"] = keep(group["members"])
		for _, name := range []string{"mcvideo", "mcdata"} {
			if part, ok := group[name].(map[string]any); ok {
				part["members"] = keep(part["members"])
			}
		}
	}
}

// ------------------------------------------------------------------------------
// stringsOf is the strings of a JSON list; other entries are skipped.
func stringsOf(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
