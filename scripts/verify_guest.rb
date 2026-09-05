#!/usr/bin/env ruby
# frozen_string_literal: true

# Run through a real dev SSH session. Never reads real credential contents.
require 'digest'
require 'etc'
require 'tmpdir'
require_relative 'check_codex'

module GuestVerification
  def self.check
    Verification.assert(Etc.getpwuid.name == 'dev', 'Must run as dev')
    Verification.assert(!ENV.key?('SSH_AUTH_SOCK'), 'Agent socket was forwarded')
    Verification.assert(!Verification.run(['sudo', '-n', 'true']).last.success?, 'dev has sudo access')
    Verification.assert((Process.groups - [Etc.getpwnam('dev').gid]).empty?, 'Unexpected supplementary groups')
    %w[/home/jack /root].each do |path|
      begin
        Dir.children(path)
      rescue Errno::EACCES
        next
      end
      raise "Administrator home accessible: #{path}"
    end
    %w[/etc/codex /etc/codex/requirements.toml /usr/local/bin /usr/local/share/agent-vm].each do |path|
      Verification.assert(File.stat(path).uid.zero? && !File.writable?(path), "Writable policy/tool path: #{path}")
    end
    mounts = File.read('/proc/mounts')
    Verification.assert(%w[virtiofs 9p fuse.sshfs].none? { |type| mounts.include?(type) }, 'Unexpected shared filesystem')
    Verification.assert(!File.exist?('/run/host-services/ssh-auth.sock'), 'Host agent socket is present')
    Verification.assert(!(File.readable?('/var/run/docker.sock') && File.writable?('/var/run/docker.sock')), 'Docker socket accessible')
    # Compare the exact provisioned policy, without adding a TOML parser dependency.
    # check_codex validates that Codex actually loads its managed profile/features.
    expected = File.read('/usr/local/share/agent-vm/requirements.sha256').split.first
    Verification.assert(Digest::SHA256.file('/etc/codex/requirements.toml').hexdigest == expected, 'Managed policy differs from provisioned recipe')
    version = File.read('/usr/local/share/agent-vm/codex-version').strip
    output, error, status = Verification.run(['codex', '--version'])
    Verification.assert(status.success? && output.strip == "codex-cli #{version}", error)
    puts 'PASS Linux account, policy ownership, mounts, SSH forwarding, and Codex installation'
    CodexPolicyCheck.check
    sandbox_check
    puts 'NOT TESTED: authenticated model run, private GitHub repository scope, desktop-provided tool inventory'
  end

  def self.sandbox_check
    canary = File.join(Dir.home, '.pgpass')
    if File.exist?(canary) || File.symlink?(canary)
      raise 'Cannot run canary test: ~/.pgpass already exists; no secrets were read'
    end
    Dir.mktmpdir('verify-', File.join(Dir.home, 'projects')) do |fixture|
      work = File.join(fixture, 'workspace')
      Dir.mkdir(work)
      sibling = File.join(fixture, 'outside-workspace')
      File.write(sibling, 'original')
      created = false
      begin
        File.open(canary, File::WRONLY | File::CREAT | File::EXCL, 0o600) do |file|
          created = true
          file.write("agent-vm-synthetic-canary\n")
        end
        Verification.assert(File.read(canary) == "agent-vm-synthetic-canary\n", 'Canary unavailable outside sandbox')
        code = <<~'CHECK'
          File.write('workspace-write-ok', 'ok')
          begin
            File.binread(File.join(Dir.home, '.pgpass'))
            abort 'managed deny-read did not hold'
          rescue Errno::EACCES, Errno::EPERM
          end
          begin
            File.write(ARGV.fetch(0), 'changed')
            abort 'outside-workspace write succeeded'
          rescue Errno::EACCES, Errno::EPERM, Errno::EROFS
          end
          puts 'PASS Codex workspace write, outside-workspace write denial, and managed secret read denial'
        CHECK
        [[], ['-c', 'permissions.vm_dev.filesystem."~/.pgpass"="read"']].each do |overrides|
          output, error, status = Verification.run(['codex', 'sandbox', '--include-managed-config', '-P', 'vm_dev', '-C', work,
                                                   *overrides, 'ruby', '-e', code, sibling])
          if overrides.empty?
            Verification.assert(status.success? && output.include?('PASS Codex'), error + output)
            puts output.strip
          else
            Verification.assert(!status.success? && error.include?('conflicts with a config-defined profile'), error)
          end
        end
        Verification.assert(File.read(sibling) == 'original', 'Outside-workspace file changed')
        puts 'PASS conflicting config override rejected before execution'
      ensure
        File.unlink(canary) if created
      end
    end
  end
end

if $PROGRAM_NAME == __FILE__
  begin
    GuestVerification.check
  rescue StandardError => e
    warn e.message
    exit 1
  end
end
