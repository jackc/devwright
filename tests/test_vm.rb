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
    assert_equal 'vmadmin', @vm.render.dig('user', 'name')
    assert_equal true, @vm.render['plain']
    assert_empty @vm.render['provision']
  end

  def test_refuses_foreign_or_unsafe_instances
    @vm.check_instance(@state)
    { 'plain' => false, 'mounts' => [{ 'location' => '~' }],
      'ssh' => { 'forwardAgent' => true }, 'provision' => [{ 'mode' => 'system', 'script' => 'true' }], 'user' => { 'name' => 'dev' } }.each do |key, value|
      assert_raises(RuntimeError) { @vm.check_instance(@state.merge('config' => @vm.render.merge(key => value))) }
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
    assert_includes args, 'ControlPath=none'
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
      assert_raises(RuntimeError) { @vm.execute('start', 'test; false') }
    end
  end

  def test_ssh_migration_preserves_config_and_refreshes_port
    Dir.mktmpdir do |home|
      vm = DevelopmentVM.new(home: home)
      dir = File.join(home, '.ssh/agent-vms')
      FileUtils.mkdir_p(dir)
      target = File.join(dir, 'test.config')
      File.write(target, DevelopmentVM::OLD_HEADER + 'old alias')
      config = File.join(home, '.ssh/config')
      original = "Host personal\n  HostName example.invalid\n"
      File.write(config, original)
      vm.stub(:ssh_config, "Host test\n  Port 1234\n") do
        capture_io { 2.times { vm.install_ssh(@state, add_include: true) } }
      end
      assert File.read(target).start_with?(DevelopmentVM::HEADER)
      assert_equal 1, File.read(config).lines.count { |line| line.chomp == DevelopmentVM::INCLUDE }
      assert File.read(config).end_with?(original)
      backups = Dir.glob(config + '.before-agent-vms-*')
      assert_equal 1, backups.length
      assert_equal original, File.read(backups.first)
      vm.stub(:ssh_config, "Host test\n  Port 5678\n") { vm.install_ssh(@state) }
      assert_includes File.read(target), '5678'
      assert_equal 0o600, File.stat(target).mode & 0o777
    end
  end

  def test_refuses_unmanaged_ssh_file
    Dir.mktmpdir do |home|
      target = File.join(home, '.ssh/agent-vms/test.config')
      FileUtils.mkdir_p(File.dirname(target))
      File.write(target, 'personal content')
      assert_raises(RuntimeError) { DevelopmentVM.new(home: home).install_ssh(@state, add_include: true) }
      assert_equal 'personal content', File.read(target)
    end
  end

  def test_configure_uses_admin_stdin_then_verifies_without_restarting
    calls = []
    @vm.stub(:info, @state) do
      @vm.stub(:run, ->(argv, **options) { calls << [argv, options] }) do
        @vm.stub(:install_ssh, nil) { capture_io { @vm.execute('configure', 'test') } }
      end
    end
    assert_equal 3, calls.length
    args, options = calls.first
    assert_equal ['-l', 'vmadmin', 'lima-test'], args[-4, 3]
    assert_equal %w[sudo -n /bin/bash -s], Shellwords.split(args.last)
    assert_equal @vm.provision, options.fetch(:input)
    assert_equal %w[ruby /usr/local/share/agent-vm/verify.rb], Shellwords.split(calls[1][0].last)
    assert_equal %w[test -f /usr/local/share/agent-vm/managed], Shellwords.split(calls.last[0].last)
  end

  def test_configure_starts_a_stopped_vm_before_setup
    calls = []
    @vm.stub(:info, @state.merge('status' => 'Stopped')) do
      @vm.stub(:run, ->(argv, **options) { calls << argv }) do
        @vm.stub(:install_ssh, nil) { capture_io { @vm.execute('configure', 'test') } }
      end
    end
    assert_equal ['limactl', 'start', '--tty=false', 'test'], calls.first
    assert_equal %w[sudo -n /bin/bash -s], Shellwords.split(calls[1].last)
  end

  def test_start_does_not_provision
    calls = []
    @vm.stub(:info, @state) do
      @vm.stub(:configure, ->(*) { flunk 'Start ran provisioning' }) do
        @vm.stub(:run, ->(argv) { calls << argv }) do
          @vm.stub(:install_ssh, nil) { capture_io { @vm.execute('start', 'test') } }
        end
      end
    end
    assert_equal ['limactl', 'start', '--tty=false', 'test'], calls.first
    assert_equal 2, calls.length
  end

  def test_failed_provisioning_does_not_report_readiness
    @vm.stub(:info, @state) do
      @vm.stub(:remote, ->(*) { raise 'provision failed' }) do
        @vm.stub(:ready, ->(*) { flunk 'Failed configuration reported ready' }) do
          assert_raises(RuntimeError) { @vm.execute('configure', 'test') }
        end
      end
    end
  end
end
