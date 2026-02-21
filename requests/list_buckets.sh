#!/bin/bash
# List all buckets
BASE_URL="${BASE_URL:-http://localhost:8080}"

curl -s -X GET "${BASE_URL}/buckets" \
  -H "Accept: application/json" | jq .
