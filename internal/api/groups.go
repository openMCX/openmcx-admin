// Groups and templates of the API (slice D5): listed, created or replaced and deleted, each change a revision.
//
// Specifications: TS 24.481 V19.3.0 (Rel-19), TS 23.280 V19.10.1 (Rel-19).
package api

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

// Groups and templates (slice D5). A group is in the site.json form the GMS takes
// (openmcx-server); its members are MCPTT IDs, and the members' other services
// and group lists follow them (slice D4).

// ------------------------------------------------------------------------------
// listGroups answers the document's groups, in the site.json form the GMS takes.
func (server *Server) listGroups(writer http.ResponseWriter, request *http.Request, _ string) {
	rev, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	writer.Header().Set("ETag", etag(rev))
	writeJSON(writer, http.StatusOK, map[string]any{"groups": orGroups(doc.Groups)})
}

// ------------------------------------------------------------------------------
// orGroups is the groups, or an empty list for none.
func orGroups(groups []map[string]any) []map[string]any {
	if groups == nil {
		return []map[string]any{}
	}
	return groups
}

// ------------------------------------------------------------------------------
// putGroup creates or replaces a group on the If-Match revision. Its id is the path's and a sip: URI of the
// system's domain (an MC service group ID, TS 23.280 clause 8.1.3.1); members default to none and protect_media and
// protect_floor to false; the regroup fields are refused. The members' MCVideo and MCData memberships and their
// users' group lists follow (slice D4); the GMS renders the group as a group document of TS 24.481 clause 7.2.2.
func (server *Server) putGroup(writer http.ResponseWriter, request *http.Request, actor string) {
	rev, doc, ok := server.base(writer, request)
	if !ok {
		return
	}
	id := request.PathValue("id")
	var group map[string]any
	if err := json.NewDecoder(io.LimitReader(request.Body, 4<<20)).Decode(&group); err != nil {
		fail(writer, http.StatusBadRequest, "group: "+err.Error(), nil)
		return
	}
	if group["id"] != id || !strings.HasPrefix(id, "sip:") || !strings.HasSuffix(id, "@"+doc.System.Domain) {
		fail(writer, http.StatusBadRequest, "the group's id must be the path's, a sip: URI of "+doc.System.Domain, nil)
		return
	}
	if _, ok := group["members"]; !ok {
		group["members"] = []any{}
	}
	for _, key := range []string{"protect_media", "protect_floor"} {
		if _, ok := group[key]; !ok {
			group[key] = false
		}
	}
	for _, key := range []string{"regrouped", "regrouped_by", "temporary_constituents"} {
		if _, ok := group[key]; ok {
			fail(writer, http.StatusBadRequest, key+" belongs to the regroup procedure, not to MCX Admin", nil)
			return
		}
	}
	before := doc.Clone()
	replaced := false
	for index := range doc.Groups {
		if doc.Groups[index]["id"] == id {
			doc.Groups[index], replaced = group, true
		}
	}
	if !replaced {
		doc.Groups = append(doc.Groups, group)
	}
	doc.AlignServiceMembers(before)
	doc.FollowMembership(before)
	verb := "saved"
	if !replaced {
		verb = "created"
	}
	server.commit(writer, request, rev, doc, actor, "group "+id+" "+verb, "api")
}

// ------------------------------------------------------------------------------
// deleteGroup removes a group on the If-Match revision; its former members' group lists lose it.
func (server *Server) deleteGroup(writer http.ResponseWriter, request *http.Request, actor string) {
	rev, doc, ok := server.base(writer, request)
	if !ok {
		return
	}
	id := request.PathValue("id")
	before := doc.Clone()
	kept := doc.Groups[:0]
	for _, group := range doc.Groups {
		if group["id"] != id {
			kept = append(kept, group)
		}
	}
	if len(kept) == len(before.Groups) {
		fail(writer, http.StatusNotFound, "no such group", nil)
		return
	}
	doc.Groups = kept
	doc.FollowMembership(before)
	server.commit(writer, request, rev, doc, actor, "group "+id+" deleted", "api")
}

// ------------------------------------------------------------------------------
// listTemplates answers the templates by name, each with its profile sections and the number of users naming it.
func (server *Server) listTemplates(writer http.ResponseWriter, request *http.Request, _ string) {
	rev, doc, err := server.Store.Current(request.Context(), server.Domain)
	if err != nil {
		fail(writer, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	usage := map[string]int{}
	for _, user := range doc.Users {
		if user.Template != "" {
			usage[user.Template]++
		}
	}
	names := make([]string, 0, len(doc.Templates))
	for name := range doc.Templates {
		names = append(names, name)
	}
	sort.Strings(names)
	list := []map[string]any{}
	for _, name := range names {
		list = append(list, map[string]any{"name": name, "profile": doc.Templates[name], "users": usage[name]})
	}
	writer.Header().Set("ETag", etag(rev))
	writeJSON(writer, http.StatusOK, map[string]any{"templates": list})
}

// ------------------------------------------------------------------------------
// putTemplate creates or replaces a template (a JSON object of profile sections) on the If-Match revision.
func (server *Server) putTemplate(writer http.ResponseWriter, request *http.Request, actor string) {
	rev, doc, ok := server.base(writer, request)
	if !ok {
		return
	}
	name := request.PathValue("name")
	var profile map[string]any
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<20)).Decode(&profile); err != nil || profile == nil {
		fail(writer, http.StatusBadRequest, "a template is a JSON object of profile sections", nil)
		return
	}
	if doc.Templates == nil {
		doc.Templates = map[string]map[string]any{}
	}
	_, existed := doc.Templates[name]
	doc.Templates[name] = profile
	verb := "saved"
	if !existed {
		verb = "created"
	}
	server.commit(writer, request, rev, doc, actor, "template "+name+" "+verb, "api")
}

// ------------------------------------------------------------------------------
// deleteTemplate removes a template no user names, on the If-Match revision; one in use is refused (409) with the
// users naming it.
func (server *Server) deleteTemplate(writer http.ResponseWriter, request *http.Request, actor string) {
	rev, doc, ok := server.base(writer, request)
	if !ok {
		return
	}
	name := request.PathValue("name")
	if _, ok := doc.Templates[name]; !ok {
		fail(writer, http.StatusNotFound, "no such template", nil)
		return
	}
	var users []string
	for _, user := range doc.Users {
		if user.Template == name {
			users = append(users, user.Login)
		}
	}
	if len(users) > 0 {
		fail(writer, http.StatusConflict, "the template is in use", map[string]any{"users": users})
		return
	}
	delete(doc.Templates, name)
	server.commit(writer, request, rev, doc, actor, "template "+name+" deleted", "api")
}
