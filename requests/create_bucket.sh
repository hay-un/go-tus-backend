#!/bin/bash
# Create a new bucket
# Usage: ./create_bucket.sh <bucket-name>
# Example: ./create_bucket.sh my-music
BASE_URL="${BASE_URL:-http://localhost:8080}"
BUCKET_NAME="${1:-my-music}"

curl -s -X POST "${BASE_URL}/buckets" \
  -H "Content-Type: application/json" \
  -d "{\"name\": \"${BUCKET_NAME}\"}" | jq .
