# frozen_string_literal: true
require 'minitest/autorun'
require 'minitest/mock'
require 'tmpdir'
require 'rbconfig'
require_relative '../scripts/vm'

class DevelopmentVMTest < Minitest::Test
  def setup
    @vm = DevelopmentVM.new
    @state = { 'name' => 'test', 'dir' => '/tmp/a path', 'status' => 'Running', 'config' => @vm.render }
  end

  def test_render_resolves_payloads
    text = @vm.provision
    refute_match(/_B64__|__CODEX_VERSION__/, text)
    assert_equal 'dev', @vm.render.dig('user', 'name')
    assert_equal true, @vm.render['plain']
    assert_empty @vm.render['provision']
  end

  def test_refuses_foreign_or_unsafe_instances
    @vm.check_instance(@state)
    { 'plain' => false, 'mounts' => [{ 'location' => '~' }],
      'ssh' => { 'forwardAgent' => true }, 'provision' => [{ 'mode' => 'system', 'script' => 'true' }], 'user' => { 'name' => 'root' } }.each do |key, value|
      assert_raises(RuntimeError) { @vm.check_instance(@state.merge('config' => @vm.render.merge(key => value))) }
    end
  end

  def test_dotfiles_options_are_optional_and_shell_escaped
    refute_includes @vm.provision, 'jackc'
    configured = DevelopmentVM.new(dotfiles_repo: "https://example.com/a'$(false).git", dotfiles_install: 'setup script')
    assignments = configured.provision.lines.first(2).join
    output, error, status = Open3.capture3('/bin/bash', '-c', assignments + 'printf "%s\\n" "$dotfiles_repository" "$dotfiles_install"')
    assert status.success?, error
    assert_equal ["https://example.com/a'$(false).git", 'setup script'], output.lines.map(&:chomp)
    assert_raises(RuntimeError) { configured.execute('verify', 'test') }
    assert_raises(RuntimeError) { DevelopmentVM.new(dotfiles_install: '../install') }
  end

  def test_dotfiles_installer_runs_in_independent_checkouts_and_can_repeat
    script = File.read(File.join(DevelopmentVM::ROOT, 'lima/dotfiles.sh'))
    user_setup = script.split("<<'USER_SETUP'\n", 2).last.split("\nUSER_SETUP", 2).first
    Dir.mktmpdir do |directory|
      source = File.join(directory, 'source')
      FileUtils.mkdir_p(source)
      installer = File.join(source, 'custom setup')
      File.write(installer, <<~'INSTALL')
        #!/bin/bash
        set -eu
        test -f './custom setup'
        printf '%s\n' "$HOME" >> "$HOME/installed"
        git config --global credential.helper 'cache --timeout=7200'
      INSTALL
      File.chmod(0o755, installer)
      @vm.run(['git', 'init', '-q', source], capture: true)
      @vm.run(['git', '-C', source, 'add', '.'], capture: true)
      @vm.run(['git', '-C', source, '-c', 'user.name=Test', '-c', 'user.email=test@example.invalid',
               '-c', 'commit.gpgsign=false', 'commit', '-qm', 'fixture'], capture: true)
      %w[root dev].each do |account|
        home = File.join(directory, account)
        FileUtils.mkdir_p(home)
        env = { 'HOME' => home, 'PATH' => ENV.fetch('PATH'), 'GIT_CONFIG_NOSYSTEM' => '1' }
        2.times do
          output, error, status = Open3.capture3(env, '/bin/bash', '-s', '--', source, 'custom setup',
                                                stdin_data: user_setup, unsetenv_others: true)
          assert status.success?, output + error
        end
        assert_equal [home, home], File.readlines(File.join(home, 'installed'), chomp: true)
        refute File.exist?(File.join(home, '.zshenv'))
        output, error, status = Open3.capture3(env, 'git', 'config', '--global', '--get-all',
                                              'credential.https://github.com.helper', unsetenv_others: true)
        assert status.success?, error
        assert_equal "\n!/usr/local/bin/gh auth git-credential\n", output
      end
    end
  end

  def test_environment_removal_is_effective_in_child
    old = ENV['GH_TOKEN']
    ENV['GH_TOKEN'] = 'synthetic-token'
    output = @vm.run([RbConfig.ruby, '-e', 'puts ENV.fetch("GH_TOKEN", "absent")'], capture: true)
    assert_equal "absent\n", output
    assert_equal 'synthetic-token', ENV['GH_TOKEN']
    assert_equal %w[GH_TOKEN GITHUB_TOKEN OPENAI_API_KEY SSH_AUTH_SOCK], @vm.environment.keys.sort
    assert @vm.environment.values.all?(&:nil?)
  ensure
    ENV['GH_TOKEN'] = old
  end

  def test_subprocess_arguments_are_not_shell_code
    values = ['a path with spaces', '$(false)', '`false`', 'one; two', "quote'and\"double"]
    output = @vm.run([RbConfig.ruby, '-rjson', '-e', 'puts JSON.generate(ARGV)', *values], capture: true)
    assert_equal values, JSON.parse(output)
  end

  def test_command_failure_stops_execution_without_dumping_arguments
    error = assert_raises(RuntimeError) do
      @vm.run([RbConfig.ruby, '-e', 'exit 7', 'synthetic-token'], capture: true)
    end
    assert_includes error.message, 'exit 7'
    refute_includes error.message, 'synthetic-token'
  end

  def test_ssh_separates_identities_and_quotes_remote_arguments
    args = @vm.ssh_args(@state)
    assert_includes args, 'ControlPath=~/.ssh/control-%C'
    assert_includes args, 'IdentityAgent=none'
    assert_equal ['-l', 'dev', 'lima-test'], args.last(3)
    command = ['echo', "a'; $(false)", 'a path']
    @vm.stub(:run, ->(argv) { assert_equal command, Shellwords.split(argv.last) }) do
      @vm.remote(@state, command)
    end
  end

  def test_token_goes_only_on_stdin
    @vm.stub(:info, @state) do
      @vm.stub(:read_token, 'synthetic-token') do
        @vm.stub(:run, lambda { |argv, **options|
          refute_includes argv.join(' '), 'synthetic-token'
          assert_equal "synthetic-token\n", options.fetch(:input)
        }) { capture_io { @vm.execute('set-token', 'test') } }
      end
    end
  end

  def test_invalid_name_never_runs_commands
    @vm.stub(:run, ->(*) { flunk 'Executed a command for invalid input' }) do
      assert_raises(RuntimeError) { @vm.execute('verify', 'test; false') }
    end
  end

  def test_ssh_install_preserves_personal_config
    Dir.mktmpdir do |home|
      vm = DevelopmentVM.new(home: home)
      dir = File.join(home, '.ssh/agent-vms')
      FileUtils.mkdir_p(dir)
      target = File.join(dir, 'test.config')
      File.write(target, DevelopmentVM::HEADER + 'old alias')
      config = File.join(home, '.ssh/config')
      original = "Host personal\n  HostName example.invalid\n"
      File.write(config, original)
      vm.stub(:ssh_config, "Host test\n  Port 1234\n") do
        capture_io { 2.times { vm.install_ssh(@state) } }
      end
      assert File.read(target).start_with?(DevelopmentVM::HEADER)
      assert_equal 1, File.read(config).lines.count { |line| line.chomp == DevelopmentVM::INCLUDE }
      assert File.read(config).end_with?(original)
      backups = Dir.glob(config + '.before-agent-vms-*')
      assert_equal 1, backups.length
      assert_equal original, File.read(backups.first)
      assert_equal 0o600, File.stat(target).mode & 0o777
    end
  end

  def test_refuses_unmanaged_ssh_file
    Dir.mktmpdir do |home|
      target = File.join(home, '.ssh/agent-vms/test.config')
      FileUtils.mkdir_p(File.dirname(target))
      File.write(target, 'personal content')
      assert_raises(RuntimeError) { DevelopmentVM.new(home: home).install_ssh(@state) }
      assert_equal 'personal content', File.read(target)
    end
  end

  def test_bootstrap_establishes_root_access_before_configuration
    calls = []
    @vm.stub(:remote, ->(*args, **options) { calls << [args, options] }) { @vm.bootstrap(@state) }
    assert_equal %w[sudo -n /bin/bash -s], calls[0][0][1]
    assert_equal 'dev', calls[0][0][2]
    assert_equal File.read(File.join(DevelopmentVM::ROOT, 'lima/bootstrap.sh')), calls[0][1][:input]
    assert_equal %w[test -s /root/.ssh/authorized_keys], calls[1][0][1]
    assert_equal 'root', calls[1][0][2]
  end

  def test_configure_uses_admin_stdin_then_verifies_without_restarting
    calls = []
    @vm.stub(:info, @state) do
      @vm.stub(:run, ->(argv, **options) { calls << [argv, options] }) do
        capture_io { @vm.execute('configure', 'test') }
      end
    end
    assert_equal 2, calls.length
    args, options = calls.first
    assert_equal ['-l', 'root', 'lima-test'], args[-4, 3]
    assert_equal %w[/bin/bash -s], Shellwords.split(args.last)
    assert_equal @vm.provision, options.fetch(:input)
    assert_equal %w[ruby /usr/local/share/agent-vm/verify.rb], Shellwords.split(calls[1][0].last)
  end

  def test_configure_starts_a_stopped_vm_before_setup
    calls = []
    @vm.stub(:info, @state.merge('status' => 'Stopped')) do
      @vm.stub(:run, ->(argv, **options) { calls << argv }) do
        capture_io { @vm.execute('configure', 'test') }
      end
    end
    assert_equal ['limactl', 'start', '--tty=false', 'test'], calls.first
    assert_equal %w[/bin/bash -s], Shellwords.split(calls[1].last)
  end

  def test_removed_commands_never_run
    @vm.stub(:run, ->(*) { flunk 'Removed command ran a subprocess' }) do
      %w[start shell admin].each do |action|
        assert_raises(RuntimeError) { @vm.execute(action, 'test') }
      end
    end
  end

  def test_failed_provisioning_does_not_verify
    calls = []
    @vm.stub(:info, @state) do
      @vm.stub(:remote, ->(*args, **options) { calls << args; raise 'provision failed' }) do
        assert_raises(RuntimeError) { @vm.execute('configure', 'test') }
      end
    end
    assert_equal 1, calls.length
  end

  def test_ssh_uses_current_lima_config_and_separates_users
    Dir.mktmpdir('ssh fixture ') do |dir|
      state = @state.merge('dir' => dir)
      wrapper = File.join(dir, 'wrapper.config')
      File.write(wrapper, @vm.ssh_config(state))
      sockets = []
      [1234, 5678].each do |port|
        File.write(File.join(dir, 'ssh.config'), <<~CONFIG)
          Host lima-test
            HostName 127.0.0.1
            Port #{port}
            User dev
            ControlMaster auto
            ControlPath /tmp/admin-socket
        CONFIG
        ['lima-test', 'root@lima-test'].each do |destination|
          output = @vm.run(['ssh', '-G', '-T', '-F', wrapper, destination], capture: true)
          options = output.lines.to_h { |line| line.strip.split(' ', 2) }
          assert_equal port.to_s, options['port']
          assert_equal(destination.include?('@') ? 'root' : 'dev', options['user'])
          assert_equal 'auto', options['controlmaster']
          assert_equal '60', options['controlpersist']
          sockets << options.fetch('controlpath')
          assert_equal 'none', options['identityagent']
          assert_equal 'no', options['forwardagent']
        end
      end
      assert_equal 4, sockets.uniq.length, 'Different users and ports must have distinct sockets'
    end
  end
end
