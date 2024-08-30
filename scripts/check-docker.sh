#!/bin/bash

# skip checks if there is no tty (this is being called in a build script)
if [ -f /.dockerenv ] || [ ! -e /dev/tty ] || [ ! -t 0 ]; then
  exit 0
fi

if ! docker compose version >/dev/null; then
  echo "Docker and Compose plugin is required: https://docs.docker.com/compose/install/" >/dev/tty
  echo FAILED
fi
