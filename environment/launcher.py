"""Small Lima/Ansible entry point; no devwright executable is required."""
import argparse
import getpass
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import tomllib

import jsonschema
import yaml

HERE = Path(__file__).resolve().parent
ROOT = HERE.parent
NAME = re.compile(r"[a-z][a-z0-9-]{0,39}\Z")
VARIABLE = re.compile(r"[A-Za-z_][A-Za-z0-9_]*\Z")


def run(args, *, capture=False, check=True, input=None, env=None):
    return subprocess.run([str(a) for a in args], text=True, input=input,
                          stdout=subprocess.PIPE if capture else None,
                          stderr=subprocess.PIPE if capture else None,
                          check=check, env=env)


def load_config(path):
    path = Path(path).resolve()
    defaults = yaml.safe_load((HERE / "config.yaml").read_text())
    config = yaml.safe_load(path.read_text()) or {}
    if not isinstance(config, dict) or config.keys() - defaults.keys():
        raise ValueError("Recipe must be a mapping using only keys from environment/config.yaml")
    config = defaults | config
    for key in ("packages", "credentials"):
        if not isinstance(config[key], list):
            raise ValueError(f"{key} must be a list")
    if not all(isinstance(p, str) and p and not p.startswith('-') for p in config['packages']):
        raise ValueError("packages must contain apt package names")
    if not isinstance(config['environment'], dict):
        raise ValueError("environment must be a mapping")
    for key, value in config['environment'].items():
        if not isinstance(key, str) or not VARIABLE.fullmatch(key) or not isinstance(value, (str, int, float, bool)):
            raise ValueError("environment requires valid variable names and scalar values")
    seen = set()
    for item in config['credentials']:
        if (not isinstance(item, dict) or set(item) != {'name', 'description'}
                or not isinstance(item['name'], str) or not VARIABLE.fullmatch(item['name'])
                or not isinstance(item['description'], str) or item['name'] in seen):
            raise ValueError("credentials require unique names and descriptions")
        seen.add(item['name'])
    if seen & config['environment'].keys():
        raise ValueError("A variable cannot be both a credential and non-secret environment")
    for key in ('dotfiles_repo', 'dotfiles_install', 'codex_requirements', 'codex_config',
                'claude_managed_settings', 'claude_config'):
        if not isinstance(config[key], str) or '\0' in config[key]:
            raise ValueError(f"{key} must be a string without NUL")
    installer = Path(config['dotfiles_install'])
    if not config['dotfiles_install'] or installer.is_absolute() or '..' in installer.parts:
        raise ValueError("dotfiles_install must be a relative path inside its repository")
    if config['dotfiles_repo'].startswith('-'):
        raise ValueError("Invalid dotfiles repository")
    for key in ('reset_codex_requirements', 'reset_claude_managed_settings',
                'replace_codex_config', 'replace_claude_config'):
        if type(config[key]) is not bool:
            raise ValueError(f"{key} must be boolean")
    for key in ('codex_requirements', 'codex_config', 'claude_managed_settings', 'claude_config'):
        if config[key]:
            file = (path.parent / config[key]).resolve()
            contents = file.read_text()
            value = tomllib.loads(contents) if key.startswith('codex') else json.loads(contents)
            if not isinstance(value, dict):
                raise ValueError(f"{key} must contain an object")
            if key == 'codex_requirements':
                if 'default_permissions' in value and not isinstance(value['default_permissions'], str):
                    raise ValueError('default_permissions must be a string')
                for field in ('features', 'allowed_permission_profiles'):
                    if field in value and (not isinstance(value[field], dict)
                            or not all(type(v) is bool for v in value[field].values())):
                        raise ValueError(f'{field} must map names to booleans')
            if key == 'claude_config':
                schema = json.loads((ROOT / 'internal/claudepolicy/schema/settings.json').read_text())
                jsonschema.validate(value, schema)
            if key == 'claude_managed_settings':
                # Match the subset interpreted by the existing managed-policy verifier.
                boolean = {'type': ['boolean', 'null']}
                strings = {'type': ['array', 'null'], 'items': {'type': 'string'}}
                def obj(properties):
                    return {'type': ['object', 'null'], 'properties': properties}
                jsonschema.validate(value, obj({
                    'disableClaudeAiConnectors': boolean,
                    'allowedMcpServers': {'type': ['array', 'null']},
                    'permissions': obj({'deny': strings, 'disableBypassPermissionsMode': {'type': ['string', 'null']}}),
                    'sandbox': obj({
                        'enabled': boolean, 'failIfUnavailable': boolean, 'allowUnsandboxedCommands': boolean,
                        'filesystem': obj({'disabled': boolean, 'denyRead': strings, 'allowManagedReadPathsOnly': boolean})})}))
            config[key] = str(file)
    for agent, policy in (('codex', 'requirements'), ('claude', 'managed_settings')):
        custom, reset = config[f'{agent}_{policy}'], config[f'reset_{agent}_{policy}']
        if custom and reset:
            raise ValueError(f"Cannot select and reset {agent} policy together")
        if config[f'replace_{agent}_config'] and not config[f'{agent}_config']:
            raise ValueError(f"Replacing {agent} settings requires a config file")
        config[f'{agent}_policy_mode'] = 'custom' if custom else 'default' if reset else 'preserve'
    config['project_environment'] = config.pop('environment')
    return config


