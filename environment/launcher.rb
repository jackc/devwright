# frozen_string_literal: true
# Lima creation/onboarding with Ruby's standard library; guest setup uses Bash.
require 'base64'
require 'digest'
require 'fileutils'
require 'io/console'
require 'json'
require 'open3'
require 'optparse'
require 'shellwords'
require 'tempfile'
require 'tmpdir'
require 'yaml'

module Environment
  ROOT = File.expand_path('..', __dir__)
  NAME = /\A[a-z][a-z0-9-]{0,39}\z/
  VARIABLE = /\A[A-Za-z_][A-Za-z0-9_]*\z/
  MARKER = '# lima-environment-v1: saved creation recipe, never updated in place.'
  module_function

  def run(*args)
    raise "Command failed: #{args.first}" unless system(*args)
  end

  def capture(*args, input: '')
    out, err, status = Open3.capture3(*args, stdin_data: input)
    raise "#{args.first} failed: #{err.strip}" unless status.success?
    out
  end

  def read(path)
    File.binread(File.expand_path(path, ROOT))
  end

  def config(path)
    defaults = YAML.safe_load(File.read(File.join(__dir__, 'config.yaml')))
    selected = YAML.safe_load(File.read(path)) || {}
    raise 'Recipe must be a mapping using only config.yaml keys' unless selected.is_a?(Hash) && (selected.keys - defaults.keys).empty?
    data = defaults.merge(selected)
    raise 'packages must contain apt package names' unless data['packages'].is_a?(Array) && data['packages'].all? { |p| p.is_a?(String) && p.match?(/\A[a-z0-9][a-z0-9+.:=-]*\z/) }
    raise 'environment must map variable names to scalar values' unless data['environment'].is_a?(Hash) && data['environment'].all? { |k, v| k.is_a?(String) && k.match?(VARIABLE) && [String, Integer, Float, TrueClass, FalseClass].any? { |type| v.is_a?(type) } && !v.to_s.include?("\0") }
    declarations(data['credentials'])
    raise 'A variable cannot be both an environment value and credential' unless (data['credentials'].map { |c| c['name'] } & data['environment'].keys).empty?
    %w[dotfiles_repo dotfiles_install codex_requirements codex_config claude_managed_settings claude_config system_script user_script].each do |key|
      raise "#{key} must be a string without NUL" unless data[key].is_a?(String) && !data[key].include?("\0")
    end
    installer = data['dotfiles_install']
    raise 'dotfiles_install must stay inside the repository' if installer.empty? || installer.start_with?('/') || installer.split('/').include?('..')
    raise 'Invalid dotfiles repository' if data['dotfiles_repo'].start_with?('-')
    %w[codex_requirements codex_config claude_managed_settings claude_config system_script user_script].each do |key|
      next if data[key].empty?
      file = File.expand_path(data[key], File.dirname(File.expand_path(path)))
      contents = File.read(file)
      if key.start_with?('claude')
        raise "#{key} must contain a JSON object" unless JSON.parse(contents).is_a?(Hash)
      elsif key.end_with?('_script')
        capture('bash', '-n', input: contents)
      end
      data[key] = file
    end
    data
  end

  def declarations(items)
    raise 'credentials must contain unique variable names and descriptions' unless items.is_a?(Array) && items.all? { |c| c.is_a?(Hash) && c.keys.sort == %w[description name] && c['name'].is_a?(String) && c['name'].match?(VARIABLE) && c['description'].is_a?(String) } && items.map { |c| c['name'] }.uniq.length == items.length
    items
  end

  def encoded(content)
    Base64.strict_encode64(content)
  end

  def materialize(content, target)
    "printf '%s' '#{encoded(content)}' | base64 -d > #{target}\n"
  end

  def setup(data)
    script = +"#!/bin/bash\nset -euo pipefail\ncd -- \"$(dirname -- \"$0\")\"\nexport DEBIAN_FRONTEND=noninteractive\n"
    script << materialize(read('lima/bootstrap.sh'), 'bootstrap.sh') << "/bin/bash bootstrap.sh\n"
    unless data['packages'].empty?
      script << "apt-get update -qq\napt-get install -y --no-install-recommends #{data['packages'].shelljoin}\n"
    end
    script << "export dotfiles_bundle=\"$PWD/dotfiles.bundle\"\n"
    unless data['dotfiles_repo'].empty?
      Dir.mktmpdir('lima-dotfiles-') do |dir|
        clone = File.join(dir, 'repository.git')
        bundle = File.join(dir, 'dotfiles.bundle')
        # Only this host operation uses personal Git authentication; logs go to stderr.
        capture('git', 'clone', '--bare', '--single-branch', '--', data['dotfiles_repo'], clone)
        capture('git', '-C', clone, 'bundle', 'create', bundle, '--all')
        script << materialize(File.binread(bundle), '"$dotfiles_bundle"')
      end
    end
    sources = {
      'DEFAULT_REQUIREMENTS' => 'config/codex/requirements.toml',
      'REQUIREMENTS' => data['codex_requirements'].empty? ? 'config/codex/requirements.toml' : data['codex_requirements'],
      'CONFIG' => data['codex_config'].empty? ? 'config/codex/config.toml' : data['codex_config'],
      'DEFAULT_MANAGED_SETTINGS' => 'config/claude/managed-settings.json',
      'MANAGED_SETTINGS' => data['claude_managed_settings'].empty? ? 'config/claude/managed-settings.json' : data['claude_managed_settings'],
      'CLAUDE_CONFIG' => data['claude_config'].empty? ? 'config/claude/settings.json' : data['claude_config'],
      'DOTFILES' => 'lima/dotfiles.sh', 'CREDENTIALS' => 'lima/credentials.sh'
    }
    provision = read('lima/provision.sh')
    sources.each { |key, file| provision = provision.gsub("__#{key}_B64__", encoded(read(file))) }
    provision = provision.gsub(/__VERIFY_(ARM64|AMD64)_B64__/, '')
    prefix = +"dotfiles_repository=#{data['dotfiles_repo'].shellescape}\ndotfiles_install=#{data['dotfiles_install'].shellescape}\n"
    prefix << "install_verifier=false\ncodex_policy_mode=#{data['codex_requirements'].empty? ? 'default' : 'custom'}\nclaude_policy_mode=#{data['claude_managed_settings'].empty? ? 'default' : 'custom'}\nreplace_codex_config=false\nreplace_claude_config=false\n"
    script << materialize(prefix + provision, 'base.sh') << "/bin/bash base.sh\n"
    environment = "# Non-secret values from the saved creation recipe.\n" + data['environment'].map { |key, value| "export #{key}=#{value.to_s.shellescape}\n" }.join
    user_setup = read('environment/setup-user.sh').sub('__ENVIRONMENT_B64__', encoded(environment))
    script << materialize(user_setup, '/usr/local/share/devwright/setup-environment.sh')
    script << "chmod 755 /usr/local/share/devwright/setup-environment.sh\nsudo -u dev -H env -i HOME=/home/dev USER=dev LOGNAME=dev PATH=/usr/local/bin:/usr/bin:/bin /bin/bash /usr/local/share/devwright/setup-environment.sh\n"
    %w[system user].each do |account|
      next if data["#{account}_script"].empty?
      target = "/usr/local/share/devwright/project-#{account}.sh"
      script << materialize(File.binread(data["#{account}_script"]), target) << "chmod 755 #{target}\n"
      script << if account == 'system'
                  "/bin/bash #{target}\n"
                else
                  ['sudo', '-u', 'dev', '-H', 'env', '-i', 'HOME=/home/dev', 'USER=dev', 'LOGNAME=dev', 'PATH=/usr/local/bin:/usr/bin:/bin', '/bin/bash', '-c', '. "$HOME/.config/devwright/environment.sh"; exec /bin/bash "$1"', 'bash', target].shelljoin + "\n"
                end
    end
    script
  end

  def render(data, resources = {})
    vm = YAML.safe_load(File.read(File.join(__dir__, 'lima.yaml')))
    resources.each { |key, value| vm[key] = key == 'cpus' ? value : "#{value}GiB" }
    script = read('environment/provision.sh').sub('__SETUP_B64__', encoded(setup(data)).scan(/.{1,76}/).join("\n"))
    vm['provision'] = [{ 'mode' => 'system', 'script' => script }]
    vm['message'] = JSON.generate({ 'environment_script_sha256' => Digest::SHA256.hexdigest(script),
                                  'environment_credentials' => data['credentials'] }).gsub('{{', '\u007b\u007b')
    vm
  end

  def check_instance(state, name, expected: nil)
    cfg = state.fetch('config')
    raise 'Unexpected instance identity or directory' unless state['name'] == name && state['dir'].is_a?(String) && state['dir'].start_with?('/') && !state['dir'].match?(/[\r\n\x00]/)
    ssh = cfg.fetch('ssh', {})
    user = cfg.fetch('user', {})
    raise 'Unexpected Lima mounts, forwarding, environment, or account; inspect global overrides' unless cfg['plain'] && [cfg['mounts'], cfg['portForwards'], cfg['env'], cfg['probes']].all? { |v| v.nil? || v.empty? } && !ssh['forwardAgent'] && !ssh['loadDotSSHPubKeys'] && !cfg['propagateProxyEnv'] && user['name'] == 'dev' && user['home'] == '/home/dev' && user['uid'] == 1000
    provisions = cfg.fetch('provision', [])
    raise 'Expected exactly one saved system provisioner' unless provisions.length == 1 && provisions[0]['mode'] == 'system'
    script = provisions[0].fetch('script')
    digest = JSON.parse(cfg.fetch('message'))['environment_script_sha256']
    raise 'Saved creation recipe is missing or modified' unless script.lines[1]&.chomp == MARKER && Digest::SHA256.hexdigest(script) == digest
    raise 'Lima overrides changed the selected recipe' if expected && (script != expected['provision'][0]['script'] || cfg['message'] != expected['message'])
    declarations(JSON.parse(cfg.fetch('message')).fetch('environment_credentials'))
    state
  end

  def info(name, expected: nil)
    rows = capture('limactl', 'list', '--json', name).lines.reject { |line| line.strip.empty? }.map { |line| JSON.parse(line) }
    raise "Expected exactly one VM named #{name}" unless rows.length == 1
    check_instance(rows.first, name, expected: expected)
  end

  def ssh_args(state, user, command)
    ['ssh', '-F', File.join(state['dir'], 'ssh.config'), '-o', 'IdentityAgent=none', '-o', 'ForwardAgent=no',
     '-o', 'ControlPath=~/.ssh/control-%C', '-o', 'ControlMaster=auto', '-o', 'ControlPersist=60',
     '-o', 'BatchMode=yes', '-l', user, "lima-#{state['name']}", command.shelljoin]
  end

  def verify(state)
    raise 'VM must be running' unless state['status'] == 'Running'
    run(*ssh_args(state, 'dev', ['/usr/local/share/devwright/verify']))
  end

  def finish(state)
    raise 'VM must be running' unless state['status'] == 'Running'
    arch = { 'aarch64' => 'arm64', 'x86_64' => 'amd64' }.fetch(state['arch'])
    archive = read("guestbin/verify-linux-#{arch}.gz")
    installer = <<~'SH'
      set -euo pipefail
      test -f /usr/local/share/devwright/lima-setup-complete
      tmp=$(mktemp /usr/local/share/devwright/verify.XXXXXXXX)
      trap 'rm -f -- "$tmp"' EXIT
      gzip -dc > "$tmp"
      chmod 755 "$tmp"
      mv -fT "$tmp" /usr/local/share/devwright/verify
    SH
    capture(*ssh_args(state, 'root', ['/bin/bash', '-c', installer]), input: archive)
    verify(state)
    install_ssh(state)
    credentials(state)
    puts 'Ready. Complete agent account sign-ins inside the guest as needed.'
  end

  def ssh_config(state)
    path = File.join(state['dir'], 'ssh.config').gsub(/([\\"])/) { |s| "\\#{s}" }
    "# Generated by environment/dev\nHost lima-#{state['name']}\n  IdentityAgent none\n  ForwardAgent no\n  ControlMaster auto\n  ControlPath ~/.ssh/control-%C\n  ControlPersist 60\n  Include \"#{path}\"\n\nHost *\n"
  end

  def atomic(path, content)
    Tempfile.create('.environment-', File.dirname(path)) do |file|
      file.write(content)
      file.close
      File.rename(file.path, path)
    end
  end

  def install_ssh(state, home: Dir.home)
    directory = File.join(home, '.ssh/lima-environments')
    FileUtils.mkdir_p(directory, mode: 0o700)
    target = File.join(directory, "#{state['name']}.config")
    main = File.join(home, '.ssh/config')
    include_line = 'Include "~/.ssh/lima-environments/*.config"'
    raise 'Refusing to replace unmanaged SSH config' if File.symlink?(target) || (File.exist?(target) && !File.read(target).start_with?("# Generated by environment/dev\n"))
    old = File.exist?(main) ? File.read(main) : ''
    missing = !old.lines.map(&:chomp).include?(include_line)
    raise "Add #{include_line} to the source of your SSH config symlink" if missing && File.symlink?(main)
    atomic(target, ssh_config(state))
    if missing
      Tempfile.create('config.before-lima-environments-', File.dirname(main)) { |file| file.write(old); file.close; FileUtils.cp(file.path, "#{file.path}.backup") } if File.exist?(main)
      atomic(main, "#{include_line}\n\n#{old}")
    end
    puts "SSH ready: ssh lima-#{state['name']} (dev); ssh root@lima-#{state['name']} (admin)"
  end

  def credential_script(values)
    script = +"#!/bin/bash\nset -euo pipefail\numask 077\ntest \"$(id -un)\" = dev\ndirectory=\"$HOME/.config/devwright/credentials.d\"\nmkdir -p \"$directory\"\nchmod 700 \"$directory\"\n"
    values.each do |name, value|
      raise 'Invalid credential name or value' unless name.match?(VARIABLE) && !value.empty? && !value.include?("\0")
      script << "tmp=$(mktemp \"$directory/.credential.XXXXXXXX\")\ntrap 'rm -f -- \"$tmp\"' EXIT\n"
      script << materialize("export #{name}=#{value.shellescape}\n", '"$tmp"')
      script << "mv -fT \"$tmp\" \"$directory/#{name}.sh\"\n"
    end
    script
  end

  def credentials(state, replace: false)
    raise 'VM must be running' unless state['status'] == 'Running'
    items = declarations(JSON.parse(state['config']['message']).fetch('environment_credentials'))
    return if items.empty?
    names = items.map { |c| c['name'] }
    probe = 'for name in "$@"; do if [[ -n "${!name:-}" ]]; then printf "%s\n" "$name"; fi; done'
    present = capture(*ssh_args(state, 'dev', ['/bin/bash', '-c', probe, 'bash', *names])).lines.map(&:chomp)
    values = {}
    items.each do |item|
      next if !replace && present.include?(item['name'])
      raise "Missing #{item['name']}; run environment/dev credentials #{state['name']} in a terminal" unless IO.console
      values[item['name']] = IO.console.getpass("#{item['name']} — #{item['description']}: ")
    end
    unless values.empty?
      capture(*ssh_args(state, 'dev', ['/bin/bash', '-s']), input: credential_script(values))
      puts 'Credentials installed. Reconnect shells and remote runtimes to load new values.'
    end
  end

  def main(argv)
    options = { config: File.join(__dir__, 'config.yaml'), resources: {} }
    parser = OptionParser.new do |p|
      p.banner = 'Usage: environment/dev {create|finish|verify|credentials|ssh-config|install-ssh|render} NAME [options]'
      p.on('--config FILE', 'Creation recipe (create/render only)') { |v| options[:config] = v; options[:custom_config] = true }
      %w[cpus memory disk].each { |key| p.on("--#{key} INTEGER", Integer, 'Creation resource override; sizes in GiB') { |v| options[:resources][key] = v } }
      p.on('--replace', 'Prompt again for declared credentials') { options[:replace] = true }
      p.on('-h', '--help') { puts p; return }
    end
    parser.parse!(argv)
    action, name = argv
    raise 'configure has been removed: change the recipe and create a new VM' if action == 'configure'
    raise parser.to_s unless %w[create finish verify credentials ssh-config install-ssh render].include?(action) && argv.length == 2 && name.match?(NAME)
    raise 'Resource/config options apply only to create/render' if !%w[create render].include?(action) && (options[:custom_config] || !options[:resources].empty?)
    raise 'Resources must be positive' unless options[:resources].values.all?(&:positive?)
    raise '--replace applies only to credentials' if options[:replace] && action != 'credentials'
    if %w[create render].include?(action)
      vm = render(config(options[:config]), options[:resources])
      if action == 'render'
        puts YAML.dump(vm)
        return
      end
      %w[arm64 amd64].each do |arch|
        raise 'Build verifiers first: mise run guest' unless File.file?(File.join(ROOT, "guestbin/verify-linux-#{arch}.gz"))
      end
      Tempfile.create(['lima-environment-', '.yaml']) do |file|
        file.write(YAML.dump(vm)); file.flush
        run('limactl', 'validate', file.path)
        run('limactl', 'create', '--tty=false', "--name=#{name}", file.path)
        info(name, expected: vm) # Reject global overrides before any guest script executes.
        run('limactl', 'start', '--tty=false', name)
        state = info(name, expected: vm)
        finish(state)
      end
      return
    end
    state = info(name)
    case action
    when 'finish' then finish(state)
    when 'verify' then verify(state)
    when 'credentials' then credentials(state, replace: options[:replace])
    when 'ssh-config' then puts ssh_config(state)
    when 'install-ssh' then install_ssh(state)
    end
  end
end

if $PROGRAM_NAME == __FILE__
  begin
    Environment.main(ARGV)
  rescue StandardError => e
    warn "Error: #{e.message}"
    exit 1
  end
end
