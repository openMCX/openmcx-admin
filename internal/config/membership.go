// One membership: users' group lists and groups' MCPTT, MCVideo and MCData members kept in step with each other.
//
// Specifications: TS 24.484 V19.6.0 (Rel-19), TS 24.481 V19.3.0 (Rel-19).
package config

import (
	"encoding/json"
	"slices"
	"sort"
)

// ------------------------------------------------------------------------------
// Members is a group's MCPTT members (the <list> entries of its group document, TS 24.481 clause 7.2.2).
func Members(group map[string]any) []string {
	var out []string
	switch list := group["members"].(type) {
	case []any:
		for _, member := range list {
			if text, ok := member.(string); ok {
				out = append(out, text)
			}
		}
	case []string:
		out = append(out, list...)
	}
	return out
}

// ------------------------------------------------------------------------------
// FollowMembership keeps users' group lists (the <MCPTTGroupInfo> of their profiles, TS 24.484
// clause 8.3.2.1) in step with group membership changed between before and d: a
// user added to a group gains it, one removed loses it, and a deleted group leaves
// every list. Users whose membership did not change keep their lists as they are.
func (doc *Document) FollowMembership(before *Document) {
	members := func(doc *Document) map[string][]string {
		out := map[string][]string{}
		if doc == nil {
			return out
		}
		for _, group := range doc.Groups {
			id, _ := group["id"].(string)
			out[id] = Members(group)
		}
		return out
	}
	old, now := members(before), members(doc)
	byID := map[string]int{}
	for index, user := range doc.Users {
		byID[user.MCPTTID] = index
	}
	change := func(user, group string, add bool) {
		index, ok := byID[user]
		if !ok {
			return // a member of another system
		}
		account := &doc.Users[index]
		has := slices.Contains(account.Groups, group)
		switch {
		case add && !has:
			account.Groups = append(account.Groups, group)
			sort.Strings(account.Groups)
		case !add && has:
			account.Groups = slices.DeleteFunc(slices.Clone(account.Groups), func(name string) bool { return name == group })
		}
	}
	for id, list := range now {
		for _, member := range list {
			if !slices.Contains(old[id], member) {
				change(member, id, true)
			}
		}
		for _, member := range old[id] {
			if !slices.Contains(list, member) {
				change(member, id, false)
			}
		}
	}
	for id, list := range old {
		if _, kept := now[id]; !kept {
			for _, member := range list {
				change(member, id, false)
			}
		}
	}
}

// ------------------------------------------------------------------------------
// Clone is a deep copy of the document.
func (doc *Document) Clone() *Document {
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	var out Document
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return &out
}

// ------------------------------------------------------------------------------
// MCVideoGroups is the groups whose MCVideo members name the user, in the document's
// group order: the CMS's MCVideo profile in admin mode (slice D4).
func (doc *Document) MCVideoGroups(user User) []string {
	var out []string
	if user.MCVideoID == "" {
		return out
	}
	for _, group := range doc.Groups {
		if video, ok := group["mcvideo"].(map[string]any); ok && slices.Contains(Members(video), user.MCVideoID) {
			id, _ := group["id"].(string)
			out = append(out, id)
		}
	}
	return out
}

// ------------------------------------------------------------------------------
// section is a group's MCVideo or MCData part, nil when the group has none.
func section(group map[string]any, name string) map[string]any {
	part, _ := group[name].(map[string]any)
	return part
}

// ------------------------------------------------------------------------------
// addMember adds an ID to the members of a group or of one of its service sections, once; an empty ID is ignored.
func addMember(holder map[string]any, id string) {
	list := Members(holder)
	if id == "" || slices.Contains(list, id) {
		return
	}
	holder["members"] = stringsAny(append(list, id))
}

// ------------------------------------------------------------------------------
// removeMember takes an ID off the members of a group or of one of its service sections.
func removeMember(holder map[string]any, id string) {
	list := Members(holder)
	if id == "" || !slices.Contains(list, id) {
		return
	}
	holder["members"] = stringsAny(slices.DeleteFunc(list, func(member string) bool { return member == id }))
}

// ------------------------------------------------------------------------------
// join and leave change a user's membership of a group in every service the group
// carries: the MCPTT members, and the MCVideo and MCData members when it has them.
func join(group map[string]any, user User) {
	addMember(group, user.MCPTTID)
	if video := section(group, "mcvideo"); video != nil {
		addMember(video, user.MCVideoID)
	}
	if data := section(group, "mcdata"); data != nil {
		addMember(data, user.MCDataID)
	}
}

// ------------------------------------------------------------------------------
// leave takes a user off a group in every service the group carries (see join).
func leave(group map[string]any, user User) {
	removeMember(group, user.MCPTTID)
	if video := section(group, "mcvideo"); video != nil {
		removeMember(video, user.MCVideoID)
	}
	if data := section(group, "mcdata"); data != nil {
		removeMember(data, user.MCDataID)
	}
}

// ------------------------------------------------------------------------------
// FollowUserGroups makes groups follow users' group lists changed between before
// and d (a user edited, created or uploaded): a group added to a user's list gains
// the user as a member, one taken off loses it, in every service the group carries.
func (doc *Document) FollowUserGroups(before *Document) {
	old := map[string][]string{}
	if before != nil {
		for _, user := range before.Users {
			old[user.Login] = user.Groups
		}
	}
	index := map[string]map[string]any{}
	for _, group := range doc.Groups {
		id, _ := group["id"].(string)
		index[id] = group
	}
	for _, user := range doc.Users {
		was := old[user.Login]
		for _, id := range user.Groups {
			if group := index[id]; group != nil && !slices.Contains(was, id) {
				join(group, user)
			}
		}
		for _, id := range was {
			if group := index[id]; group != nil && !slices.Contains(user.Groups, id) {
				leave(group, user)
			}
		}
	}
}

// ------------------------------------------------------------------------------
// AlignServiceMembers gives a member added to a group's MCPTT members between before
// and d its MCVideo and MCData IDs in the group's other services, and takes them off
// for a member removed (a group edited by an administrator).
func (doc *Document) AlignServiceMembers(before *Document) {
	old := map[string][]string{}
	if before != nil {
		for _, group := range before.Groups {
			id, _ := group["id"].(string)
			old[id] = Members(group)
		}
	}
	byMCPTT := map[string]User{}
	for _, user := range doc.Users {
		byMCPTT[user.MCPTTID] = user
	}
	for _, group := range doc.Groups {
		id, _ := group["id"].(string)
		now := Members(group)
		for _, member := range now {
			if user, ok := byMCPTT[member]; ok && !slices.Contains(old[id], member) {
				join(group, user)
			}
		}
		for _, member := range old[id] {
			if user, ok := byMCPTT[member]; ok && !slices.Contains(now, member) {
				leave(group, user)
			}
		}
	}
}
