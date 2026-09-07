package devsandbox

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

const usage = `Usage: dev-sandbox ACTION [NAME] [OPTIONS]
Actions: render, create, configure, verify, ssh-config, install-ssh
User backend only: list, shell, delete (delete requires an explicit NAME)
Default environment: dev

Options (may appear before or after the action):
  --backend lima|incus|user  Boundary manager (default: lima)
  --ssh-port N            Existing system SSH port (user create/configure/render; default: 22)
  --remove-home           Delete the managed home instead of archiving it (user delete)
  --container             Create an Incus system container instead of a VM (create/render)
  --network NAME          Incus managed network (default: incusbr0; create/render)
  --storage NAME          Incus storage pool (default: default; create/render)
  --dotfiles-repo URL      Install this Git repository for root and dev (create/configure)
  --dotfiles-install PATH  Installer relative to the repository (default: install)
  --codex-requirements FILE  Use and remember a custom managed policy (create/configure)
  --reset-codex-requirements Restore the embedded managed policy (configure)
  --codex-config FILE      Initial dev config; existing config is preserved (create/configure)
  --replace-codex-config   Replace existing dev config with --codex-config (configure)
  --claude-managed-settings FILE  Use and remember a custom managed Claude Code policy (create/configure)
  --reset-claude-managed-settings Restore the embedded managed Claude Code policy (configure)
  --claude-config FILE     Initial dev Claude Code settings; existing settings are preserved (create/configure)
  --replace-claude-config  Replace existing dev Claude Code settings with --claude-config (configure)
  --cpus N                CPUs for a new VM (default: 4; create/render only)
  --memory SIZE           Memory for a new VM, e.g. 8GiB (default: 4GiB; create/render only)
  --disk SIZE             Disk for a new VM, e.g. 100GiB (default: 60GiB; create/render only)
  --version               Print the CLI/embedded recipe version
  -h, --help              Show this help

Use limactl or incus start/stop/delete for lifecycle; ssh lima-NAME or incus-NAME for development.
Incus uses the local server's default project. Repeat --backend incus on every action.
`

type options struct {
	codexRequirements, codexConfig                            string
	resetCodexRequirements, replaceCodexConfig                bool
	requirementsPayload, configPayload                        []byte
	claudeManagedSettings, claudeConfig                       string
	resetClaudeManagedSettings, replaceClaudeConfig           bool
	managedPayload, claudeConfigPayload                       []byte
	action, name, dotfilesRepo, dotfilesInstall, memory, disk string
	cpus                                                      int
	backend, network, storage                                 string
	container                                                 bool
	sshPort                                                   int
	removeHome                                                bool
	help, version                                             bool
	set                                                       map[string]bool
}

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
var validSize = regexp.MustCompile(`^[1-9][0-9]*(MiB|GiB|TiB)$`)