def check_instance(state, name):
    config = state['config']
    if (state['name'] != name or not Path(state['dir']).is_absolute()
            or any(c in state['dir'] for c in '\r\n\0')):
        raise ValueError("Unexpected Lima identity or directory")
    if (not config.get('plain') or config.get('mounts') or config.get('provision')
            or config.get('ssh', {}).get('forwardAgent')
            or config.get('ssh', {}).get('loadDotSSHPubKeys')
            or config.get('propagateProxyEnv')
            or config.get('portForwards')
            or config.get('user', {}).get('name') != 'dev'
            or config.get('user', {}).get('home') != '/home/dev'
            or config.get('user', {}).get('uid') != 1000):
        raise ValueError("Unexpected Lima mounts, forwarding, provisioning, environment, or account; inspect global overrides")
    return state


def info(name):
    result = run(['limactl', 'list', '--json', name], capture=True)
    rows = [json.loads(line) for line in result.stdout.splitlines() if line.strip()]
    if len(rows) != 1:
        raise ValueError(f"Expected exactly one VM named {name}")
    return check_instance(rows[0], name)


def ssh_options(state):
    return ['-F', str(Path(state['dir']) / 'ssh.config'),
            '-o', 'IdentityAgent=none', '-o', 'ForwardAgent=no',
            '-o', 'ControlPath=~/.ssh/control-%C', '-o', 'ControlMaster=auto',
            '-o', 'ControlPersist=60', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15']


def ssh(state, user, command, **kwargs):
    return run(['ssh', *ssh_options(state), '-l', user, 'lima-' + state['name'],
                shlex.join(command)], **kwargs)


def ssh_config(state):
    file = str(Path(state['dir']) / 'ssh.config').replace('\\', '\\\\').replace('"', '\\"')
    return (f"# Generated by environment/dev\nHost lima-{state['name']}\n"
            "  IdentityAgent none\n  ForwardAgent no\n  ControlMaster auto\n"
            "  ControlPath ~/.ssh/control-%C\n  ControlPersist 60\n"
            f'  Include "{file}"\n\nHost *\n')


def install_ssh(state):
    directory = Path.home() / '.ssh' / 'lima-environments'
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    target = directory / (state['name'] + '.config')
    main = directory.parent / 'config'
    include = 'Include "~/.ssh/lima-environments/*.config"'
    if target.is_symlink() or (target.exists() and not target.read_text().startswith('# Generated by environment/dev\n')):
        raise ValueError(f"Refusing to overwrite unmanaged SSH configuration: {target}")
    old = main.read_text() if main.exists() else ''
    if include not in old.splitlines() and main.is_symlink():
        raise ValueError(f"Add {include} to the source of the SSH config symlink")
    def atomic(path, content):
        with tempfile.NamedTemporaryFile(mode='w', dir=path.parent, delete=False) as f:
            f.write(content)
        os.replace(f.name, path)
    atomic(target, ssh_config(state))
    if include not in old.splitlines():
        if main.exists():
            with tempfile.NamedTemporaryFile(prefix='config.before-lima-environments-', dir=main.parent, delete=False) as f:
                backup = f.name
            shutil.copy2(main, backup)
        atomic(main, include + '\n\n' + old)
    print(f"SSH ready: ssh lima-{state['name']} (dev); ssh root@lima-{state['name']} (admin)")


def playbook(name, inventory, variables, directory):
    executable = Path(sys.executable).with_name('ansible-playbook')
    env = os.environ.copy()
    # Do not inherit arbitrary Ansible plugin/config/inventory or logging overrides.
    env = {k: v for k, v in env.items() if not k.startswith('ANSIBLE_')}
    env['ANSIBLE_CONFIG'] = str(HERE / 'ansible.cfg')
    env['ANSIBLE_LOCAL_TEMP'] = str(directory / 'ansible-tmp')
    run([executable, '-i', inventory, HERE / name, '-e', '@' + str(variables)], env=env)


def configure(state, config):
    for arch in ('arm64', 'amd64'):
        if not (ROOT / f'guestbin/verify-linux-{arch}.gz').is_file():
            raise ValueError("Build the acceptance verifiers first: mise run guest")
    with tempfile.TemporaryDirectory(prefix='lima-environment-') as tmp:
        directory = Path(tmp)
        inventory = directory / 'inventory.json'
        variables = directory / 'vars.json'
        inventory.write_text(json.dumps({'development': {'hosts': {'lima-' + state['name']: {
            'ansible_connection': 'ssh', 'ansible_ssh_common_args': shlex.join(ssh_options(state)),
            'ansible_ssh_args': '', 'ansible_python_interpreter': '/usr/bin/python3'}}}}))
        if config['dotfiles_repo']:
            clone, bundle = directory / 'dotfiles.git', directory / 'dotfiles.bundle'
            run(['git', 'clone', '--bare', '--single-branch', '--', config['dotfiles_repo'], clone])
            run(['git', '-C', clone, 'bundle', 'create', bundle, '--all'])
            config = config | {'dotfiles_bundle': str(bundle)}
        variables.write_text(json.dumps(config))
        if ssh(state, 'root', ['test', '-s', '/root/.ssh/authorized_keys'], capture=True, check=False).returncode:
            playbook('bootstrap.yaml', inventory, variables, directory)
        # A real root connection must succeed before setup revokes dev's sudo.
        ssh(state, 'root', ['test', '-s', '/root/.ssh/authorized_keys'])
        check_instance(info(state['name']), state['name'])
        playbook('setup.yaml', inventory, variables, directory)
    verify(state)


def verify(state):
    ssh(state, 'dev', ['/usr/local/share/devwright/verify'])


def credentials(state, declarations, replace=False):
    if not declarations:
        return
    names = [item['name'] for item in declarations]
    present = ssh(state, 'dev', ['/usr/bin/python3', '-c',
        'import json,os,sys; print(json.dumps([n for n in sys.argv[1:] if os.environ.get(n)]))', *names], capture=True)
    existing = json.loads(present.stdout)
    values = {}
    for item in declarations:
        if not replace and item['name'] in existing:
            continue
        if not sys.stdin.isatty():
            raise ValueError(f"Missing {item['name']}; run credentials from an interactive terminal")
        value = getpass.getpass(f"{item['name']} — {item['description']}: ")
        if not value or '\0' in value:
            raise ValueError(f"{item['name']} must be nonempty and contain no NUL")
        values[item['name']] = value
    if values:
        # Transfer over stdin as dev, not command arguments, Ansible logs, or a host file.
        script = '''import json,os,shlex,sys,tempfile
from pathlib import Path
p=Path.home()/'.config/devwright/credentials.sh'
values=json.load(sys.stdin)
text=p.read_text()
for k,v in values.items():
    begin='# BEGIN ENVIRONMENT CREDENTIAL '+k
    end='# END ENVIRONMENT CREDENTIAL '+k
    if begin in text or end in text:
        if text.count(begin)!=1 or text.count(end)!=1 or text.index(begin)>text.index(end):
            raise ValueError('Malformed managed credential block')
        before,rest=text.split(begin,1)
        _,after=rest.split(end,1)
        text=before+after.lstrip('\\n')
    text+='\\n'+begin+'\\nexport '+k+'='+shlex.quote(v)+'\\n'+end+'\\n'
fd,tmp=tempfile.mkstemp(dir=p.parent)
with os.fdopen(fd,'w') as f: f.write(text)
os.replace(tmp,p)
'''
        ssh(state, 'dev', ['/usr/bin/python3', '-c', script], input=json.dumps(values))
        print('Credentials installed. Reconnect shells and remote runtimes to load new values.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['create', 'configure', 'verify', 'ssh-config', 'install-ssh', 'credentials'])
    parser.add_argument('name', help='Explicit Lima instance name')
    parser.add_argument('--config', default=str(HERE / 'config.yaml'))
    parser.add_argument('--cpus', type=int)
    parser.add_argument('--memory', type=int, help='GiB')
    parser.add_argument('--disk', type=int, help='GiB')
    parser.add_argument('--replace', action='store_true', help='Prompt again for declared credentials')
    args = parser.parse_args()
    if not NAME.fullmatch(args.name):
        parser.error('Name must begin with a lowercase letter and contain at most 40 lowercase letters, digits, or hyphens')
    if args.replace and args.action != 'credentials':
        parser.error('--replace applies only to credentials')
    for key in ('cpus', 'memory', 'disk'):
        value = getattr(args, key)
        if value is not None and (value < 1 or args.action != 'create'):
            parser.error('Positive resource overrides apply only to create')
    config = load_config(args.config)
    version = run(['limactl', '--version'], capture=True).stdout
    match = re.search(r'(\d+)\.(\d+)\.(\d+)', version)
    if not match or tuple(map(int, match.groups())) < (2, 2, 0):
        raise ValueError('Lima 2.2.0 or newer is required')
    if args.action in ('create', 'configure'):
        for arch in ('arm64', 'amd64'):
            if not (ROOT / f'guestbin/verify-linux-{arch}.gz').exists():
                raise ValueError('Build verifiers first: mise run guest')
    if args.action == 'create':
        run(['limactl', 'validate', HERE / 'lima.yaml'])
        command = ['limactl', 'create', '--tty=false', '--name=' + args.name]
        command += [f'--{key}={getattr(args, key)}' for key in ('cpus', 'memory', 'disk') if getattr(args, key)]
        run([*command, HERE / 'lima.yaml'])
    state = info(args.name)
    if args.action in ('create', 'configure') and state['status'] != 'Running':
        run(['limactl', 'start', '--tty=false', args.name])
        state = info(args.name)
    if args.action in ('create', 'configure', 'verify', 'credentials') and state['status'] != 'Running':
        raise ValueError('VM must be running')
    if args.action in ('create', 'configure'):
        configure(state, config)
        install_ssh(state)
        credentials(state, config['credentials'])
        print(f"Ready: ssh lima-{args.name}. Complete account sign-ins inside the guest as needed.")
    elif args.action == 'verify':
        verify(state)
    elif args.action == 'ssh-config':
        print(ssh_config(state), end='')
    elif args.action == 'install-ssh':
        install_ssh(state)
    elif args.action == 'credentials':
        credentials(state, config['credentials'], args.replace)


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError, jsonschema.ValidationError) as error:
        print(f'Error: {error}', file=sys.stderr)
        sys.exit(1)
