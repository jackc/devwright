#!/bin/bash
set -euo pipefail
# devwright runs this as the development user, in the project checkout, after
# dotfiles and credentials. It runs once successfully; failures may be retried.
# Example: bundle install