func parseOptions(args []string) (options, error) {
	o := options{name: "dev", set: map[string]bool{}}
	f := flag.NewFlagSet("dev-sandbox", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.BoolVar(&o.help, "help", false, "")
	f.BoolVar(&o.help, "h", false, "")
	f.BoolVar(&o.version, "version", false, "")
	f.StringVar(&o.backend, "backend", "lima", "")
	f.IntVar(&o.sshPort, "ssh-port", 22, "")
	f.BoolVar(&o.removeHome, "remove-home", false, "")
	f.BoolVar(&o.container, "container", false, "")
	f.StringVar(&o.network, "network", "incusbr0", "")
	f.StringVar(&o.storage, "storage", "default", "")
	f.StringVar(&o.dotfilesRepo, "dotfiles-repo", "", "")
	f.StringVar(&o.dotfilesInstall, "dotfiles-install", "install", "")
	f.StringVar(&o.codexRequirements, "codex-requirements", "", "")
	f.StringVar(&o.codexConfig, "codex-config", "", "")
	f.BoolVar(&o.resetCodexRequirements, "reset-codex-requirements", false, "")
	f.BoolVar(&o.replaceCodexConfig, "replace-codex-config", false, "")
	f.StringVar(&o.claudeManagedSettings, "claude-managed-settings", "", "")
	f.StringVar(&o.claudeConfig, "claude-config", "", "")
	f.BoolVar(&o.resetClaudeManagedSettings, "reset-claude-managed-settings", false, "")
	f.BoolVar(&o.replaceClaudeConfig, "replace-claude-config", false, "")
	f.IntVar(&o.cpus, "cpus", 0, "")
	f.StringVar(&o.memory, "memory", "", "")
	f.StringVar(&o.disk, "disk", "", "")
	var positional []string
	// flag stops at the first positional argument; resume parsing to retain the
	// previous CLI's support for options on either side of ACTION and NAME.
	for len(args) > 0 {
		if args[0] == "--" {
			positional = append(positional, args[1:]...)
			break
		}
		if args[0] == "-" || !strings.HasPrefix(args[0], "-") {
			positional = append(positional, args[0])
			args = args[1:]
			continue
		}
		key, _, hasValue := strings.Cut(strings.TrimLeft(args[0], "-"), "=")
		count := 1
		if entry := f.Lookup(key); entry != nil && !hasValue {
			boolean, ok := entry.Value.(interface{ IsBoolFlag() bool })
			if (!ok || !boolean.IsBoolFlag()) && len(args) > 1 {
				count = 2
			}
		}
		if err := f.Parse(args[:count]); err != nil {
			return o, err
		}
		args = args[count:]
	}
	f.Visit(func(f *flag.Flag) { o.set[f.Name] = true })
	if o.help || o.version {
		return o, nil
	}
	if len(positional) < 1 || len(positional) > 2 {
		return o, errors.New(usage)
	}
	o.action = positional[0]
	if len(positional) == 2 {
		o.name = positional[1]
	}
	switch o.action {
	case "render", "create", "configure", "verify", "ssh-config", "install-ssh":
	case "list", "shell", "delete":
		if o.backend != "user" {
			return o, fmt.Errorf("%s applies only to --backend user", o.action)
		}
	default:
		return o, fmt.Errorf("unknown action: %s; use --help", o.action)
	}
	if !validName.MatchString(o.name) {
		return o, errors.New("Environment name must start with a letter and contain only lowercase letters, digits, and hyphens (maximum 40 characters)")
	}
	if o.backend != "lima" && o.backend != "incus" && o.backend != "user" {
		return o, errors.New("--backend must be lima, incus, or user")
	}
	if o.backend == "user" {
		if len(o.name) > 28 {
			return o, errors.New("user environment names have a maximum of 28 characters")
		}
		if o.action == "delete" && len(positional) != 2 {
			return o, errors.New("delete requires an explicit environment name")
		}
		if o.action == "list" && len(positional) != 1 {
			return o, errors.New("list does not accept a name")
		}
		for _, key := range []string{"cpus", "memory", "disk", "codex-requirements", "reset-codex-requirements", "claude-managed-settings", "reset-claude-managed-settings"} {
			if o.set[key] {
				return o, fmt.Errorf("--%s is unsupported by the user backend; host resources and managed Codex/Claude Code policies remain host-administered", key)
			}
		}
	}
	if o.set["ssh-port"] && (o.backend != "user" || (o.action != "create" && o.action != "configure" && o.action != "render")) {
		return o, errors.New("--ssh-port applies only to user create/configure/render")
	}
	if o.sshPort < 1 || o.sshPort > 65535 {
		return o, errors.New("--ssh-port must be between 1 and 65535")
	}
	if o.set["remove-home"] && (o.backend != "user" || o.action != "delete") {
		return o, errors.New("--remove-home applies only to user delete")
	}
	for _, key := range []string{"container", "network", "storage"} {
		if o.set[key] && (o.backend != "incus" || (o.action != "create" && o.action != "render")) {
			return o, fmt.Errorf("--%s applies only to Incus create and render", key)
		}
	}
	if !validIncusResource.MatchString(o.network) || !validIncusResource.MatchString(o.storage) {
		return o, errors.New("expected a simple Incus network or storage pool name")
	}
	for _, key := range []string{"codex-requirements", "codex-config", "reset-codex-requirements", "replace-codex-config",
		"claude-managed-settings", "claude-config", "reset-claude-managed-settings", "replace-claude-config"} {
		if o.set[key] && o.action != "create" && o.action != "configure" {
			return o, fmt.Errorf("--%s applies only to create and configure", key)
		}
	}
	if (o.resetCodexRequirements || o.replaceCodexConfig || o.resetClaudeManagedSettings || o.replaceClaudeConfig) && o.action != "configure" {
		return o, errors.New("reset/replace Codex and Claude Code options apply only to configure")
	}
	for _, agent := range []struct {
		reset, replace                             bool
		policyKey, resetKey, configKey, replaceKey string
	}{
		{o.resetCodexRequirements, o.replaceCodexConfig, "codex-requirements", "reset-codex-requirements", "codex-config", "replace-codex-config"},
		{o.resetClaudeManagedSettings, o.replaceClaudeConfig, "claude-managed-settings", "reset-claude-managed-settings", "claude-config", "replace-claude-config"},
	} {
		if agent.reset && o.set[agent.policyKey] {
			return o, fmt.Errorf("--%s conflicts with --%s", agent.resetKey, agent.policyKey)
		}
		if agent.replace && !o.set[agent.configKey] {
			return o, fmt.Errorf("--%s requires --%s", agent.replaceKey, agent.configKey)
		}
	}
	for key, path := range map[string]string{"codex-requirements": o.codexRequirements, "codex-config": o.codexConfig,
		"claude-managed-settings": o.claudeManagedSettings, "claude-config": o.claudeConfig} {
		if o.set[key] && path == "" {
			return o, fmt.Errorf("--%s requires a file path", key)
		}
	}
	if o.set["dotfiles-install"] && !o.set["dotfiles-repo"] {
		return o, errors.New("--dotfiles-install requires --dotfiles-repo")
	}
	if o.set["dotfiles-repo"] {
		if o.action != "create" && o.action != "configure" {
			return o, errors.New("dotfiles options apply only to create and configure")
		}
		if o.dotfilesRepo == "" || strings.HasPrefix(o.dotfilesRepo, "-") || strings.ContainsRune(o.dotfilesRepo, 0) {
			return o, errors.New("expected a dotfiles Git repository URL")
		}
	}
	if o.dotfilesInstall == "" || strings.HasPrefix(o.dotfilesInstall, "/") || strings.ContainsRune(o.dotfilesInstall, 0) {
		return o, errors.New("dotfiles installer must be a relative path within the repository")
	}
	for _, part := range strings.Split(o.dotfilesInstall, "/") {
		if part == ".." {
			return o, errors.New("dotfiles installer must be a relative path within the repository")
		}
	}
	for _, key := range []string{"cpus", "memory", "disk"} {
		if o.set[key] && o.action != "create" && o.action != "render" {
			return o, errors.New("resource options apply only to create and render; edit existing resources with the instance manager")
		}
	}
	if o.set["cpus"] && o.cpus < 1 {
		return o, errors.New("--cpus must be a positive integer")
	}
	for key, value := range map[string]string{"memory": o.memory, "disk": o.disk} {
		if o.set[key] && !validSize.MatchString(value) {
			return o, fmt.Errorf("--%s must be a positive whole number with MiB, GiB, or TiB units (e.g. 8GiB)", key)
		}
	}
	return o, nil
}

// Run executes the CLI. Rendering and help work without Lima or SSH installed.
func Run(ctx context.Context, args []string, version string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 1 && args[0] == "__user-helper" {
		return runUserHelper(ctx, stdin, stdout, stderr)
	}
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	if o.help {
		_, err = io.WriteString(stdout, usage)
		return err
	}
	if o.version {
		_, err = fmt.Fprintf(stdout, "dev-sandbox %s\n", version)
		return err
	}
	if err := o.loadAgentFiles(); err != nil {
		return err
	}
	if o.backend == "user" {
		return runUserBackend(ctx, o, stdin, stdout, stderr)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	v := vm{options: o, ctx: ctx, home: home, out: stdout, run: commandRunner(ctx, stdin, stdout, stderr),
		withContext: func(ctx context.Context) runner { return commandRunner(ctx, stdin, stdout, stderr) }}
	if o.action != "render" {
		check := preflight
		if o.backend == "incus" {
			check = incusPreflight
		}
		if err := check(v.run, exec.LookPath, runtime.GOOS); err != nil {
			return err
		}
	}
	return v.execute()
}

var limaVersion = regexp.MustCompile(`(?m)^limactl version v?([0-9]+)\.([0-9]+)\.([0-9]+)([^\s]*)`)

func preflight(run runner, lookPath func(string) (string, error), goos string) error {
	if goos != "darwin" && goos != "linux" {
		return errors.New("dev-sandbox supports macOS and Linux hosts")
	}
	for _, name := range []string{"limactl", "ssh"} {
		if _, err := lookPath(name); err != nil {
			if name == "limactl" {
				return errors.New("Lima 2.2+ is required; on macOS run: brew install lima; see https://lima-vm.io/docs/installation/")
			}
			return errors.New("OpenSSH is required; install your operating system's OpenSSH client and ensure ssh is on PATH")
		}
	}
	output, err := run([]string{"limactl", "--version"}, nil, true)
	if err != nil {
		return err
	}
	m := limaVersion.FindStringSubmatch(output)
	if len(m) == 0 {
		return errors.New("could not determine Lima version; install Lima 2.2+ (stable)")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 2 || (major == 2 && minor < 2) || strings.HasPrefix(m[4], "-") {
		return errors.New("Lima 2.2+ (stable) is required; upgrade Lima before continuing")
	}
	// Ask OpenSSH to parse the required settings without making a connection.
	_, err = run([]string{"ssh", "-G", "-T", "-F", os.DevNull, "-o", "IdentityAgent=none", "-o", "ForwardAgent=no",
		"-o", "ControlPath=~/.ssh/control-%C", "-o", "ControlMaster=auto", "-o", "ControlPersist=60", "lima-check"}, nil, true)
	if err != nil {
		return fmt.Errorf("OpenSSH does not accept the required connection settings: %w", err)
	}
	return nil
}
