//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// The change preview: two documents compared user by user, group by group and template by template.
//
// OpenMCX administration (no 3GPP specification).
package config

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Change is one line of the change preview ("Diff preview").
type Change struct {
	Kind   string `json:"kind"`   // user-, group- or template- added, removed or changed
	Record string `json:"record"` // the login or group ID
	Field  string `json:"field,omitempty"`
	Before any    `json:"before,omitempty"`
	After  any    `json:"after,omitempty"`
}

// ------------------------------------------------------------------------------
// String is the change as one line: the kind and record, and for a changed field its path with the values before
// and after.
func (change Change) String() string {
	switch change.Kind {
	case "user-added", "user-removed", "group-added", "group-removed", "template-added", "template-removed":
		return fmt.Sprintf("%s %s", change.Kind, change.Record)
	}
	return fmt.Sprintf("%s %s %s: %s -> %s", change.Kind, change.Record, change.Field, show(change.Before), show(change.After))
}

// ------------------------------------------------------------------------------
// show is a value as JSON, or (none) for no value.
func show(value any) string {
	if value == nil {
		return "(none)"
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

// ------------------------------------------------------------------------------
// Diff compares two documents: users by login, groups by ID. A user's rights are
// compared on its effective profile, so a template change shows on every user it
// affects; each right is one path (profile.private_call.allow).
func Diff(before, after *Document) []Change {
	var out []Change
	bu, au := map[string]User{}, map[string]User{}
	for _, user := range before.Users {
		bu[user.Login] = user
	}
	for _, user := range after.Users {
		au[user.Login] = user
	}
	for _, login := range sortedKeys(bu) {
		if _, ok := au[login]; !ok {
			out = append(out, Change{Kind: "user-removed", Record: login})
		}
	}
	for _, login := range sortedKeys(au) {
		previous, ok := bu[login]
		if !ok {
			out = append(out, Change{Kind: "user-added", Record: login})
			continue
		}
		out = append(out, fieldChanges("user-changed", login, userView(before, previous), userView(after, au[login]))...)
	}
	bg, ag := groupIndex(before), groupIndex(after)
	for _, id := range sortedKeys(bg) {
		if _, ok := ag[id]; !ok {
			out = append(out, Change{Kind: "group-removed", Record: id})
		}
	}
	for _, id := range sortedKeys(ag) {
		previous, ok := bg[id]
		if !ok {
			out = append(out, Change{Kind: "group-added", Record: id})
			continue
		}
		out = append(out, fieldChanges("group-changed", id, previous, ag[id])...)
	}
	// Templates by name: a template no user has yet is a change of its own; one in
	// use also shows on each user's effective profile above.
	for _, name := range sortedKeys(before.Templates) {
		if _, ok := after.Templates[name]; !ok {
			out = append(out, Change{Kind: "template-removed", Record: name})
		}
	}
	for _, name := range sortedKeys(after.Templates) {
		previous, ok := before.Templates[name]
		if !ok {
			out = append(out, Change{Kind: "template-added", Record: name})
			continue
		}
		out = append(out, fieldChanges("template-changed", name, previous, after.Templates[name])...)
	}
	return out
}

// ------------------------------------------------------------------------------
// userView flattens a user for comparison: its account and identity fields and
// its effective profile under "profile".
func userView(doc *Document, user User) map[string]any {
	view := clone(map[string]any{
		"display_name": user.DisplayName, "email": user.Email, "status": user.Status, "template": user.Template,
		"mc_id": user.MCID, "mcptt_id": user.MCPTTID, "sip_uri": user.SIPURI, "client_ids": user.ClientIDs,
		"mcdata_id": user.MCDataID, "mcvideo_id": user.MCVideoID, "groups": user.Groups,
		"profile": doc.EffectiveProfile(user),
	})
	return view
}

// ------------------------------------------------------------------------------
// groupIndex is the document's groups by ID.
func groupIndex(doc *Document) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, group := range doc.Groups {
		id, _ := group["id"].(string)
		out[id] = group
	}
	return out
}

// ------------------------------------------------------------------------------
// fieldChanges compares two objects path by path. Lists of strings (groups,
// members, targets) are reported as the entries added and removed.
func fieldChanges(kind, record string, before, after map[string]any) []Change {
	bf, af := map[string]any{}, map[string]any{}
	flatten(before, "", bf)
	flatten(after, "", af)
	paths := map[string]bool{}
	for path := range bf {
		paths[path] = true
	}
	for path := range af {
		paths[path] = true
	}
	var out []Change
	for _, path := range sortedKeys(paths) {
		beforeValue, afterValue := bf[path], af[path]
		if equalJSON(beforeValue, afterValue) {
			continue
		}
		if bl, ok := asStrings(beforeValue); ok {
			if al, ok := asStrings(afterValue); ok {
				added, removed := setDiff(bl, al)
				if len(added) > 0 || len(removed) > 0 {
					out = append(out, Change{Kind: kind, Record: record, Field: path, Before: removedOrNil(removed), After: removedOrNil(added)})
				}
				continue
			}
		}
		out = append(out, Change{Kind: kind, Record: record, Field: path, Before: beforeValue, After: afterValue})
	}
	return out
}

// ------------------------------------------------------------------------------
// flatten puts the leaves of a JSON object into out by dotted path; an empty value (see empty) is left out.
func flatten(value any, prefix string, out map[string]any) {
	object, ok := value.(map[string]any)
	if !ok || len(object) == 0 {
		if prefix != "" && !empty(value) {
			out[prefix] = value
		}
		return
	}
	for key, element := range object {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		flatten(element, path, out)
	}
}

// ------------------------------------------------------------------------------
// empty treats absent, "", false, 0, [] and {} alike: "not granted, not set".
func empty(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == ""
	case bool:
		return !typed
	case float64:
		return typed == 0
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	}
	return false
}

// ------------------------------------------------------------------------------
// equalJSON compares two values by their JSON encoding.
func equalJSON(left, right any) bool {
	ra, _ := json.Marshal(left)
	rb, _ := json.Marshal(right)
	return string(ra) == string(rb)
}

// ------------------------------------------------------------------------------
// asStrings is a value as a list of strings when it is one (nil counting as the empty list).
func asStrings(value any) ([]string, bool) {
	if value == nil {
		return nil, true
	}
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

// ------------------------------------------------------------------------------
// setDiff is the entries of after not in before (added) and of before not in after (removed), each sorted.
func setDiff(before, after []string) (added, removed []string) {
	beforeSet, afterSet := map[string]bool{}, map[string]bool{}
	for _, entry := range before {
		beforeSet[entry] = true
	}
	for _, entry := range after {
		afterSet[entry] = true
	}
	for entry := range afterSet {
		if !beforeSet[entry] {
			added = append(added, entry)
		}
	}
	for entry := range beforeSet {
		if !afterSet[entry] {
			removed = append(removed, entry)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// ------------------------------------------------------------------------------
// removedOrNil is the list, or nil for an empty one, so that the change line shows (none).
func removedOrNil(list []string) any {
	if len(list) == 0 {
		return nil
	}
	return list
}
