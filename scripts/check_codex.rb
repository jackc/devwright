#!/usr/bin/env ruby
# frozen_string_literal: true

# Credential-free verification helpers and Codex app-server policy checks.
require 'json'
require 'open3'
require 'timeout'

module Verification
  def self.assert(condition, message)
    raise message unless condition
  end

  def self.stop(waiter)
    begin
      Process.kill('TERM', -waiter.pid)
    rescue Errno::ESRCH
      # The whole group may already have exited.
    end
    waiter.join(5)
    begin
      # Also clean up descendants holding pipes after their parent has exited.
      Process.kill('KILL', -waiter.pid)
    rescue Errno::ESRCH
    end
    waiter.join
  end

  def self.run(args, timeout: 45)
    Open3.popen3(*args, pgroup: true) do |input, output, error, waiter|
      input.close
      readers = [output, error].map { |io| Thread.new { io.read } }
      begin
        Timeout.timeout(timeout) { [readers[0].value, readers[1].value, waiter.value] }
      ensure
        stop(waiter)
        readers.each(&:join)
      end
    end
  end
end

class CodexPolicyCheck
  FEATURE_KEYS = %w[apps plugins browser_use in_app_browser computer_use].freeze

  def initialize(input, output)
    @input = input
    @output = output
    @buffer = +''
  end

  def send_message(message)
    @input.puts(JSON.generate(message))
    @input.flush
  end

  def request(method, params, id, timeout: 20)
    send_message('id' => id, 'method' => method, 'params' => params)
    deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + timeout
    loop do
      while (newline = @buffer.index("\n"))
        message = JSON.parse(@buffer.slice!(0..newline))
        next unless message['id'] == id

        raise message['error'].inspect if message.key?('error')

        return message.fetch('result')
      end
      remaining = deadline - Process.clock_gettime(Process::CLOCK_MONOTONIC)
      raise Timeout::Error, method if remaining <= 0 || !IO.select([@output], nil, nil, remaining)

      @buffer << @output.readpartial(16_384)
    end
  rescue EOFError
    raise 'Codex app-server exited before replying'
  end

  def self.check
    args = ['codex', 'app-server', '--stdio', '--strict-config',
            '-c', 'features.apps=true', '-c', 'features.plugins=true']
    Open3.popen2(*args, err: File::NULL, pgroup: true) do |input, output, waiter|
      begin
        client = new(input, output)
        client.request('initialize', {
                         'clientInfo' => { 'name' => 'agent_vm_verify', 'version' => '1.0' },
                         'capabilities' => { 'experimentalApi' => true }
                       }, 1)
        client.send_message('method' => 'initialized')
        requirements = client.request('configRequirements/read', {}, 2).fetch('requirements')
        config = client.request('config/read', { 'includeLayers' => false }, 3).fetch('config')
        Verification.assert(requirements['allowedPermissionProfiles'] == { 'vm_dev' => true }, 'Unexpected managed profiles')
        Verification.assert(requirements['defaultPermissions'] == 'vm_dev', 'Unexpected managed default')
        Verification.assert(config['default_permissions'] == 'vm_dev', 'Unexpected configured default')
        Verification.assert(FEATURE_KEYS.all? { |key| requirements.fetch('featureRequirements')[key] == false },
                            'Missing managed feature restrictions')
      ensure
        Verification.stop(waiter)
      end
    end
    # config/read returns raw requests. Check resolved flags separately.
    output, error, status = Verification.run(['codex', '-c', 'features.apps=true', '-c', 'features.plugins=true',
                                               'features', 'list'], timeout: 20)
    Verification.assert(status.success?, error)
    features = output.lines.reject { |line| line.strip.empty? }.to_h { |line| [line.split.first, line.split.last] }
    Verification.assert(FEATURE_KEYS.all? { |key| features[key] == 'false' }, 'Managed feature pin was not enforced')
    puts 'PASS Codex app-server reads managed profile; resolved features reject apps/plugins overrides'
  end
end

if $PROGRAM_NAME == __FILE__
  begin
    CodexPolicyCheck.check
  rescue StandardError => e
    warn e.message
    exit 1
  end
end
