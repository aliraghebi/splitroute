#!/bin/sh
# Usage: update-cask.sh <version> <app zip>
# Writes the cask for the app zip and pushes it to aliraghebi/homebrew-tap.
set -eu
version=$1 zip=$2
sha=$(shasum -a 256 "$zip" | cut -d' ' -f1)
tap=$(mktemp -d)
git clone --depth 1 "https://x-access-token:${HOMEBREW_TAP_TOKEN}@github.com/aliraghebi/homebrew-tap.git" "$tap"
sed -e "s/@VERSION@/$version/" -e "s/@SHA256@/$sha/" macos/splitroute.rb.in > "$tap/Casks/splitroute.rb"
cd "$tap"
git config user.name "github-actions[bot]"
git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
git add Casks/splitroute.rb
git commit -m "splitroute $version"
git push
