#!/bin/bash
set -e

/usr/local/bin/homelab-healthcheck &

exec "$@"
