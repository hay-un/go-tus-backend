#!/bin/bash
# Delete a bucket (cascade-deletes all objects inside first)
# Usage: ./delete_bucket.sh <bucket-name>
# Example: ./delete_bucket.sh my-music
BASE_URL="${BASE_URL:-http://localhost:8080}"
BUCKET_NAME="${1:-my-music}"

curl -s -o /dev/null -w "%{http_code}" \
  -X DELETE "${BASE_URL}/buckets/${BUCKET_NAME}"
echo ""
