# frozen_string_literal: true
require 'minitest/autorun'
require 'minitest/mock'
require 'stringio'
require 'rbconfig'
require_relative '../scripts/verify_guest'

class VerificationTest < Minitest::Test
  def test_protocol_handles_notifications_and_buffered_replies
    reader, writer = IO.pipe
    input = StringIO.new
    writer.write("{\"method\":\"notification\"}\n{\"id\":1,\"result\":\"first\"}\n{\"id\":2,\"result\":\"second\"}\n")
    writer.close
    client = CodexPolicyCheck.new(input, reader)
    assert_equal 'first', client.request('one', {}, 1)
    assert_equal 'second', client.request('two', {}, 2)
    assert_equal %w[one two], input.string.lines.map { |line| JSON.parse(line)['method'] }
  ensure
    reader&.close
    writer&.close unless writer&.closed?
  end

  def test_protocol_reports_eof_and_timeout
    reader, writer = IO.pipe
    client = CodexPolicyCheck.new(StringIO.new, reader)
    assert_raises(Timeout::Error) { client.request('wait', {}, 1, timeout: 0.01) }
    writer.close
    assert_match(/exited before replying/, assert_raises(RuntimeError) { client.request('closed', {}, 2) }.message)
  ensure
    reader&.close
    writer&.close unless writer&.closed?
  end

  def test_command_output_status_and_timeout
    out, err, status = Verification.run([RbConfig.ruby, '-e', 'puts "out"; warn "err"; exit 7'])
    assert_equal "out\n", out
    assert_equal "err\n", err
    assert_equal 7, status.exitstatus
    assert_raises(Timeout::Error) { Verification.run([RbConfig.ruby, '-e', 'sleep 30'], timeout: 0.05) }
  end

  def test_existing_canary_is_never_read_or_removed
    Dir.mktmpdir do |home|
      path = File.join(home, '.pgpass')
      File.write(path, 'existing synthetic content')
      Dir.stub(:home, home) do
        assert_match(/already exists/, assert_raises(RuntimeError) { GuestVerification.sandbox_check }.message)
      end
      assert_equal 'existing synthetic content', File.read(path)
    end
  end

  def test_timeout_cleans_up_descendants_after_parent_exits
    code = 'fork { trap("TERM") {}; sleep 30 }; exit! 0'
    assert_raises(Timeout::Error) { Verification.run([RbConfig.ruby, '-e', code], timeout: 0.1) }
  end
end
