#!/bin/sh
set -eu
project_dir="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
venv_dir="${1:-$project_dir/bin/.venv}"
python3 -m venv "$venv_dir"
"$venv_dir/bin/python" -m pip install --upgrade pip
"$venv_dir/bin/python" -m pip install -r "$project_dir/requirements.txt"
printf 'Python environment: %s\n' "$venv_dir"
