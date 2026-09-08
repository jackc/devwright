#!/usr/bin/python3
"""Write recipe environment and its loader as dev; never evaluate shell code."""
import json
import os
from pathlib import Path
import shlex
import sys
import tempfile


def write_atomic(path, content):
    fd, name = tempfile.mkstemp(dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as stream:
            stream.write(content)
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def configure(home, values):
    directory = home / '.config/devwright'
    content = '# Non-secret environment from the checked-in recipe.\n'
    content += ''.join(f'export {key}={shlex.quote(str(value))}\n' for key, value in values.items())
    write_atomic(directory / 'environment.sh', content)
    path = directory / 'credentials.sh'
    content = path.read_text()
    hook = '. "$HOME/.config/devwright/environment.sh"'
    if hook not in content.splitlines():
        content += '\n' + hook + '\n'
        write_atomic(path, content)


if __name__ == '__main__':
    if os.getuid() == 0 or Path.home() != Path('/home/dev'):
        raise SystemExit('Must run as dev with HOME=/home/dev')
    configure(Path.home(), json.load(sys.stdin))
