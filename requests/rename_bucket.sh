#!/bin/bash
# Rename a bucket (copy all objects to new bucket, delete old)
# Usage: ./rename_bucket.sh <old-name> <new-name>
# Example: ./rename_bucket.sh my-music my-new-music
BASE_URL="${BASE_URL:-http://localhost:8080}"
OLD_NAME="${1:-my-music}"
NEW_NAME="${2:-my-new-music}"

curl -s -X POST "${BASE_URL}/buckets/${OLD_NAME}/rename" \
  -H "Content-Type: application/json" \
  -d "{\"new_name\": \"${NEW_NAME}\"}" | jq .
