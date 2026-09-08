# frozen_string_literal: true
# Creates one disposable native-parity-* VM. No test framework gems required.
require_relative 'launcher'

name = ARGV.fetch(0)
raise 'Use a fresh native-parity-* name' unless name.match?(/\Anative-parity-[a-z0-9-]+\z/)
Dir.mktmpdir('native-parity-fixtures-') do |dir|
  dotfiles = File.join(dir, 'dotfiles')
  FileUtils.mkdir_p(dotfiles)
  File.write(File.join(dotfiles, 'install'), "#!/bin/bash\nset -eu\nprintf '%s\\n' \"$USER\" > \"$HOME/.parity-dotfiles\"\nprintf 'return 0\\n' > \"$HOME/.profile\"\n")
  File.chmod(0o755, File.join(dotfiles, 'install'))
  Environment.run('git', 'init', '-q', dotfiles)
  Environment.run('git', '-C', dotfiles, 'add', 'install')
  Environment.run('git', '-C', dotfiles, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid', 'commit', '-qm', 'Test installer')
  File.write(File.join(dir, 'system.sh'), "#!/bin/bash\nset -eu\necho once >> /usr/local/share/devwright/project-runs\n")
  File.write(File.join(dir, 'user.sh'), "#!/bin/bash\nset -eu\ntest \"$APP_ENV\" = 'literal {{braces}} $HOME'\necho keep > \"$HOME/projects/preserve\"\n")
  requirements = File.join(dir, 'requirements.toml')
  File.write(requirements, Environment.read('config/codex/requirements.toml') + "\n# Native parity policy\n")
  path = File.join(dir, 'config.yaml')
  File.write(path, YAML.dump({ 'packages' => ['tree'], 'dotfiles_repo' => dotfiles,
    'environment' => { 'APP_ENV' => 'literal {{braces}} $HOME' },
    'system_script' => 'system.sh', 'user_script' => 'user.sh', 'codex_requirements' => requirements,
    'credentials' => [{ 'name' => 'NATIVE_PARITY_TOKEN', 'description' => 'Synthetic {{test}} credential' }] }))
  # A missing credential fails unattended onboarding, after the guest is provisioned.
  out, err, status = Open3.capture3(File.join(__dir__, 'dev'), 'create', name, '--config', path)
  puts out
  warn err
  raise 'Expected missing-credential onboarding failure' if status.success? || !err.include?('Missing NATIVE_PARITY_TOKEN')
  state = Environment.info(name)
  values = { 'NATIVE_PARITY_TOKEN' => "first '$HOME' value" }
  Environment.capture(*Environment.ssh_args(state, 'dev', ['/bin/bash', '-s']), input: Environment.credential_script(values))
  values['NATIVE_PARITY_TOKEN'] = "second '$HOME' value"
  Environment.capture(*Environment.ssh_args(state, 'dev', ['/bin/bash', '-s']), input: Environment.credential_script(values))
  Environment.finish(state) # Checks existing credentials without a terminal.
  puts 'PASS fresh provisioning, missing-credential recovery, rotation, and finish'

  check = <<~'SH'
    set -euo pipefail
    test "$APP_ENV" = 'literal {{braces}} $HOME'
    test "$NATIVE_PARITY_TOKEN" = "second '\$HOME' value"
    test "$(cat "$HOME/.parity-dotfiles")" = dev
    test "$(cat "$HOME/projects/preserve")" = keep
    test -x /usr/bin/tree
    ! grep -q first "$HOME/.config/devwright/credentials.d/NATIVE_PARITY_TOKEN.sh"
    test "$(stat -c %a "$HOME/.config/devwright/credentials.d/NATIVE_PARITY_TOKEN.sh")" = 600
    grep -q 'Native parity policy' /etc/codex/requirements.toml
  SH
  Environment.run(*Environment.ssh_args(state, 'dev', ['/bin/bash', '-c', check]))
  Environment.run(*Environment.ssh_args(state, 'root', ['test', '-f', '/root/.parity-dotfiles']))
  root_check = 'test "$(wc -l < /usr/local/share/devwright/project-runs)" = 1'
  Environment.run(*Environment.ssh_args(state, 'root', ['/bin/bash', '-c', root_check]))
  completed = Environment.capture(*Environment.ssh_args(state, 'root', ['cat', '/usr/local/share/devwright/lima-setup-complete']))
  # Changing source files cannot affect the saved creation recipe or rerun setup.
  File.write(File.join(dir, 'system.sh'), "#!/bin/bash\nexit 99\n")
  File.write(path, YAML.dump({ 'packages' => ['not-a-real-package'] }))
  Environment.run('limactl', 'stop', name)
  Environment.run('limactl', 'start', '--tty=false', name)
  state = Environment.info(name)
  Environment.verify(state)
  Environment.run(*Environment.ssh_args(state, 'dev', ['/bin/bash', '-c', check]))
  Environment.run(*Environment.ssh_args(state, 'root', ['/bin/bash', '-c', root_check]))
  raise 'Setup reran on restart' unless completed == Environment.capture(*Environment.ssh_args(state, 'root', ['cat', '/usr/local/share/devwright/lima-setup-complete']))
  Environment.run('ssh', "lima-#{name}", 'test "$(id -un)" = dev')
  puts 'PASS restart skips setup, retains original recipe and user state, and keeps SSH alias working'
end
