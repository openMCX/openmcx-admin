// Validation of a configuration document: identities unique and well formed, references resolved, no secret.
//
// Specifications: TS 23.280 V19.10.1 (Rel-19).
package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Problem is one validation error, named by record and field so that an upload can
// report it against its row.
type Problem struct {
	Record string `json:"record"` // "user alice", "group sip:ops@x", "template t", "document"
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// ------------------------------------------------------------------------------
// String is the problem as one line: record, field and reason.
func (problem Problem) String() string {
	return problem.Record + ": " + problem.Field + ": " + problem.Reason
}

// secretKey matches the names of fields that would carry a secret. A document never
// holds one (MCX Admin handles no secrets; the plan's Scope).
var secretKey = regexp.MustCompile(`(?i)(password|passwd|secret|private_?key|certificate|^key$|_key$|^token$|_token$)`)

// ------------------------------------------------------------------------------
// Validate checks a document: identities unique and well formed (an MC service ID is a URI, TS 23.280 clause
// 8.1.2, and openMCX requires sip: URIs for it, the MC ID, the SIP URI and the MC service group ID of TS 23.280
// clause 8.1.3.1), references resolved, and no secret anywhere.
func (doc *Document) Validate() []Problem {
	check := &validation{doc: doc, owner: map[string]string{}, groups: map[string]bool{},
		known: map[string]map[string]bool{"mcptt": {}, "mcvideo": {}, "mcdata": {}}}
	if doc.System.Domain == "" {
		check.add("document", "system.domain", "missing")
	}
	for _, name := range sortedKeys(doc.Templates) {
		for _, path := range secretPaths(doc.Templates[name], "") {
			check.add("template "+name, path, "a secret is not allowed")
		}
	}
	for _, user := range doc.Users {
		check.user(user)
	}
	for _, group := range doc.Groups {
		check.group(group)
	}
	for _, group := range doc.Groups {
		check.members(group)
	}
	for _, user := range doc.Users {
		for _, group := range user.Groups {
			if !check.groups[group] {
				check.add("user "+user.Login, "groups", fmt.Sprintf("unknown group %s", group))
			}
		}
	}
	return check.problems
}

// validation is the state of one Validate run: the problems found, the owner of each identity claimed so far, the
// group IDs and the users' IDs per service.
type validation struct {
	doc      *Document
	problems []Problem
	owner    map[string]string
	groups   map[string]bool
	known    map[string]map[string]bool
}

// ------------------------------------------------------------------------------
// add records a problem of a record's field.
func (check *validation) add(record, field, reason string) {
	check.problems = append(check.problems, Problem{record, field, reason})
}

// ------------------------------------------------------------------------------
// claim records an identity for a record; one already claimed is a problem. Identities are unique across all users
// and kinds (TS 23.280 clause 8.1.1 and clause 8.1.2: an MC ID and an MC service ID each identify one user).
func (check *validation) claim(record, field, id string) {
	if id == "" {
		return
	}
	if prev, ok := check.owner[id]; ok {
		check.add(record, field, fmt.Sprintf("%s is also used by %s", id, prev))
		return
	}
	check.owner[id] = record
}

// ------------------------------------------------------------------------------
// user checks one user: login, status, template, the form and uniqueness of its identities, and no secret in its
// profile; its service IDs become known members.
func (check *validation) user(user User) {
	record := "user " + user.Login
	if user.Login == "" {
		check.add(record, "login", "missing")
	}
	check.claim(record, "login", "login:"+user.Login)
	if !Statuses[user.Status] {
		check.add(record, "status", fmt.Sprintf("%q is not one of pending, active, suspended, retired", user.Status))
	}
	if user.Template != "" {
		if _, ok := check.doc.Templates[user.Template]; !ok {
			check.add(record, "template", fmt.Sprintf("unknown template %q", user.Template))
		}
	}
	for field, id := range map[string]string{"mc_id": user.MCID, "mcptt_id": user.MCPTTID, "sip_uri": user.SIPURI} {
		if !strings.HasPrefix(id, "sip:") {
			check.add(record, field, fmt.Sprintf("%q is not a sip: URI", id))
		}
	}
	for field, id := range map[string]string{"mcdata_id": user.MCDataID, "mcvideo_id": user.MCVideoID} {
		if id != "" && !strings.HasPrefix(id, "sip:") {
			check.add(record, field, fmt.Sprintf("%q is not a sip: URI", id))
		}
	}
	check.claim(record, "mc_id", user.MCID)
	check.claim(record, "mcptt_id", user.MCPTTID)
	check.claim(record, "sip_uri", user.SIPURI)
	check.claim(record, "mcdata_id", user.MCDataID)
	check.claim(record, "mcvideo_id", user.MCVideoID)
	for _, clientID := range user.ClientIDs {
		check.claim(record, "client_ids", "client:"+clientID)
	}
	check.known["mcptt"][user.MCPTTID] = true
	for service, id := range map[string]string{"mcvideo": user.MCVideoID, "mcdata": user.MCDataID} {
		if id != "" {
			check.known[service][id] = true
		}
	}
	for _, path := range secretPaths(user.Profile, "profile") {
		check.add(record, path, "a secret is not allowed")
	}
}

// ------------------------------------------------------------------------------
// group checks one group's ID (a sip: URI, once) and that it holds no secret.
func (check *validation) group(group map[string]any) {
	id, _ := group["id"].(string)
	record := "group " + id
	if !strings.HasPrefix(id, "sip:") {
		check.add(record, "id", fmt.Sprintf("%q is not a sip: URI", id))
	}
	if check.groups[id] {
		check.add(record, "id", "duplicate group")
	}
	check.groups[id] = true
	for _, path := range secretPaths(group, "") {
		check.add(record, path, "a secret is not allowed")
	}
}

// ------------------------------------------------------------------------------
// members checks that a group's MCPTT, MCVideo and MCData members are users' IDs of that service.
func (check *validation) members(group map[string]any) {
	id, _ := group["id"].(string)
	record := "group " + id
	for _, member := range stringList(group["members"]) {
		if !check.known["mcptt"][member] {
			check.add(record, "members", fmt.Sprintf("unknown MCPTT ID %s", member))
		}
	}
	for _, service := range []struct{ section, name string }{{"mcvideo", "MCVideo"}, {"mcdata", "MCData"}} {
		part, _ := group[service.section].(map[string]any)
		for _, member := range stringList(part["members"]) {
			if !check.known[service.section][member] {
				check.add(record, service.section+".members", fmt.Sprintf("unknown %s ID %s", service.name, member))
			}
		}
	}
}

// ------------------------------------------------------------------------------
// secretPaths lists the paths of fields that carry a secret: by name, or by a PEM value.
func secretPaths(value any, prefix string) []string {
	var out []string
	join := func(key string) string {
		if prefix == "" {
			return key
		}
		return prefix + "." + key
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, key := range sortedKeys(typed) {
			if secretKey.MatchString(key) {
				out = append(out, join(key))
				continue
			}
			out = append(out, secretPaths(typed[key], join(key))...)
		}
	case []any:
		for index, element := range typed {
			out = append(out, secretPaths(element, fmt.Sprintf("%s[%d]", prefix, index))...)
		}
	case string:
		if strings.Contains(typed, "-----BEGIN") {
			out = append(out, prefix)
		}
	}
	return out
}

// ------------------------------------------------------------------------------
// stringList is the strings of a JSON list; other entries are skipped.
func stringList(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
