#!/usr/bin/env bash
# entrypoint.sh — start envd in the background, then exec the user command.
#
# envd flags (supported):
#   -isnotfc     Skip Firecracker-specific features (prints all logs to stdout)
#   -port        Listen port (default 49983, overridable via ENVD_PORT env var)
set -euo pipefail

ENVD_PORT="${ENVD_PORT:-49983}"

# Start envd in background with only supported flags.
/usr/bin/envd \
  -isnotfc \
  -port "${ENVD_PORT}" &

# Give envd a moment to bind its port.
sleep 2

# Execute the main command (default: tail -f /dev/null to keep container alive).
if [ $# -gt 0 ]; then
  exec "$@"
else
  exec tail -f /dev/null
fi
