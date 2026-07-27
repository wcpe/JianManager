#!/bin/sh
set -eu

data_dir="${JIANMANAGER_DATA_DIR:-/app/data}"
runtime_modules="$data_dir/opt/runtimes/global/node_modules"

if [ ! -f "$runtime_modules/mineflayer/package.json" ]; then
  mkdir -p "$runtime_modules"
  cp -a /opt/jianmanager/bot-worker-node_modules/. "$runtime_modules/"
fi

exec /app/worker "$@"
