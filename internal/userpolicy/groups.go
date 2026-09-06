// Package userpolicy shares native-account checks between administration and
// the verifier running as the restricted account.
package userpolicy

import (
	"errors"
	"fmt"
	"strings"
)

const everyoneGUID = "ABCDEFAB-CDEF-ABCD-EFAB-CDEF0000000C"
const localAccountsGUID = "ABCDEFAB-CDEF-ABCD-EFAB-CDEF0000003D"

// CheckGroups permits only the private group, macOS's automatic groups, SSH
// access, public sharing inherited from Everyone, and the standard printing
// group inherited from Local Accounts. These are existing common access, not
// discretionary account grants. Print administration remains forbidden.
// A familiar group name alone never authorizes membership.
func CheckGroups(goos, account, names string, readGroup func(string) (string, error)) error {
	var problems []error
	var unexpected []string
	for _, name := range strings.Fields(names) {
		if name == account {
			continue
		}
		if goos == "darwin" {
			if name == "everyone" || name == "localaccounts" || name == "com.apple.access_ssh" {
				continue
			}
			ancestor := ""
			if isDarwinShareGroup(name) {
				ancestor = everyoneGUID
			} else if name == "_lpoperator" {
				ancestor = localAccountsGUID
			}
			if ancestor != "" {
				record, err := readGroup(name)
				if err != nil {
					problems = append(problems, fmt.Errorf("inspect effective group %s: %w", name, err))
					continue
				}
				if includesNestedGroup(record, ancestor) {
					continue
				}
			}
		}
		unexpected = append(unexpected, name)
	}
	if len(unexpected) > 0 {
		problems = append(problems, fmt.Errorf("incompatible host group policy: unexpected effective groups [%s] for %s; remove private or privileged membership administratively", strings.Join(unexpected, ", "), account))
	}
	return errors.Join(problems...)
}

func includesNestedGroup(record, ancestor string) bool {
	for _, guid := range groupValues(record, "NestedGroups") {
		if strings.EqualFold(guid, ancestor) {
			return true
		}
	}
	return false
}

func isDarwinShareGroup(name string) bool {
	suffix, ok := strings.CutPrefix(name, "com.apple.sharepoint.group.")
	if !ok || suffix == "" {
		return false
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// DarwinGroupHasUser distinguishes explicit membership from nested membership.
// Removing an individual from an Everyone-derived group cannot revoke the
// inherited public access and must not rewrite the host's nested-group policy.
func DarwinGroupHasUser(record, account, guid string) bool {
	for _, name := range groupValues(record, "GroupMembership") {
		if name == account {
			return true
		}
	}
	for _, member := range groupValues(record, "GroupMembers") {
		if guid != "" && strings.EqualFold(member, guid) {
			return true
		}
	}
	return false
}

// dscl wraps multivalued attributes onto indented continuation lines.
func groupValues(record, attribute string) []string {
	var values []string
	inside := false
	for _, line := range strings.Split(record, "\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if inside {
				values = append(values, strings.Fields(line)...)
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		inside = ok && key == attribute
		if inside {
			values = append(values, strings.Fields(value)...)
		}
	}
	return values
}
