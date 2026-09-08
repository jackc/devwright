# frozen_string_literal: true
# Standard-library-only tests: ruby environment/test_launcher.rb
require_relative 'launcher'

def assert(value, message = 'Assertion failed')
  raise message unless value
end

def rejects
  begin
    yield
  rescue StandardError
    return
  end
  raise 'Expected rejection'
end

count = 0
test = lambda do |name, &block|
  block.call
  count += 1
  puts "PASS #{name}"
end

Dir.mktmpdir('lima-recipe-tests-') do |dir|
  path = File.join(dir, 'config.yaml')
  test.call('unknown and removed recipe options rejected') do
    [{ 'ansible_connection' => 'local' }, { 'replace_codex_config' => true }, { 'environment' => { 'BAD-NAME' => 'x' } }, { 'dotfiles_install' => '../escape' }, { 'packages' => ['--bad'] }, { 'credentials' => [{ 'name' => 'TOKEN', 'description' => 'test' }], 'environment' => { 'TOKEN' => 'secret' } }].each do |data|
      File.write(path, YAML.dump(data))
      rejects { Environment.config(path) }
    end
  end
  test.call('script paths resolved and Bash syntax checked') do
    File.write(File.join(dir, 'setup.sh'), "#!/bin/bash\necho ready\n")
    File.write(path, YAML.dump({ 'user_script' => 'setup.sh' }))
    assert(Environment.config(path)['user_script'] == File.join(dir, 'setup.sh'))
    File.write(File.join(dir, 'setup.sh'), "if then\n")
    rejects { Environment.config(path) }
  end
  data = Environment.config(File.join(__dir__, 'config.yaml'))
  vm = Environment.render(data)
  script = vm['provision'][0]['script']
  test.call('render contains one Bash provisioner with a completion guard') do
    assert(vm['provision'].length == 1)
    assert(script.index('lima-setup-complete') < script.index('base64 -d'))
    assert(Digest::SHA256.hexdigest(script) == JSON.parse(vm['message'])['environment_script_sha256'])
    Environment.capture('bash', '-n', input: script)
  end
  test.call('failed setup retries and completed setup is skipped') do
    test_state = File.join(dir, 'boot-state')
    payload = "#!/bin/bash\nset -eu\necho attempt >> #{File.join(dir, 'attempts').shellescape}\nif [ ! -e #{File.join(dir, 'retry').shellescape} ]; then touch #{File.join(dir, 'retry').shellescape}; exit 23; fi\n"
    guard = Environment.read('environment/provision.sh').sub('__SETUP_B64__', Environment.encoded(payload)).sub('state=/usr/local/share/devwright', "state=#{test_state.shellescape}")
    rejects { Environment.capture('bash', input: guard) }
    assert(!File.exist?(File.join(test_state, 'lima-setup-complete')))
    Environment.capture('bash', input: guard)
    Environment.capture('bash', input: guard)
    assert(File.readlines(File.join(dir, 'attempts')).length == 2)
    assert(File.exist?(File.join(test_state, 'lima-setup-complete')))
  end
  state = { 'name' => 'test', 'dir' => File.join(dir, 'test'), 'config' => vm }
  test.call('effective recipe accepted and injected provisions rejected') do
    Environment.check_instance(state, 'test', expected: vm)
    other = Marshal.load(Marshal.dump(state))
    other['config']['provision'] << { 'mode' => 'system', 'script' => 'bad' }
    rejects { Environment.check_instance(other, 'test') }
  end
  test.call('global overrides rejected before first boot') do
    %w[mounts portForwards probes].each do |key|
      other = Marshal.load(Marshal.dump(state))
      other['config'][key] = [{}]
      rejects { Environment.check_instance(other, 'test', expected: vm) }
    end
    other = Marshal.load(Marshal.dump(state))
    other['config']['provision'][0]['script'] += "\necho injected\n"
    rejects { Environment.check_instance(other, 'test') }
  end
  test.call('SSH aliases preserve existing host configuration and separate users') do
    FileUtils.mkdir_p(File.join(dir, '.ssh'))
    File.write(File.join(dir, '.ssh/config'), "Host personal\n  HostName example.com\n")
    2.times { Environment.install_ssh(state, home: dir) }
    text = File.read(File.join(dir, '.ssh/config'))
    assert(text.scan('Include').length == 1 && text.include?('Host personal'))
    assert(Environment.ssh_config(state).include?('ControlPath ~/.ssh/control-%C'))
    %w[root dev].each do |user|
      args = Environment.ssh_args(state, user, ['echo', '$(literal)'])
      assert(args.include?('IdentityAgent=none') && args[args.index('-l') + 1] == user)
      assert(args.last == ['echo', '$(literal)'].shelljoin)
    end
  end
  test.call('credential values stay in stdin script and are shell-quoted') do
    value = "literal '\" $HOME $(false)\nsecond line"
    text = Environment.credential_script({ 'TOKEN' => value })
    Environment.capture('bash', '-n', input: text)
    encoded = text.match(/printf '%s' '([^']+)'/)[1]
    assignment = Base64.strict_decode64(encoded)
    output = Environment.capture('bash', '-c', assignment + '\nprintf "%s" "$TOKEN"'.sub('\\n', "\n"))
    assert(output == value)
    rejects { Environment.credential_script({ 'BAD-NAME' => 'x' }) }
  end
  test.call('configuration-in-place explicitly rejected') do
    rejects { Environment.main(['configure', 'test']) }
  end
end
puts "#{count} tests passed"
