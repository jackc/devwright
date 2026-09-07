package devwright

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

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
	set                                                       map[string]bool
}

var validName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
var validSize = regexp.MustCompile(`^[1-9][0-9]*(MiB|GiB|TiB)$`)

// newCLI constructs fresh commands and flag storage for each invocation.
func newCLI(version string, execute func(context.Context, options) error) *cobra.Command {
	o := options{name: "dev", set: map[string]bool{}}
	root := &cobra.Command{
		Use:           "devwright",
		Short:         "Set up development environments with Lima, Incus, or native users",
		Long:          "Set up development environments with Lima, Incus, or native users.\n\nDefault environment: dev. Command-specific options follow the command; --backend may appear before or after it.\nUse limactl or incus start/stop/delete for lifecycle; ssh lima-NAME or incus-NAME for development.\nIncus uses the local server's default project. Repeat --backend incus on every action.",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("an action is required; use --help\n%s", cmd.UsageString())
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	// Only options shared by all environment commands are persistent.
	f := root.PersistentFlags()
	f.BoolP("help", "h", false, "Show help for the command")
	f.StringVar(&o.backend, "backend", "lima", "Boundary manager: lima, incus, or user")
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the CLI/embedded recipe version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "devwright %s\n", version)
			return err
		},
	})
	for _, action := range []struct{ name, short string }{
		{"render", "Render the environment configuration"},
		{"create", "Create and configure an environment"},
		{"configure", "Configure an existing environment"},
		{"verify", "Verify the environment"},
		{"ssh-config", "Print SSH configuration"},
		{"install-ssh", "Install SSH configuration"},
		{"list", "List environments (user backend only)"},
		{"shell", "Open an environment shell (user backend only)"},
		{"delete", "Delete an explicitly named environment (user backend only)"},
	} {
		use := action.name + " [NAME]"
		if action.name == "delete" {
			use = "delete NAME"
		} else if action.name == "list" {
			use = "list"
		}
		cmd := &cobra.Command{
			Use: use, Short: action.short,
			Args: cobra.MaximumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				o.action = cmd.Name()
				if len(args) == 1 {
					o.name = args[0]
				}
				cmd.Flags().Visit(func(f *pflag.Flag) { o.set[f.Name] = true })
				validated, err := validateOptions(o, append([]string{o.action}, args...))
				if err != nil {
					return err
				}
				return execute(cmd.Context(), validated)
			},
		}
		addCommandFlags(cmd.Flags(), action.name, &o)
		root.AddCommand(cmd)
	}
	return root
}

func addCommandFlags(f *pflag.FlagSet, action string, o *options) {
	switch action {
	case "create", "configure", "render":
		f.IntVar(&o.sshPort, "ssh-port", 22, "Existing system SSH port (user backend only)")
	}
	switch action {
	case "delete":
		f.BoolVar(&o.removeHome, "remove-home", false, "Delete the managed home instead of archiving it (user backend only)")
	}
	switch action {
	case "create", "render":
		f.BoolVar(&o.container, "container", false, "Create an Incus system container instead of a VM")
		f.StringVar(&o.network, "network", "incusbr0", "Incus managed network")
		f.StringVar(&o.storage, "storage", "default", "Incus storage pool")
		f.IntVar(&o.cpus, "cpus", 0, "CPUs for a new VM (default: 4)")
		f.StringVar(&o.memory, "memory", "", "Memory for a new VM, e.g. 8GiB (default: 4GiB)")
		f.StringVar(&o.disk, "disk", "", "Disk for a new VM, e.g. 100GiB (default: 60GiB)")
	}
	switch action {
	case "create", "configure":
		f.StringVar(&o.dotfilesRepo, "dotfiles-repo", "", "Install this Git repository for root and dev")
		f.StringVar(&o.dotfilesInstall, "dotfiles-install", "install", "Installer relative to the repository")
		f.StringVar(&o.codexRequirements, "codex-requirements", "", "Use and remember a custom managed policy")
		f.StringVar(&o.codexConfig, "codex-config", "", "Initial dev config; existing config is preserved")
		f.StringVar(&o.claudeManagedSettings, "claude-managed-settings", "", "Use and remember a custom managed Claude Code policy")
		f.StringVar(&o.claudeConfig, "claude-config", "", "Initial dev Claude Code settings; existing settings are preserved")
	}
	switch action {
	case "configure":
		f.BoolVar(&o.resetCodexRequirements, "reset-codex-requirements", false, "Restore the embedded managed policy")
		f.BoolVar(&o.replaceCodexConfig, "replace-codex-config", false, "Replace existing dev config with --codex-config")
		f.BoolVar(&o.resetClaudeManagedSettings, "reset-claude-managed-settings", false, "Restore the embedded managed Claude Code policy")
		f.BoolVar(&o.replaceClaudeConfig, "replace-claude-config", false, "Replace existing dev Claude Code settings with --claude-config")
	}
}

func parseOptions(args []string) (options, error) {
	var parsed options
	cmd := newCLI("dev", func(_ context.Context, o options) error {
		parsed = o
		return nil
	})
	cmd.SetArgs(args)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil && parsed.action == "" {
		err = errors.New("an executable action is required")
	}
	return parsed, err
}

func validateOptions(o options, positional []string) (options, error) {
	switch o.action {
	case "list", "shell", "delete":
		if o.backend != "user" {
			return o, fmt.Errorf("%s applies only to --backend user", o.action)
		}
	}
	if !validName.MatchString(o.name) {
		return o, errors.New("Environment name must start with a letter and contain only lowercase letters, digits, and hyphens (maximum 40 characters)")
	}
	if o.backend != "lima" && o.backend != "incus" && o.backend != "user" {
		return o, errors.New("--backend must be lima, incus, or user")
	}
	if o.backend == "user" {
		if len(o.name) > 22 {
			return o, errors.New("user environment names have a maximum of 22 characters")
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
	cmd := newCLI(version, func(ctx context.Context, o options) error {
		return runOptions(ctx, o, stdin, stdout, stderr)
	})
	cmd.SetArgs(args)
	cmd.SetIn(stdin)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	return cmd.ExecuteContext(ctx)
}

func runOptions(ctx context.Context, o options, stdin io.Reader, stdout, stderr io.Writer) error {
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
		hostGit:     commandRunnerEnvironment(ctx, stdin, stdout, stderr, os.Environ),
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
		return errors.New("devwright supports macOS and Linux hosts")
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
