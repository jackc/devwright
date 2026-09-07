package devwright

import (
	"devwright/internal/userpolicy"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var errUserAbsent = errors.New("account does not exist")

type nativeIdentity struct {
	uid, gid   int
	home, guid string
}

func (a *userAdmin) lookup(name string) (nativeIdentity, error) {
	var id nativeIdentity
	if a.goos == "linux" {
		text, err := a.command(nil, "/usr/bin/getent", "passwd", name)
		if err != nil {
			var status *exec.ExitError
			if errors.As(err, &status) && status.ExitCode() == 2 {
				return id, errUserAbsent
			}
			return id, err
		}
		for _, line := range strings.Split(text, "\n") {
			f := strings.Split(line, ":")
			if len(f) == 7 && f[0] == name {
				uid, e := strconv.Atoi(f[2])
				if e != nil {
					return id, e
				}
				gid, e := strconv.Atoi(f[3])
				return nativeIdentity{uid: uid, gid: gid, home: f[5]}, e
			}
		}
	} else {
		text, err := a.command(nil, "/usr/bin/dscl", ".", "-list", "/Users")
		if err != nil {
			return id, err
		}
		found := false
		for _, line := range strings.Split(text, "\n") {
			if strings.TrimSpace(line) == name {
				found = true
			}
		}
		if !found {
			return id, errUserAbsent
		}
		text, err = a.command(nil, "/usr/bin/dscl", ".", "-read", "/Users/"+name, "UniqueID", "PrimaryGroupID", "NFSHomeDirectory", "GeneratedUID")
		if err != nil {
			return id, err
		}
		values := map[string]string{}
		for _, line := range strings.Split(text, "\n") {
			k, v, ok := strings.Cut(line, ": ")
			if ok {
				values[k] = strings.TrimSpace(v)
			}
		}
		uid, e := strconv.Atoi(values["UniqueID"])
		if e != nil {
			return id, e
		}
		gid, e := strconv.Atoi(values["PrimaryGroupID"])
		return nativeIdentity{uid: uid, gid: gid, home: values["NFSHomeDirectory"], guid: values["GeneratedUID"]}, e
	}
	return id, errUserAbsent
}
func (a *userAdmin) groups() (map[string]int, error) {
	result := map[string]int{}
	args := []string{"/usr/bin/getent", "group"}
	if a.goos == "darwin" {
		args = []string{"/usr/bin/dscl", ".", "-list", "/Groups", "PrimaryGroupID"}
	}
	text, err := a.command(nil, args...)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" {
			continue
		}
		var name, num string
		if a.goos == "linux" {
			f := strings.Split(line, ":")
			if len(f) != 4 {
				return nil, errors.New("invalid group database output")
			}
			name, num = f[0], f[2]
		} else {
			f := strings.Fields(line)
			if len(f) != 2 {
				return nil, errors.New("invalid Directory Services group output")
			}
			name, num = f[0], f[1]
		}
		gid, err := strconv.Atoi(num)
		if err != nil {
			return nil, err
		}
		result[name] = gid
	}
	return result, nil
}
func (a *userAdmin) groupExists(name string) bool {
	groups, err := a.groups()
	return err != nil || groups[name] != 0
}
func (a *userAdmin) allocateIDs() (int, int, error) {
	used := map[int]bool{}
	args := []string{"/usr/bin/getent", "passwd"}
	if a.goos == "darwin" {
		args = []string{"/usr/bin/dscl", ".", "-list", "/Users", "UniqueID"}
	}
	text, err := a.command(nil, args...)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line == "" {
			continue
		}
		var num string
		if a.goos == "linux" {
			f := strings.Split(line, ":")
			if len(f) != 7 {
				return 0, 0, errors.New("invalid passwd database")
			}
			num = f[2]
		} else {
			f := strings.Fields(line)
			if len(f) != 2 {
				return 0, 0, errors.New("invalid Directory Services user output")
			}
			num = f[1]
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			return 0, 0, err
		}
		used[n] = true
	}
	groups, err := a.groups()
	if err != nil {
		return 0, 0, err
	}
	for _, n := range groups {
		used[n] = true
	}
	// Tombstones prevent this tool from recycling IDs with leftover files elsewhere.
	entries, err := os.ReadDir(filepath.Join(a.base, "users"))
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(a.base, "users", entry.Name()))
		if err != nil {
			return 0, 0, err
		}
		var s userState
		if err := json.Unmarshal(data, &s); err != nil {
			return 0, 0, err
		}
		used[s.UID] = true
		used[s.GID] = true
	}
	for n := 1000; n < 60000; n++ {
		if used[n] {
			continue
		}
		free, err := a.effectiveIDFree(n)
		if err != nil {
			return 0, 0, err
		}
		if free {
			return n, n, nil
		}
	}
	return 0, 0, errors.New("no available managed UID/GID")
}
func (a *userAdmin) createAccount(s *userState) error {
	groups, err := a.groups()
	if err != nil {
		return err
	}
	if gid, ok := groups[s.Account]; ok {
		if gid != s.GID {
			return errors.New("partial group identity mismatch")
		}
	} else {
		args := []string{"/usr/sbin/groupadd", "--gid", strconv.Itoa(s.GID), s.Account}
		if a.goos == "darwin" {
			args = []string{"/usr/sbin/dseditgroup", "-o", "create", "-i", strconv.Itoa(s.GID), s.Account}
		}
		if _, err := a.command(nil, args...); err != nil {
			return err
		}
	}
	if a.goos == "darwin" {
		// Set the generated identity in the initial Directory Services operation.
		// Subsequent attribute writes can then be retried without adopting a record.
		names, err := a.command(nil, "/usr/bin/dscl", ".", "-list", "/Users")
		if err != nil {
			return err
		}
		exists := false
		for _, name := range strings.Fields(names) {
			if name == s.Account {
				exists = true
			}
		}
		if exists {
			value, err := a.command(nil, "/usr/bin/dscl", ".", "-read", "/Users/"+s.Account, "GeneratedUID")
			if err != nil {
				return err
			}
			if strings.TrimSpace(value) != "GeneratedUID: "+s.GUID {
				return errors.New("partial macOS account identity mismatch")
			}
		} else {
			if _, err := a.command(nil, "/usr/bin/dscl", ".", "-create", "/Users/"+s.Account, "GeneratedUID", s.GUID); err != nil {
				return err
			}
		}
		for _, pair := range [][2]string{{"UniqueID", strconv.Itoa(s.UID)}, {"PrimaryGroupID", strconv.Itoa(s.GID)}, {"NFSHomeDirectory", s.Home}, {"UserShell", "/bin/bash"}, {"RealName", "Devwright " + s.Name}, {"IsHidden", "1"}, {"Password", "*NP*"}} {
			if _, err := a.command(nil, "/usr/bin/dscl", ".", "-create", "/Users/"+s.Account, pair[0], pair[1]); err != nil {
				return err
			}
		}
	} else if id, err := a.lookup(s.Account); err == nil {
		if id.uid != s.UID || id.gid != s.GID || id.home != s.Home {
			return errors.New("partial account identity mismatch")
		}
	} else if !errors.Is(err, errUserAbsent) {
		return err
	} else {
		if _, err := a.command(nil, "/usr/sbin/useradd", "--no-create-home", "--no-user-group", "--no-log-init", "--uid", strconv.Itoa(s.UID), "--gid", strconv.Itoa(s.GID), "--home-dir", s.Home, "--shell", "/bin/bash", "--password", "*NP*", s.Account); err != nil {
			return err
		}
	}

	if err := secureUserDir(filepath.Dir(s.Home), 0755); err != nil {
		return err
	}
	if err := os.Mkdir(s.Home, 0700); err == nil {
		if err := os.Chown(s.Home, s.UID, s.GID); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	return a.checkIdentity(s)
}
func (a *userAdmin) checkIdentity(s *userState) error {
	id, err := a.lookup(s.Account)
	if err != nil {
		return err
	}
	if id.uid != s.UID || id.gid != s.GID || id.home != s.Home || (a.goos == "darwin" && id.guid != s.GUID) {
		return errors.New("managed account identity differs from registry; refusing operation")
	}
	groups, err := a.groups()
	if err != nil {
		return err
	}
	if groups[s.Account] != s.GID {
		return errors.New("managed private group identity differs")
	}
	if err := secureUserDir(filepath.Dir(s.Home), 0755); err != nil {
		return err
	}
	info, err := os.Lstat(s.Home)
	if err != nil {
		return err
	}
	if !info.IsDir() || int(info.Sys().(*syscall.Stat_t).Uid) != s.UID {
		return errors.New("managed home is not a directory owned by its recorded UID")
	}
	return nil
}
func (a *userAdmin) sudoPath(name string) string {
	return filepath.Join(a.etc, "sudoers.d", "99-devwright-"+name)
}
func (a *userAdmin) restrictAccount(s *userState) error {
	if a.goos == "linux" {
		if _, err := a.command(nil, "/usr/sbin/usermod", "-G", "", "-s", "/bin/bash", "-p", "*NP*", s.Account); err != nil {
			return err
		}
	} else {
		// Remove explicit local group memberships; retain only the private group and
		// Remote Login access group. Automatic everyone/localaccounts are OS-defined.
		groups, err := a.groups()
		if err != nil {
			return err
		}
		names, err := a.command(nil, "/usr/bin/id", "-Gn", s.Account)
		if err != nil {
			return err
		}
		for _, name := range strings.Fields(names) {
			if name == s.Account || name == "everyone" || name == "localaccounts" || name == "com.apple.access_ssh" {
				continue
			}
			if _, ok := groups[name]; !ok {
				return fmt.Errorf("cannot remove nonlocal membership %s", name)
			}
			record, err := a.command(nil, "/usr/bin/dscl", ".", "-read", "/Groups/"+name)
			if err != nil {
				return err
			}
			if !userpolicy.DarwinGroupHasUser(record, s.Account, s.GUID) {
				continue // The effective-membership audit still checks nested groups.
			}
			if _, err := a.command(nil, "/usr/sbin/dseditgroup", "-o", "edit", "-d", s.Account, "-t", "user", name); err != nil {
				return err
			}
		}
		if _, exists := groups["com.apple.access_ssh"]; exists {
			if _, err := a.command(nil, "/usr/sbin/dseditgroup", "-o", "edit", "-a", s.Account, "-t", "user", "com.apple.access_ssh"); err != nil {
				return err
			}
		}
		for _, pair := range [][2]string{{"Password", "*NP*"}, {"IsHidden", "1"}} {
			if _, err := a.command(nil, "/usr/bin/dscl", ".", "-create", "/Users/"+s.Account, pair[0], pair[1]); err != nil {
				return err
			}
		}
	}
	if err := os.Chmod(s.Home, 0700); err != nil {
		return err
	}
	// Clear inherited home ACL grants; chmod alone does not revoke macOS ACLs.
	if a.goos == "darwin" {
		if _, err := a.command(nil, "/bin/chmod", "-N", s.Home); err != nil {
			return err
		}
	}
	if err := writeUserFile(a.sudoPath(s.Name), []byte(s.Account+" ALL=(ALL:ALL) !ALL\n"), 0440); err != nil {
		return err
	}
	if _, err := a.command(nil, "/usr/sbin/visudo", "-cf", filepath.Join(a.etc, "sudoers")); err != nil {
		return err
	}
	return nil
}
func (a *userAdmin) audit(s *userState) error {
	if err := a.checkIdentity(s); err != nil {
		return err
	}
	if a.goos == "linux" {
		shadow, err := a.command(nil, "/usr/bin/getent", "shadow", s.Account)
		if err != nil {
			return err
		}
		fields := strings.Split(strings.TrimSpace(shadow), ":")
		if len(fields) != 9 || fields[0] != s.Account || fields[1] != "*NP*" {
			return errors.New("managed account password state changed; run configure")
		}
	}
	names, err := a.command(nil, "/usr/bin/id", "-Gn", s.Account)
	if err != nil {
		return err
	}
	if err := userpolicy.CheckGroups(a.goos, s.Account, names, func(name string) (string, error) {
		return a.command(nil, "/usr/bin/dscl", ".", "-read", "/Groups/"+name)
	}); err != nil {
		return err
	}
	grants, err := a.command(nil, "/usr/bin/sudo", "-l", "-U", s.Account)
	if err != nil {
		return fmt.Errorf("cannot inspect effective sudo policy: %w", err)
	}
	if err := checkUserSudo(grants); err != nil {
		return err
	}
	if a.goos == "darwin" {
		text, err := a.command(nil, "/usr/bin/dscl", ".", "-read", "/Users/"+s.Account)
		if err != nil {
			return err
		}
		if !strings.Contains(text, "IsHidden: 1") || strings.Contains(text, "SecureToken") || strings.Contains(text, "ShadowHashData") {
			return errors.New("managed macOS account gained login authentication or Secure Token")
		}
	}
	if err := a.checkSSHEffective(s); err != nil {
		return err
	}
	for path, want := range map[string]string{a.keysPath(s.Name): s.PublicKey + "\n", a.sshRulePath(s.Name): string(a.sshRule(s)), a.sudoPath(s.Name): s.Account + " ALL=(ALL:ALL) !ALL\n"} {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(data) != want {
			return fmt.Errorf("managed account file differs from its recipe: %s; run configure", path)
		}
	}
	for _, p := range []string{a.statePath(s.Name), a.keysPath(s.Name), a.sshRulePath(s.Name), a.sudoPath(s.Name), a.verifierPath()} {
		if err := secureUserFile(p); err != nil {
			return err
		}
	}
	return nil
}
func checkUserSudo(text string) error {
	seen := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "(") {
			seen = true
			_, rule, ok := strings.Cut(line, ")")
			if !ok || strings.TrimSpace(rule) != "!ALL" {
				return errors.New("host sudo policy grants commands to the managed user; remove those grants administratively")
			}
		}
	}
	if !seen {
		return errors.New("could not establish an explicit sudo denial")
	}
	return nil
}
func (a *userAdmin) deleteAccount(s *userState, removeHome bool) (userReply, error) {
	reply := userReply{State: s}
	id, lookupErr := a.lookup(s.Account)
	exists := lookupErr == nil
	if lookupErr != nil && !errors.Is(lookupErr, errUserAbsent) {
		return reply, lookupErr
	}
	if exists && (id.uid != s.UID || id.gid != s.GID || id.home != s.Home || (a.goos == "darwin" && id.guid != s.GUID)) {
		return reply, errors.New("account identity changed; refusing deletion")
	}
	if !exists && s.Phase != "deleting" {
		return reply, errors.New("managed account disappeared; refusing deletion")
	}
	if err := a.noProcesses(s.UID); err != nil {
		return reply, err
	}
	s.Phase = "deleting"
	if err := a.save(s); err != nil {
		return reply, err
	}
	if exists {
		if a.goos == "linux" {
			if _, err := a.command(nil, "/usr/sbin/usermod", "-s", "/usr/sbin/nologin", s.Account); err != nil {
				return reply, err
			}
		} else {
			if _, err := a.command(nil, "/usr/bin/dscl", ".", "-create", "/Users/"+s.Account, "UserShell", "/usr/bin/false"); err != nil {
				return reply, err
			}
		}
	}
	if _, err := os.Lstat(a.keysPath(s.Name)); err == nil {
		if err := writeUserFile(a.keysPath(s.Name), nil, 0644); err != nil {
			return reply, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return reply, err
	}
	if err := a.noProcesses(s.UID); err != nil {
		return reply, err
	}
	archiveRoot := filepath.Join(filepath.Dir(s.Home), ".devwright-archives")
	if err := secureUserDir(archiveRoot, 0700); err != nil {
		return reply, err
	}
	archive := filepath.Join(archiveRoot, fmt.Sprintf("%s-%d", s.Name, s.UID))
	if s.Archive != "" && s.Archive != archive {
		return reply, errors.New("invalid retained-home path in registry")
	}
	// Record the deterministic destination before renaming, allowing interruption
	// recovery without searching arbitrary directories or following user symlinks.
	if s.Archive == "" {
		if _, err := os.Lstat(archive); err == nil {
			return reply, errors.New("archive destination already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return reply, err
		}
		s.Archive = archive
		if err := a.save(s); err != nil {
			return reply, err
		}
	}
	if info, err := os.Lstat(s.Home); err == nil {
		if !exists || !info.IsDir() || int(info.Sys().(*syscall.Stat_t).Uid) != s.UID {
			return reply, errors.New("unsafe managed home")
		}
		if err := secureUserDir(filepath.Dir(s.Home), 0755); err != nil {
			return reply, err
		}
		if _, err := os.Lstat(archive); err == nil {
			return reply, errors.New("both home and archive exist; refusing overwrite")
		} else if !errors.Is(err, os.ErrNotExist) {
			return reply, err
		}
		if err := os.Rename(s.Home, archive); err != nil {
			return reply, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return reply, err
	}
	if info, err := os.Lstat(archive); err == nil {
		if !info.IsDir() || int(info.Sys().(*syscall.Stat_t).Uid) != s.UID {
			return reply, errors.New("unsafe archived home")
		}
	} else if !errors.Is(err, os.ErrNotExist) || exists {
		return reply, fmt.Errorf("missing archived home: %w", err)
	}
	if exists {
		if a.goos == "linux" {
			if _, err := a.command(nil, "/usr/sbin/userdel", s.Account); err != nil {
				return reply, err
			}
		} else {
			groups, err := a.groups()
			if err != nil {
				return reply, err
			}
			if _, ok := groups["com.apple.access_ssh"]; ok {
				if _, err := a.command(nil, "/usr/sbin/dseditgroup", "-o", "edit", "-d", s.Account, "-t", "user", "com.apple.access_ssh"); err != nil {
					return reply, err
				}
			}
			if _, err := a.command(nil, "/usr/bin/dscl", ".", "-delete", "/Users/"+s.Account); err != nil {
				return reply, err
			}
		}
	}
	groups, err := a.groups()
	if err != nil {
		return reply, err
	}
	if gid, ok := groups[s.Account]; ok {
		if gid != s.GID {
			return reply, errors.New("private group identity changed")
		}
		args := []string{"/usr/sbin/groupdel", s.Account}
		if a.goos == "darwin" {
			args = []string{"/usr/sbin/dseditgroup", "-o", "delete", s.Account}
		}
		if _, err := a.command(nil, args...); err != nil {
			return reply, err
		}
	}
	for _, p := range []string{a.keysPath(s.Name), a.sshRulePath(s.Name), a.sudoPath(s.Name)} {
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return reply, err
		}
		if err := secureUserFile(p); err != nil {
			return reply, err
		}
		if err := os.Remove(p); err != nil {
			return reply, err
		}
	}
	if err := a.reloadSSH(); err != nil {
		return reply, err
	}
	if removeHome {
		if err := os.RemoveAll(archive); err != nil {
			return reply, err
		}
		s.Archive = ""
	}
	s.Phase = "deleted"
	return reply, a.save(s)
}
func (a *userAdmin) noProcesses(uid int) error {
	text, err := a.command(nil, "/bin/ps", "-axo", "uid=,pid=,comm=")
	if err != nil {
		return err
	}
	processes := describeUserProcesses(text, uid)
	if len(processes) > 0 {
		hint := "stop its sessions/processes before deletion"
		if a.goos == "darwin" {
			hint += fmt.Sprintf("; macOS can retain per-user services after logout (inspect with sudo launchctl print user/%d)", uid)
		}
		return fmt.Errorf("account UID %d has running processes: %s; %s", uid, strings.Join(processes, ", "), hint)
	}
	return nil
}
func describeUserProcesses(text string, uid int) []string {
	var processes []string
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == strconv.Itoa(uid) {
			description := f[1]
			if len(f) > 2 {
				description += " (" + strings.Join(f[2:], " ") + ")"
			}
			processes = append(processes, description)
		}
	}
	return processes
}

// Enumeration may omit directory-service identities. Check each proposed ID
// through the effective lookup service before reserving it for a local account.
func (a *userAdmin) effectiveIDFree(id int) (bool, error) {
	for _, kind := range []string{"passwd", "group"} {
		if a.goos == "linux" {
			_, err := a.command(nil, "/usr/bin/getent", kind, strconv.Itoa(id))
			if err == nil {
				return false, nil
			}
			var status *exec.ExitError
			if !errors.As(err, &status) || status.ExitCode() != 2 {
				return false, err
			}
		} else {
			query, key := "user", "uid"
			if kind == "group" {
				query, key = "group", "gid"
			}
			text, err := a.command(nil, "/usr/bin/dscacheutil", "-q", query, "-a", key, strconv.Itoa(id))
			if err != nil {
				return false, err
			}
			if strings.TrimSpace(text) != "" {
				return false, nil
			}
		}
	}
	return true, nil
}

func (a *userAdmin) purgeArchive(s *userState) error {
	root := filepath.Join(filepath.Dir(s.Home), ".devwright-archives")
	expected := filepath.Join(root, fmt.Sprintf("%s-%d", s.Name, s.UID))
	if s.Archive != expected {
		return errors.New("invalid retained-home path in registry")
	}
	if err := secureUserDir(root, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(expected)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || int(info.Sys().(*syscall.Stat_t).Uid) != s.UID {
		return errors.New("unsafe archived home")
	}
	return os.RemoveAll(expected)
}
