#!/bin/bash
# Stream a file (or a byte range) from a bucket.
# The backend responds with 206 Partial Content for range requests,
# enabling native <video>/<audio> players to seek and buffer.
#
# Usage:
#   ./stream_file.sh <bucket> <key>                    # full file → 200 OK
#   ./stream_file.sh <bucket> <key> <start>-<end>      # byte range → 206
#
# Examples:
#   ./stream_file.sh my-videos abc123-uuid
#   ./stream_file.sh my-videos abc123-uuid 0-1048575
#   ./stream_file.sh my-videos abc123-uuid -524288    # last 512 KB

BASE_URL="${BASE_URL:-http://localhost:8080}"
BUCKET="${1:?Usage: $0 <bucket> <key> [byte-range]}"
KEY="${2:?Usage: $0 <bucket> <key> [byte-range]}"
RANGE="${3:-}"

STREAM_URL="${BASE_URL}/files/${BUCKET}/${KEY}/stream"

echo "→ GET ${STREAM_URL}"

if [[ -n "$RANGE" ]]; then
  echo "→ Range: bytes=${RANGE}"
  curl -s -i \
    -H "Range: bytes=${RANGE}" \
    "${STREAM_URL}" | head -30
else
  # Fetch headers only for a quick sanity check.
  curl -s -I "${STREAM_URL}"
fi
