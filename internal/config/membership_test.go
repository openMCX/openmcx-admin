//
// OpenMCX Licence Information : AGPL-3.0-or-later
//

// Tests of one membership: MCVideo groups, group lists following members and members following group lists.
//
// OpenMCX administration (no 3GPP specification).
package config

import (
	"os"
	"slices"
	"testing"
)

// ------------------------------------------------------------------------------
// TestMembership: a user's MCVideo groups; group lists follow members added, removed and groups deleted.
func TestMembership(test *testing.T) {
	raw, _ := os.ReadFile("testdata/site.json")
	doc, _, err := ImportSite(raw)
	if err != nil {
		test.Fatal(err)
	}
	user := func(document *Document, login string) User {
		for _, candidate := range document.Users {
			if candidate.Login == login {
				return candidate
			}
		}
		test.Fatalf("no user %s", login)
		return User{}
	}
	if got := doc.MCVideoGroups(user(doc, "alice")); !slices.Equal(got, []string{"sip:operations@mcx.test"}) {
		test.Fatalf("alice's MCVideo groups %v", got)
	}
	if got := doc.MCVideoGroups(user(doc, "bob")); len(got) != 0 {
		test.Fatalf("bob's MCVideo groups %v", got)
	}
	// The MCData groups, from the groups' MCData members.
	doc.Groups[0]["mcdata"] = map[string]any{"members": []any{"sip:alice.mcdata@mcx.test"}}
	if got := doc.MCDataGroups(user(doc, "alice")); !slices.Equal(got, []string{"sip:operations@mcx.test"}) {
		test.Fatalf("alice's MCData groups %v", got)
	}
	if got := doc.MCDataGroups(user(doc, "bob")); len(got) != 0 {
		test.Fatalf("bob's MCData groups %v", got)
	}
	delete(doc.Groups[0], "mcdata")
	before := doc.Clone()
	// bob leaves operations; a new group has alice and bob.
	doc.Groups[0]["members"] = []any{"sip:alice.mcptt@mcx.test"}
	doc.Groups = append(doc.Groups, map[string]any{"id": "sip:night@mcx.test", "members": []any{"sip:alice.mcptt@mcx.test", "sip:bob.mcptt@mcx.test"}})
	doc.FollowMembership(before)
	if got := user(doc, "bob").Groups; !slices.Equal(got, []string{"sip:night@mcx.test"}) {
		test.Fatalf("bob's groups %v", got)
	}
	if got := user(doc, "alice").Groups; !slices.Contains(got, "sip:operations@mcx.test") || !slices.Contains(got, "sip:night@mcx.test") {
		test.Fatalf("alice's groups %v", got)
	}
	// Deleting the new group takes it off both lists.
	before = doc.Clone()
	doc.Groups = doc.Groups[:1]
	doc.FollowMembership(before)
	if slices.Contains(user(doc, "alice").Groups, "sip:night@mcx.test") || len(user(doc, "bob").Groups) != 0 {
		test.Fatalf("after deletion alice %v bob %v", user(doc, "alice").Groups, user(doc, "bob").Groups)
	}
}

// ------------------------------------------------------------------------------
// TestFollowUserGroups: a group taken off a user's list loses the user in every service; a member added back gains
// its MCVideo ID too.
func TestFollowUserGroups(test *testing.T) {
	raw, _ := os.ReadFile("testdata/site.json")
	doc, _, _ := ImportSite(raw)
	before := doc.Clone()
	// alice leaves operations from her user record: every service's members follow.
	for index := range doc.Users {
		if doc.Users[index].Login == "alice" {
			doc.Users[index].Groups = slices.DeleteFunc(slices.Clone(doc.Users[index].Groups), func(name string) bool { return name == "sip:operations@mcx.test" })
		}
	}
	doc.FollowUserGroups(before)
	ops := doc.Groups[0]
	if slices.Contains(Members(ops), "sip:alice.mcptt@mcx.test") || slices.Contains(Members(section(ops, "mcvideo")), "sip:alice.mcvideo@mcx.test") {
		test.Fatalf("alice is still a member: %v", ops)
	}
	// An administrator adds her back to the group: her MCVideo ID follows.
	before = doc.Clone()
	ops["members"] = append(stringsAny(Members(ops)), "sip:alice.mcptt@mcx.test")
	doc.AlignServiceMembers(before)
	if !slices.Contains(Members(section(ops, "mcvideo")), "sip:alice.mcvideo@mcx.test") {
		test.Fatalf("her MCVideo ID did not follow: %v", ops)
	}
}
