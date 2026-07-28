#!/usr/bin/env bash
# entrypoint.sh — start envd in the background, then exec the user command.
#
# envd flags:
#   -isnotfc     Skip Firecracker-specific features (MMDS poll, log exporter)
#   -no-cgroups  Use no-op cgroup manager (container doesn't have full cgroup access)
#   -port        Listen port (default 49983, overridable via ENVD_PORT env var)
#   -verbose     Log to stdout for debugging
set -euo pipefail

ENVD_PORT="${ENVD_PORT:-49983}"

# Start envd in background.
/usr/bin/envd \
  -isnotfc \
  -no-cgroups \
  -port "${ENVD_PORT}" \
  -verbose &

# Give envd a moment to bind its port.
sleep 1

# Execute the main command (default: tail -f /dev/null to keep container alive).
if [ $# -gt 0 ]; then
  exec "$@"
else
  exec tail -f /dev/null
fi
