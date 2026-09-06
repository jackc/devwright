# frozen_string_literal: true
# Real PTYs catch terminal echo/cancellation regressions that mocked token input misses.
require 'minitest/autorun'
require 'pty'
require 'tmpdir'
require 'json'
require 'timeout'
require 'fileutils'

class TerminalTest < Minitest::Test
  BINARY = File.expand_path('../.build/agent-vm', __dir__)
  SHELL = <<~'SH'
    before=$(stty -g)
    "$AGENT_VM_TEST_BINARY" set-token test &
    child=$!
    printf 'child-pid:%s\n' "$child"
    wait "$child"
    result=$?
    after=$(stty -g)
    printf '\nterminal-before:%s\nterminal-after:%s\n' "$before" "$after"
    exit "$result"
  SH

  def test_hidden_input_and_terminal_restoration
    run_prompt(:success)
  end

  def test_ctrl_c_restores_terminal_without_waiting_for_more_input
    run_prompt(:interrupt)
  end

  def test_sigterm_restores_terminal_without_waiting_for_more_input
    run_prompt(:terminate)
  end

  def run_prompt(mode)
    Dir.mktmpdir('agent-vm-terminal-') do |dir|
      bin = File.join(dir, 'bin')
      FileUtils.mkdir_p(bin)
      File.write(File.join(bin, 'limactl'), <<~'SH')
        #!/bin/sh
        case "$1" in
          --version) echo 'limactl version 2.2.0' ;;
          list) cat "$AGENT_VM_TEST_STATE" ;;
          *) exit 9 ;;
        esac
      SH
      File.write(File.join(bin, 'ssh'), <<~'SH')
        #!/bin/sh
        if [ "$1" = -G ]; then exit 0; fi
        cat > "$AGENT_VM_TEST_CAPTURE"
      SH
      File.chmod(0o755, *Dir[File.join(bin, '*')])
      state = { name: 'test', dir: dir, status: 'Running',
                config: { plain: true, user: { name: 'dev' }, ssh: { forwardAgent: false }, mounts: [], provision: [] } }
      File.write(File.join(dir, 'state.json'), JSON.generate(state))
      capture = File.join(dir, 'captured')
      env = { 'PATH' => bin + ':/usr/bin:/bin', 'AGENT_VM_TEST_BINARY' => BINARY,
              'AGENT_VM_TEST_STATE' => File.join(dir, 'state.json'), 'AGENT_VM_TEST_CAPTURE' => capture }
      reader, writer, pid = PTY.spawn(env, '/bin/sh', '-c', SHELL)
      output = +''
      Timeout.timeout(5) do
        begin
          output << reader.readpartial(4096) until output.include?('(hidden): ')
        rescue EOFError, Errno::EIO
          flunk "Token prompt exited early: #{output}"
        end
        case mode
        when :success then writer.write("synthetic-terminal-token\r")
        when :interrupt then writer.write("\x03")
        when :terminate then Process.kill('TERM', Integer(output.match(/child-pid:(\d+)/)[1]))
        end
        begin
          loop { output << reader.readpartial(4096) }
        rescue EOFError, Errno::EIO
          # PTYs report EIO on macOS/Linux when the last slave closes.
        end
        _, status = Process.wait2(pid)
        pid = nil
        expected = mode == :success ? 0 : 1
        assert_equal expected, status.exitstatus, output
        before = output.match(/terminal-before:([^\r\n]+)/)[1]
        after = output.match(/terminal-after:([^\r\n]+)/)[1]
        # Darwin sets PENDIN when canonical input is restored. This transient
        # kernel flag is not a user terminal setting; compare every other bit.
        normalize = lambda do |value|
          RUBY_PLATFORM.include?('darwin') ? value.gsub(/lflag=([0-9a-f]+)/) { "lflag=#{(Regexp.last_match(1).to_i(16) & ~0x20000000).to_s(16)}" } : value
        end
        assert_equal normalize.call(before), normalize.call(after), output
        refute_includes output, 'synthetic-terminal-token'
        if mode == :success
          assert_equal "synthetic-terminal-token\n", File.read(capture)
        else
          refute File.exist?(capture), 'Canceled token input reached SSH'
        end
      end
    ensure
      if pid
        Process.kill('KILL', -pid) rescue nil
        Process.wait(pid) rescue nil
      end
      reader&.close
      writer&.close
    end
  end
end
