#!/usr/bin/env bash
set -euo pipefail

if ! command -v gh > /dev/null 2>&1
then
  echo "unable to find gh binary."
  exit 1
fi

# Pick a sha256 utility — sha256sum on Linux, shasum on macOS by default.
if command -v sha256sum > /dev/null 2>&1
then
  SHA256_CHECK=("sha256sum" "-c")
elif command -v shasum > /dev/null 2>&1
then
  SHA256_CHECK=("shasum" "-a" "256" "-c")
else
  echo "unable to find sha256sum or shasum."
  exit 1
fi

GITHUB_TOKEN="$(gh auth token 2>/dev/null || true)"
if [ -z "$GITHUB_TOKEN" ]
then
  gh auth login -p https -w
fi
GITHUB_TOKEN="$(gh auth token)"

REPO="dmcclory/rhodium-prototype"
PROJECT="rhodium-prototype"
BIN_NAME="rhodium"
DESTINATION_DIR="$HOME/.rhodium/bin"

TEMPDIR="$(mktemp -d)"
trap 'rm -rf -- "$TEMPDIR"' EXIT

LATEST_VERSION="$(gh release -R "$REPO" ls --exclude-pre-releases | grep Latest | cut -f1)"
if [ -z "$LATEST_VERSION" ]
then
  echo "no published release found for $REPO."
  exit 1
fi

echo "downloading the latest release ($LATEST_VERSION)..."
gh release -R "$REPO" download "$LATEST_VERSION" -D "$TEMPDIR"

echo "validating checksums..."
(cd "$TEMPDIR" && "${SHA256_CHECK[@]}" "${PROJECT}_${LATEST_VERSION:1}_checksums.txt")

mkdir -p "$DESTINATION_DIR"

# Map uname output to the goreleaser archive naming. uname -s yields
# Darwin/Linux directly; uname -m yields x86_64 / arm64 on macOS and
# x86_64 / aarch64 / i686 on Linux — the last two need remapping.
KERNEL="$(uname -s)"
case "$(uname -m)" in
  x86_64|amd64)  ARCH="x86_64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  i386|i686)     ARCH="i386" ;;
  *)
    echo "unsupported architecture: $(uname -m)"
    exit 1
    ;;
esac

(cd "$TEMPDIR" && tar xzf "${PROJECT}_${KERNEL}_${ARCH}.tar.gz" && cp "$BIN_NAME" "$DESTINATION_DIR/$BIN_NAME")
chmod +x "$DESTINATION_DIR/$BIN_NAME"

if [ -f "$HOME/.zshrc" ] && ! grep -q '$HOME/.rhodium/bin' "$HOME/.zshrc"
then
  echo "Adding rhodium to the ZSH path"
  echo 'export PATH="$HOME/.rhodium/bin:$PATH"' >> "$HOME/.zshrc"
fi

if [ -f "$HOME/.bashrc" ] && ! grep -q '$HOME/.rhodium/bin' "$HOME/.bashrc"
then
  echo "Adding rhodium to the bash path"
  echo 'export PATH="$HOME/.rhodium/bin:$PATH"' >> "$HOME/.bashrc"
fi

if [ -f "$HOME/.config/fish/config.fish" ] && ! grep -q '$HOME/.rhodium/bin' "$HOME/.config/fish/config.fish"
then
  echo "Adding rhodium to the fish path"
  echo 'set -gx PATH "$HOME/.rhodium/bin" $PATH' >> "$HOME/.config/fish/config.fish"
fi

echo "rhodium $LATEST_VERSION has been successfully installed!"
