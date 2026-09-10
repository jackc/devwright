package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// snapshot uses host Git authentication, then bundles only the selected history.
// No host Git configuration, hooks, agent socket, or credentials enter the guest.
func (a *app) snapshot(source, ref, dir string) (*repository, error) {
	if source == "" || strings.HasPrefix(source, "-") || strings.ContainsAny(source, "\x00\r\n") {
		return nil, errors.New("invalid repository location")
	}
	if e := a.run("git", "clone", "--no-hardlinks", "--no-checkout", "--", source, dir); e != nil {
		return nil, e
	}
	selected := ref
	if selected == "" {
		selected = "HEAD"
	}
	// rev-parse --end-of-options prevents a supplied ref becoming a Git option.
	commit, e := a.capture("git", "-C", dir, "rev-parse", "--verify", "--end-of-options", selected+"^{commit}")
	if e != nil && ref != "" {
		commit, e = a.capture("git", "-C", dir, "rev-parse", "--verify", "--end-of-options", "origin/"+ref+"^{commit}")
	}
	if e != nil {
		return nil, e
	}
	branch := "work"
	if ref != "" {
		if _, e := a.capture("git", "check-ref-format", "--branch", ref); e == nil {
			branch = ref
		}
	} else if b, e := a.capture("git", "-C", dir, "symbolic-ref", "--short", "HEAD"); e == nil {
		branch = b
	}
	if e = a.run("git", "-C", dir, "-c", "core.hooksPath=/dev/null", "checkout", "--detach", commit); e != nil {
		return nil, e
	}
	if e = a.run("git", "-C", dir, "branch", "--force", "devwright-transfer", commit); e != nil {
		return nil, e
	}
	if e = a.run("git", "-C", dir, "bundle", "create", dir+".bundle", "refs/heads/devwright-transfer"); e != nil {
		return nil, e
	}
	origin := source
	if st, e := os.Stat(source); e == nil && st.IsDir() {
		// Missing origin is normal for a local-only project; don't print a Git
		// error for this optional lookup.
		if remote, e := exec.CommandContext(a.ctx, "git", "-C", source, "remote", "get-url", "origin").Output(); e == nil {
			origin = strings.TrimSpace(string(remote))
		} else {
			origin = ""
		}
	}
	return &repository{Origin: origin, Commit: commit, Branch: branch}, nil
}
func (a *app) create(name string, o options) error {
	if !safeRelative(o.recipe) {
		return errors.New("recipe must be a path inside the project")
	}
	source := o.from
	local := false
	if st, e := os.Stat(source); e == nil && st.IsDir() {
		local = true
	}
	projectName := o.projectName
	if projectName == "" {
		var e error
		projectName, e = deriveProjectName(source, local)
		if e != nil {
			return e
		}
	} else if e := validateProjectName(projectName); e != nil {
		return e
	}
	// Refuse duplicate names before cloning repositories or executing a recipe.
	rows, e := a.capture("limactl", "list", "--format", "{{.Name}}")
	if e != nil {
		return e
	}
	for _, n := range strings.Fields(rows) {
		if n == name {
			return fmt.Errorf("VM %s already exists; choose a new name", name)
		}
	}
	temp, e := os.MkdirTemp("", "devwright-create-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(temp)
	if local {
		source, e = filepath.Abs(source)
		if e != nil {
			return e
		}
	}
	var project *repository
	var localProject string
	recipeRoot := filepath.Join(temp, "project")
	if local && o.ref == "" {
		recipeRoot = source
		localProject, e = snapshotDirectory(source, filepath.Join(temp, "project.tar.gz"))
	} else {
		project, e = a.snapshot(source, o.ref, recipeRoot)
	}
	if e != nil {
		return e
	}
	recipe := filepath.Join(recipeRoot, o.recipe)
	if _, e = os.Stat(recipe); e != nil {
		return fmt.Errorf("project recipe: %w; use devwright init in the project first", e)
	}
	declarations := []credential{}
	if e = readYAML(filepath.Join(filepath.Dir(recipe), "credentials.yaml"), &declarations, true); e != nil {
		return e
	}
	if e = validateCredentials(declarations); e != nil {
		return e
	}
	m := manifest{Project: project, ProjectName: projectName, LocalProject: localProject, Installer: o.installer, RootDotfiles: o.rootDotfiles, Credentials: declarations}
	if o.dotfiles != "" {
		m.Dotfiles, e = a.snapshot(o.dotfiles, "", filepath.Join(temp, "dotfiles"))
		if e != nil {
			return e
		}
	}
	hook := filepath.Join(filepath.Dir(recipe), "setup-project.sh")
	if data, e := os.ReadFile(hook); e == nil {
		if e = a.command(strings.NewReader(string(data)), a.out, "bash", "-n"); e != nil {
			return fmt.Errorf("setup-project.sh: %w", e)
		}
		if e = os.WriteFile(filepath.Join(temp, "setup-project.sh"), data, 0600); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	// Lima resolves and snapshots all native template references. We don't decode
	// or translate its VM schema, or put repository contents into its YAML.
	rendered := filepath.Join(temp, "lima.yaml")
	if e = a.run("limactl", "template", "copy", "--embed-all", recipe, rendered); e != nil {
		return e
	}
	if e = a.run("limactl", "validate", rendered); e != nil {
		return e
	}
	args := []string{"create", "--tty=false", "--name=" + name}
	if o.cpus != 0 {
		args = append(args, fmt.Sprintf("--cpus=%d", o.cpus))
	}
	if o.memory != "" {
		args = append(args, "--memory="+o.memory)
	}
	if o.disk != "" {
		args = append(args, "--disk="+o.disk)
	}
	for _, v := range o.params {
		args = append(args, "--param", v)
	}
	for _, v := range o.sets {
		args = append(args, "--set", v)
	}
	if e = a.run("limactl", append(args, rendered)...); e != nil {
		return e
	}
	s, e := a.info(name)
	if e != nil {
		return e
	}
	state := filepath.Join(s.Dir, "devwright")
	if e = os.Mkdir(state, 0700); e != nil {
		return e
	}
	for _, file := range []string{"project.bundle", "project.tar.gz", "dotfiles.bundle", "setup-project.sh"} {
		src := filepath.Join(temp, file)
		if _, e = os.Stat(src); errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e = copyFile(src, filepath.Join(state, file)); e != nil {
			return e
		}
	}
	if e = writeJSON(filepath.Join(state, "onboarding.json"), m); e != nil {
		return e
	}
	fmt.Fprintf(a.out, "Saved project snapshot. Resume an interrupted setup with: devwright finish %s\n", name)
	return a.finish(name)
}
func (a *app) info(name string) (instance, error) {
	var s instance
	if !validName.MatchString(name) {
		return s, errors.New("invalid instance name")
	}
	text, e := a.capture("limactl", "list", "--json", name)
	if e != nil {
		return s, e
	}
	d := json.NewDecoder(strings.NewReader(text))
	if e = d.Decode(&s); e != nil {
		return s, e
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return s, errors.New("expected exactly one Lima instance")
	}
	if s.Name != name || !filepath.IsAbs(s.Dir) || strings.ContainsAny(s.Dir, "\x00\r\n") {
		return s, errors.New("unexpected Lima instance identity")
	}
	if !variable.MatchString(s.Config.User.Name) || !filepath.IsAbs(s.Config.User.Home) {
		return s, errors.New("recipe requires an explicit Linux development user and home")
	}
	return s, nil
}
func (a *app) saved(name string) (instance, manifest, error) {
	s, e := a.info(name)
	var m manifest
	if e != nil {
		return s, m, e
	}
	b, e := os.ReadFile(filepath.Join(s.Dir, "devwright/onboarding.json"))
	if e != nil {
		return s, m, fmt.Errorf("no saved devwright onboarding for %s: %w", name, e)
	}
	if e = json.Unmarshal(b, &m); e != nil {
		return s, m, e
	}
	// Existing VMs predate source-based names and already use the VM name.
	if m.ProjectName == "" {
		m.ProjectName = name
	}
	if e = validateProjectName(m.ProjectName); e != nil {
		return s, m, fmt.Errorf("invalid saved project name: %w", e)
	}
	return s, m, validateCredentials(m.Credentials)
}
func validateCredentials(cs []credential) error {
	seen := map[string]bool{}
	for _, c := range cs {
		if !variable.MatchString(c.Name) || seen[c.Name] || strings.ContainsAny(c.Description, "\x00\r\n\x1b") {
			return errors.New("credentials require unique variable names and single-line descriptions")
		}
		seen[c.Name] = true
	}
	return nil
}
