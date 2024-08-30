#!/bin/bash

if ! docker compose version >/dev/null; then
  echo "Docker and Compose plugin is required: https://docs.docker.com/compose/install/" >/dev/tty
  echo FAILED
fi
