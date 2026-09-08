#!/bin/bash
set -euo pipefail
umask 022
policy_dir=/usr/local/share/devwright
git config --system --replace-all credential.https://github.com.helper ''
git config --system --add credential.https://github.com.helper '!/usr/bin/gh auth git-credential'
git config --system init.defaultBranch main
sudo -u dev -H /usr/bin/gh config set git_protocol https --host github.com

