// Package launch implements project onboarding around native Lima recipes.
package launch

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

//go:embed template scripts
var assets embed.FS
var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
var variable = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type app struct {
	ctx      context.Context
	in       io.Reader
	out, err io.Writer
	home     string
}
type options struct {
	from, ref, recipe, dotfiles, installer string
	noDotfiles, rootDotfiles               bool
	cpus                                   int
	memory, disk                           string
	params, sets                           []string
}
type preferences struct {
	Dotfiles  string `yaml:"dotfiles_repo"`
	Installer string `yaml:"dotfiles_install"`
	Root      bool   `yaml:"dotfiles_root"`
}
type credential struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}
type repository struct{ Origin, Commit, Branch string }
type manifest struct {
	Version           int
	Project, Dotfiles *repository
	LocalProject      string
	Installer         string
	RootDotfiles      bool
	Credentials       []credential
}
type instance struct {
	Name, Dir, Status, Hostname string
	Config                      struct {
		User struct {
			Name, Home string
			UID        int
		}
		Param  map[string]string
		Env    map[string]string
		Probes []struct{ Mode, Description, Script string }
	}
}

func Run(ctx context.Context, args []string, version string, in io.Reader, out, stderr io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	a := &app{ctx: ctx, in: in, out: out, err: stderr, home: home}
	root := &cobra.Command{Use: "devwright", Short: "Create a project development VM from a native Lima recipe", SilenceUsage: true, SilenceErrors: true}
	root.SetArgs(args)
	root.SetIn(in)
	root.SetOut(out)
	root.SetErr(stderr)
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(&cobra.Command{Use: "version", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { _, e := fmt.Fprintln(out, "devwright", version); return e }})
	root.AddCommand(&cobra.Command{Use: "init [DIRECTORY]", Short: "Write an editable .devwright Lima recipe (never overwrite)", Args: cobra.MaximumNArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		dir := "."
		if len(args) > 0 {
			dir = args[0]
		}
		return a.init(dir)
	}})
	o := options{}
	create := &cobra.Command{Use: "create NAME", Short: "Create a VM from a local directory or Git repository", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		if !validName.MatchString(args[0]) {
			return errors.New("name must be 1–40 lowercase letters, digits, or hyphens, starting with a letter")
		}
		p := preferences{Installer: "install"}
		prefDir := os.Getenv("XDG_CONFIG_HOME")
		if prefDir == "" {
			prefDir = filepath.Join(home, ".config")
		}
		if err := readYAML(filepath.Join(prefDir, "devwright/config.yaml"), &p, true); err != nil {
			return err
		}
		if !c.Flags().Changed("dotfiles") {
			o.dotfiles = p.Dotfiles
		}
		if !c.Flags().Changed("dotfiles-install") {
			o.installer = p.Installer
		}
		if !c.Flags().Changed("dotfiles-root") {
			o.rootDotfiles = p.Root
		}
		if o.noDotfiles {
			o.dotfiles = ""
			o.rootDotfiles = false
		}
		if o.rootDotfiles && o.dotfiles == "" {
			return errors.New("--dotfiles-root requires a dotfiles repository")
		}
		if !safeRelative(o.installer) {
			return errors.New("dotfiles installer must be a relative path inside its repository")
		}
		if o.cpus < 0 {
			return errors.New("cpus must be positive")
		}
		return a.create(args[0], o)
	}}
	f := create.Flags()
	f.StringVar(&o.from, "from", ".", "Local directory or Git repository URL")
	f.StringVar(&o.ref, "ref", "", "Branch, tag, or commit (defaults to HEAD)")
	f.StringVar(&o.recipe, "recipe", ".devwright/lima.yaml", "Recipe path relative to the project")
	f.StringVar(&o.dotfiles, "dotfiles", "", "Personal dotfiles repository; overrides user defaults")
	f.StringVar(&o.installer, "dotfiles-install", "install", "Executable installer relative to dotfiles checkout")
	f.BoolVar(&o.noDotfiles, "no-dotfiles", false, "Skip personal dotfiles")
	f.BoolVar(&o.rootDotfiles, "dotfiles-root", false, "Also install a separate dotfiles checkout as root")
	f.IntVar(&o.cpus, "cpus", 0, "Lima CPU override")
	f.StringVar(&o.memory, "memory", "", "Lima memory override, in GiB")
	f.StringVar(&o.disk, "disk", "", "Lima disk override, in GiB")
	f.StringArrayVar(&o.params, "param", nil, "Lima template parameter (repeatable; non-secret)")
	f.StringArrayVar(&o.sets, "set", nil, "Lima yq override (repeatable; non-secret)")
	create.MarkFlagsMutuallyExclusive("dotfiles", "no-dotfiles")
	root.AddCommand(create)
	root.AddCommand(&cobra.Command{Use: "finish NAME", Short: "Resume saved project onboarding after an interruption", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error { return a.finish(args[0]) }})
	var replace bool
	creds := &cobra.Command{Use: "credentials NAME", Short: "Prompt for missing project credentials", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		s, m, e := a.saved(args[0])
		if e != nil {
			return e
		}
		return a.credentials(s, m.Credentials, replace)
	}}
	creds.Flags().BoolVar(&replace, "replace", false, "Prompt again for all declared credentials")
	root.AddCommand(creds)
	return root.ExecuteContext(ctx)
}
func safeRelative(p string) bool {
	return p != "" && p != "." && !filepath.IsAbs(p) && filepath.Clean(p) != ".." && !strings.HasPrefix(filepath.Clean(p), ".."+string(filepath.Separator)) && !strings.ContainsAny(p, "\x00\r\n")
}
func readYAML(path string, dst any, optional bool) error {
	f, e := os.Open(path)
	if optional && errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	d := yaml.NewDecoder(f)
	d.KnownFields(true)
	if e = d.Decode(dst); e != nil {
		return fmt.Errorf("%s: %w", path, e)
	}
	var extra any
	if e = d.Decode(&extra); e != io.EOF {
		return fmt.Errorf("%s: expected one YAML document", path)
	}
	return nil
}
func (a *app) command(input io.Reader, output io.Writer, name string, args ...string) error {
	c := exec.CommandContext(a.ctx, name, args...)
	c.Stdin = input
	c.Stdout = output
	c.Stderr = a.err
	if e := c.Run(); e != nil {
		return fmt.Errorf("%s failed: %w", name, e)
	}
	return nil
}
func (a *app) run(name string, args ...string) error { return a.command(a.in, a.out, name, args...) }
func (a *app) capture(name string, args ...string) (string, error) {
	var b strings.Builder
	e := a.command(nil, &b, name, args...)
	return strings.TrimSpace(b.String()), e
}
func (a *app) init(dir string) error {
	if e := os.MkdirAll(dir, 0755); e != nil {
		return e
	}
	target := filepath.Join(dir, ".devwright")
	if _, e := os.Lstat(target); !errors.Is(e, os.ErrNotExist) {
		return fmt.Errorf("refusing to overwrite %s", target)
	}
	tmp, e := os.MkdirTemp(dir, ".devwright-init-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	e = fs.WalkDir(assets, "template", func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel := strings.TrimPrefix(path, "template")
		dst := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		data, e := assets.ReadFile(path)
		if e != nil {
			return e
		}
		return os.WriteFile(dst, data, 0644)
	})
	if e != nil {
		return e
	}
	if e = os.Rename(tmp, target); e != nil {
		return e
	}
	fmt.Fprintln(a.out, "Created", target, "— edit lima.yaml and its scripts, then run devwright create NAME --from", dir)
	return nil
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return atomic(path, append(b, '\n'))
}
func atomic(path string, b []byte) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".devwright-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func copyFile(src, dst string) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	ce := out.Close()
	if e != nil {
		return e
	}
	return ce
}
