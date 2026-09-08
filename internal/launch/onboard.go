package launch

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func (a *app) ssh(s instance, user, script string, in io.Reader, out io.Writer) error {
	if s.Status != "Running" {
		return errors.New("VM must be running; start it with limactl start")
	}
	return a.command(in, out, "ssh", "-F", filepath.Join(s.Dir, "ssh.config"), "-o", "IdentityAgent=none", "-o", "ForwardAgent=no", "-o", "ControlPath=~/.ssh/control-%C", "-o", "ControlMaster=auto", "-o", "ControlPersist=60", "-o", "BatchMode=yes", "-l", user, "lima-"+s.Name, "/bin/bash -c "+quote(script))
}
func (a *app) finish(name string) error {
	s, m, e := a.saved(name)
	if e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(s.Dir, "devwright/lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return errors.New("another onboarding operation is running for this VM")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	if e = a.run("limactl", "start", "--tty=false", name); e != nil {
		return e
	}
	s, e = a.info(name)
	if e != nil {
		return e
	}
	user := s.Config.User.Name
	if e = a.ready(s); e != nil {
		return e
	}
	// Basic readiness only: project-specific tests belong in native Lima probes.
	if e = a.ssh(s, user, "set -e; command -v bash; test -w \"$HOME\"", nil, a.out); e != nil {
		return e
	}
	if m.LocalProject != "" {
		if e = a.installDirectory(s, m.LocalProject); e != nil {
			return e
		}
	}
	if m.Project != nil {
		if e = a.installRepository(s, user, m.Project, "project.bundle", "projects/"+name, ""); e != nil {
			return e
		}
	}
	if m.Dotfiles != nil {
		if e = a.installRepository(s, user, m.Dotfiles, "dotfiles.bundle", ".local/share/devwright/dotfiles", m.Installer); e != nil {
			return e
		}
		if m.RootDotfiles {
			if e = a.installRepository(s, "root", m.Dotfiles, "dotfiles.bundle", ".local/share/devwright/dotfiles", m.Installer); e != nil {
				return e
			}
		}
	}
	if e = a.setupCredentials(s); e != nil {
		return e
	}
	if e = a.installSSH(s); e != nil {
		return e
	}
	if e = a.credentials(s, m.Credentials, false); e != nil {
		return e
	}
	hook, e := os.ReadFile(filepath.Join(s.Dir, "devwright/setup-project.sh"))
	if e == nil {
		script := "set -euo pipefail\nmarker=\"$HOME/.local/state/devwright/project-setup-complete\"\n[ ! -f \"$marker\" ] || exit 0\nmkdir -p \"$(dirname \"$marker\")\"\ncd \"$HOME/projects/" + name + "\"\n. \"$HOME/.config/devwright/credentials.sh\"\n/bin/bash -s\ntouch \"$marker\"\n"
		if e = a.ssh(s, user, script, strings.NewReader(string(hook)), a.out); e != nil {
			return fmt.Errorf("project setup failed; finish retries it: %w", e)
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	fmt.Fprintf(a.out, "Ready: ssh lima-%s\nProject: ~/projects/%s\n", name, name)
	return nil
}

// limactl start returns immediately for an already-running VM. Recheck its
// saved native readiness scripts on resume, using Lima's documented guest
// template variables. These are the project's checks, not an agent verifier.
func (a *app) ready(s instance) error {
	var status strings.Builder
	// Exit 2 includes recoverable image warnings; inspect structured errors rather
	// than treating those warnings as a failed project recipe.
	if e := a.ssh(s, s.Config.User.Name, "cloud-init status --wait --format json; rc=$?; test \"$rc\" -eq 0 || test \"$rc\" -eq 2", nil, &status); e != nil {
		return fmt.Errorf("native provisioning failed; inspect cloud-init logs and restart to retry: %w", e)
	}
	if e := cloudReady(status.String()); e != nil {
		return e
	}
	data := map[string]any{"Home": s.Config.User.Home, "User": s.Config.User.Name, "UID": s.Config.User.UID, "Name": s.Name, "Hostname": s.Hostname, "Param": s.Config.Param}
	for _, p := range s.Config.Probes {
		if p.Mode != "readiness" {
			return fmt.Errorf("unsupported Lima probe mode %q", p.Mode)
		}
		t, e := template.New("probe").Option("missingkey=error").Parse(p.Script)
		if e != nil {
			return e
		}
		var b strings.Builder
		if e = t.Execute(&b, data); e != nil {
			return e
		}
		script := "set -euo pipefail\ntmp=$(mktemp)\ntrap 'rm -f -- \"$tmp\"' EXIT\ncat > \"$tmp\"\nchmod 700 \"$tmp\"\n"
		for k, v := range s.Config.Param {
			if !variable.MatchString(k) {
				return errors.New("invalid Lima parameter name")
			}
			script += "export PARAM_" + k + "=" + quote(v) + "\n"
		}
		script += "\"$tmp\"\n"
		if e = a.ssh(s, s.Config.User.Name, script, strings.NewReader(b.String()), a.out); e != nil {
			return fmt.Errorf("readiness probe %q failed: %w", p.Description, e)
		}
	}
	return nil
}

func cloudReady(raw string) error {
	var status struct {
		Status string
		Errors []any
	}
	if e := json.Unmarshal([]byte(raw), &status); e != nil {
		return fmt.Errorf("reading cloud-init status: %w", e)
	}
	if status.Status != "done" || len(status.Errors) > 0 {
		return errors.New("cloud-init provisioning did not succeed; inspect cloud-init logs and restart to retry")
	}
	return nil
}
func (a *app) installRepository(s instance, user string, r *repository, bundle, relative, installer string) error {
	if !safeRelative(relative) || !safeRelative(r.Branch) || (installer != "" && !safeRelative(installer)) {
		return errors.New("invalid saved repository path")
	}
	f, e := os.Open(filepath.Join(s.Dir, "devwright", bundle))
	if e != nil {
		return e
	}
	defer f.Close()
	// Stream outside Lima's YAML (and its template size limit). Each account gets
	// its own upload and checkout. Root never runs an installer from dev's files.
	script := `set -euo pipefail
umask 077
target="$HOME/` + relative + `"
mkdir -p "$(dirname "$target")"
stage=$(mktemp -d "$(dirname "$target")/.devwright-transfer.XXXXXXXX")
trap 'rm -rf -- "$stage"' EXIT
cat > "$stage/repository.bundle"
if [ ! -e "$target" ]; then
  git clone --branch devwright-transfer -- "$stage/repository.bundle" "$stage/checkout"
  git -C "$stage/checkout" branch -m ` + quote(r.Branch) + `
  git -C "$stage/checkout" remote remove origin
`
	if r.Origin != "" {
		script += "  git -C \"$stage/checkout\" remote add origin " + quote(r.Origin) + "\n"
	}
	script += `  test "$(git -C "$stage/checkout" rev-parse HEAD)" = ` + quote(r.Commit) + `
  printf '%s' ` + quote(r.Commit) + ` > "$stage/checkout/.git/devwright-source"
  mv -T "$stage/checkout" "$target"
fi
test "$(cat "$target/.git/devwright-source")" = ` + quote(r.Commit) + `
`
	if installer != "" {
		script += `if [ ! -f "$target/.git/devwright-installed" ]; then
  cd "$target"
  test -x ` + quote(installer) + `
  ` + quote("./"+installer) + `
  touch "$target/.git/devwright-installed"
fi
`
	}
	return a.ssh(s, user, script, f, a.out)
}
func (a *app) setupCredentials(s instance) error {
	b, e := assets.ReadFile("scripts/credentials.sh")
	if e != nil {
		return e
	}
	var environment strings.Builder
	keys := make([]string, 0, len(s.Config.Env))
	for k := range s.Config.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !variable.MatchString(k) || strings.ContainsRune(s.Config.Env[k], 0) {
			return errors.New("invalid Lima environment variable")
		}
		fmt.Fprintf(&environment, "export %s=%s\n", k, quote(s.Config.Env[k]))
	}
	script := string(b) + "\nprintf '%s' " + quote(base64.StdEncoding.EncodeToString([]byte(environment.String()))) + " | base64 -d > \"$directory/environment.sh\"\nchmod 600 \"$directory/environment.sh\"\n" + `
mkdir -p "$directory/credentials.d"
chmod 700 "$directory/credentials.d"
if ! grep -Fqx '# BEGIN DEVWRIGHT DECLARED CREDENTIALS' "$credentials"; then
cat >> "$credentials" <<'LOADER'
# BEGIN DEVWRIGHT DECLARED CREDENTIALS
. "$HOME/.config/devwright/environment.sh"
for devwright_credential in "$HOME/.config/devwright/credentials.d/"*.sh; do
  [ ! -f "$devwright_credential" ] || . "$devwright_credential"
done
unset devwright_credential
# END DEVWRIGHT DECLARED CREDENTIALS
LOADER
fi
`
	return a.ssh(s, s.Config.User.Name, "/bin/bash -s", strings.NewReader(script), a.out)
}
func credentialScript(name, value string) (string, error) {
	if !variable.MatchString(name) || value == "" || strings.ContainsRune(value, 0) {
		return "", errors.New("credential must have a valid name and a nonempty value without NUL")
	}
	data := base64.StdEncoding.EncodeToString([]byte("export " + name + "=" + quote(value) + "\n"))
	return `set -euo pipefail
umask 077
directory="$HOME/.config/devwright/credentials.d"
mkdir -p "$directory"
chmod 700 "$directory"
tmp=$(mktemp "$directory/.credential.XXXXXXXX")
trap 'rm -f -- "$tmp"' EXIT
printf '%s' '` + data + `' | base64 -d > "$tmp"
mv -fT "$tmp" "$directory/` + name + `.sh"
`, nil
}
func (a *app) credentials(s instance, items []credential, replace bool) error {
	if e := validateCredentials(items); e != nil {
		return e
	}
	for _, c := range items {
		var b strings.Builder
		probe := ". \"$HOME/.config/devwright/credentials.sh\"\nif [[ -n ${" + c.Name + ":-} ]]; then printf present; fi"
		if e := a.ssh(s, s.Config.User.Name, probe, nil, &b); e != nil {
			return e
		}
		if !replace && strings.HasSuffix(b.String(), "present") {
			continue
		}
		tty, e := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if e != nil {
			return fmt.Errorf("missing %s; run devwright finish %s in a terminal", c.Name, s.Name)
		}
		fmt.Fprintf(tty, "%s — %s: ", c.Name, c.Description)
		value, e := term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		tty.Close()
		if e != nil {
			return e
		}
		script, e := credentialScript(c.Name, string(value))
		clear(value)
		if e != nil {
			return e
		}
		// The value is stdin, never argv, saved host state, or Lima configuration.
		if e = a.ssh(s, s.Config.User.Name, "/bin/bash -s", strings.NewReader(script), io.Discard); e != nil {
			return fmt.Errorf("installing %s failed", c.Name)
		}
	}
	return nil
}
func (a *app) installSSH(s instance) error {
	directory := filepath.Join(a.home, ".ssh/devwright")
	if e := os.MkdirAll(directory, 0700); e != nil {
		return e
	}
	target := filepath.Join(directory, s.Name+".config")
	const header = "# Generated by devwright project launcher\n"
	if info, e := os.Lstat(target); e == nil {
		old, e := os.ReadFile(target)
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 || !strings.HasPrefix(string(old), header) {
			return errors.New("refusing to replace unmanaged SSH alias")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	path := strings.NewReplacer("\\", "\\\\", "\"", "\\\"").Replace(filepath.Join(s.Dir, "ssh.config"))
	content := header + "Host lima-" + s.Name + "\n  User " + s.Config.User.Name + "\n  IdentityAgent none\n  ForwardAgent no\n  ControlMaster auto\n  ControlPath ~/.ssh/control-%C\n  ControlPersist 60\n  Include \"" + path + "\"\n\nHost *\n"
	main := filepath.Join(a.home, ".ssh/config")
	old, e := os.ReadFile(main)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	line := "Include \"~/.ssh/devwright/*.config\""
	missing := true
	for _, l := range strings.Split(string(old), "\n") {
		if l == line {
			missing = false
		}
	}
	if missing {
		if st, e := os.Lstat(main); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("add %s to your SSH config symlink's source", line)
		}
	}
	if e = atomic(target, []byte(content)); e != nil {
		return e
	}
	if missing {
		if len(old) > 0 {
			backup, e := os.CreateTemp(filepath.Dir(main), "config.before-devwright-")
			if e != nil {
				return e
			}
			_, e = backup.Write(old)
			ce := backup.Close()
			if e != nil {
				return e
			}
			if ce != nil {
				return ce
			}
		}
		if e = atomic(main, append([]byte(line+"\n\n"), old...)); e != nil {
			return e
		}
	}
	return nil
}
