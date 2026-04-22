#!/bin/sh
# check_token.sh
if [ "$1" = "start" ]; then
  # Check if token exists (either env var or file)
  if [ -z "$GITHUB_TOKEN" ] && [ ! -f /root/.local/share/copilot-api/github_token ]; then
    echo "ERROR: GitHub token not found"
    echo "Provide token via:"
    echo "  1. Environment: docker run -e GITHUB_TOKEN=ghp_xxxxx ..."
    echo "  2. Volume:     docker run -v copilot-tokens:/root/.local/share/copilot-api ..."
    echo "               then run: docker run -it copilot-api:latest auth"
    exit 1
  fi
fi

exec ./copilot-api-go "$@"