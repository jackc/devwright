package userpolicy

import (
	"errors"
	"strings"
	"testing"
)

func TestNativeEffectiveGroups(t *testing.T) {
	public := "RecordName: com.apple.sharepoint.group.1\nNestedGroups: " + everyoneGUID + "\nPrimaryGroupID: 701\n"
	for _, tc := range []struct {
		name, goos, groups, record string
		wantError                  bool
	}{
		{"private Linux account", "linux", "devwright-example", "", false},
		{"Linux sudo group", "linux", "devwright-example sudo", public, true},
		{"Linux public-name bypass", "linux", "devwright-example com.apple.sharepoint.group.1", public, true},
		{"macOS automatic and SSH", "darwin", "devwright-example everyone localaccounts com.apple.access_ssh", "", false},
		{"public sharing inherited from Everyone", "darwin", "devwright-example everyone com.apple.sharepoint.group.1", public, false},
		{"wrapped nested GUIDs", "darwin", "devwright-example com.apple.sharepoint.group.1", "NestedGroups:\n OTHER-GUID\n " + everyoneGUID + "\nRecordName: fixture\n", false},
		{"private sharing still rejected", "darwin", "devwright-example com.apple.sharepoint.group.1", "NestedGroups: PRIVATE-GROUP-GUID\n", true},
		{"no nested group", "darwin", "devwright-example com.apple.sharepoint.group.1", "GroupMembership: devwright-example\n", true},
		{"misleading description", "darwin", "devwright-example com.apple.sharepoint.group.1", "RealName: " + everyoneGUID + "\n", true},
		{"misleading member GUID", "darwin", "devwright-example com.apple.sharepoint.group.1", "GroupMembers: " + everyoneGUID + "\n", true},
		{"lookalike sharing name", "darwin", "devwright-example com.apple.sharepoint.group.1-admin", public, true},
		{"nested admin remains forbidden", "darwin", "devwright-example admin", public, true},
		{"nested wheel remains forbidden", "darwin", "devwright-example wheel", public, true},
		{"default local printing access", "darwin", "devwright-example _lpoperator", "NestedGroups: " + localAccountsGUID + " ABCDEFAB-CDEF-ABCD-EFAB-CDEF00000062\n", false},
		{"printing name alone is insufficient", "darwin", "devwright-example _lpoperator", "GroupMembership: devwright-example\n", true},
		{"printing inherited only from print admins", "darwin", "devwright-example _lpoperator", "NestedGroups: ABCDEFAB-CDEF-ABCD-EFAB-CDEF00000062\n", true},
		{"printing wrong attribute", "darwin", "devwright-example _lpoperator", "GroupMembers: " + localAccountsGUID + "\n", true},
		{"printing Everyone alone is insufficient", "darwin", "devwright-example _lpoperator", public, true},
		{"print admin remains forbidden", "darwin", "devwright-example _lpadmin", "NestedGroups: " + localAccountsGUID + "\n", true},
		{"Linux printing remains forbidden", "linux", "devwright-example _lpoperator", "NestedGroups: " + localAccountsGUID + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckGroups(tc.goos, "devwright-example", tc.groups, func(string) (string, error) { return tc.record, nil })
			if (err != nil) != tc.wantError {
				t.Fatalf("CheckGroups: %v", err)
			}
		})
	}
	problem := errors.New("directory lookup failed")
	err := CheckGroups("darwin", "devwright-example", "com.apple.sharepoint.group.1", func(string) (string, error) { return "", problem })
	if !errors.Is(err, problem) {
		t.Fatalf("did not fail closed after directory lookup failure: %v", err)
	}
}

func TestCompleteDarwinAutomaticMemberships(t *testing.T) {
	// All automatic memberships observed on the macOS acceptance host, plus
	// the account's private group and the explicitly managed SSH access group.
	records := map[string]string{
		"com.apple.sharepoint.group.1": "NestedGroups: " + everyoneGUID + "\n",
		"_lpoperator":                  "NestedGroups: " + localAccountsGUID + " ABCDEFAB-CDEF-ABCD-EFAB-CDEF00000062\n",
	}
	read := func(name string) (string, error) {
		record, ok := records[name]
		if !ok {
			return "", errors.New("unexpected directory lookup")
		}
		return record, nil
	}
	names := "devwright-example everyone localaccounts com.apple.sharepoint.group.1 _lpoperator com.apple.access_ssh"
	if err := CheckGroups("darwin", "devwright-example", names, read); err != nil {
		t.Fatal(err)
	}
	// Report every incompatible membership in one pass, including after a
	// directory lookup failure, rather than failing one group per manual run.
	err := CheckGroups("darwin", "devwright-example", names+" admin _lpadmin _developer com.apple.sharepoint.group.2", read)
	if err == nil {
		t.Fatal("accepted privileged memberships")
	}
	for _, want := range []string{"admin, _lpadmin, _developer", "com.apple.sharepoint.group.2", "unexpected directory lookup"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q in %v", want, err)
		}
	}
}

func TestExplicitDarwinGroupMembership(t *testing.T) {
	const guid = "12345678-1234-1234-1234-123456789ABC"
	for _, tc := range []struct {
		record string
		want   bool
	}{
		{"NestedGroups: " + everyoneGUID + "\n", false},
		{"NestedGroups: " + guid + "\n", false},
		{"GroupMembership: devwright-example\n", true},
		{"GroupMembership:\n other\n devwright-example\n", true},
		{"GroupMembership: devwright-example-other\n", false},
		{"GroupMembers: " + strings.ToLower(guid) + "\n", true},
		{"RealName:\n devwright-example\nGroupMembership: other\n", false},
	} {
		if got := DarwinGroupHasUser(tc.record, "devwright-example", guid); got != tc.want {
			t.Fatalf("record %q: got %v", tc.record, got)
		}
	}
}
