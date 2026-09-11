#!/usr/bin/env bash
#
# Install a released build of terraform-provider-kala into Terraform's
# filesystem mirror, so `terraform init` can resolve it without a registry.
#
# Terraform cannot fetch providers from a private repository — see
# docs/private-distribution.md for why, and for the alternatives.
#
# Usage:
#   ./scripts/install-provider.sh v0.1.0
#   ./scripts/install-provider.sh v0.1.0 --dir /opt/terraform-providers
#
# Requires: gh (authenticated), unzip, shasum or sha256sum.

set -euo pipefail

readonly HOSTNAME_="registry.terraform.io"
readonly NAMESPACE="techchapter"
readonly TYPE="kala"
readonly REPO="techchapter/terraform-provider-kala"
readonly BINARY="terraform-provider-${TYPE}"

die() { printf 'error: %s\n' "$1" >&2; exit 1; }
info() { printf '%s\n' "$1" >&2; }

usage() {
  sed -n '3,14p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

# --- arguments ---------------------------------------------------------------

VERSION=""
MIRROR_DIR=""

while [ $# -gt 0 ]; do
  case "$1" in
    -h|--help) usage 0 ;;
    --dir)     MIRROR_DIR="${2:-}"; [ -n "$MIRROR_DIR" ] || die "--dir needs a path"; shift 2 ;;
    -*)        die "unknown flag: $1" ;;
    *)         [ -z "$VERSION" ] || die "unexpected argument: $1"; VERSION="$1"; shift ;;
  esac
done

[ -n "$VERSION" ] || usage 1

# Accept both v0.1.0 and 0.1.0; the tag carries the v, the mirror path does not.
TAG="$VERSION"
case "$TAG" in v*) ;; *) TAG="v${TAG}" ;; esac
BARE_VERSION="${TAG#v}"

# --- prerequisites -----------------------------------------------------------

command -v gh >/dev/null 2>&1 || die "gh is required (https://cli.github.com); it supplies auth for the private repo"
command -v unzip >/dev/null 2>&1 || die "unzip is required"

if command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | awk '{print $1}'; }
elif command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | awk '{print $1}'; }
else
  die "need shasum or sha256sum to verify the download"
fi

gh auth status >/dev/null 2>&1 || die "gh is not authenticated — run: gh auth login"

# --- platform ----------------------------------------------------------------

# Terraform uses Go's names, so ask Go when it is available and fall back to
# uname otherwise. Getting this wrong yields Terraform's unhelpful
# "no package available for your platform".
if command -v go >/dev/null 2>&1; then
  OS="$(go env GOOS)"
  ARCH="$(go env GOARCH)"
else
  case "$(uname -s)" in
    Darwin) OS="darwin" ;;
    Linux)  OS="linux" ;;
    FreeBSD) OS="freebsd" ;;
    MINGW*|MSYS*|CYGWIN*) OS="windows" ;;
    *) die "unsupported OS: $(uname -s) — install Go, or set the target by hand" ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    i386|i686) ARCH="386" ;;
    armv7l|armv6l) ARCH="arm" ;;
    *) die "unsupported architecture: $(uname -m)" ;;
  esac
fi

PLATFORM="${OS}_${ARCH}"

# --- destination -------------------------------------------------------------

case "$OS" in
  windows) DEFAULT_MIRROR_DIR="${APPDATA:-$HOME/AppData/Roaming}/terraform.d/plugins" ;;
  *)       DEFAULT_MIRROR_DIR="${HOME}/.terraform.d/plugins" ;;
esac
[ -n "$MIRROR_DIR" ] || MIRROR_DIR="$DEFAULT_MIRROR_DIR"

DEST="${MIRROR_DIR}/${HOSTNAME_}/${NAMESPACE}/${TYPE}/${BARE_VERSION}/${PLATFORM}"

# --- download ----------------------------------------------------------------

ARCHIVE="${BINARY}_${BARE_VERSION}_${PLATFORM}.zip"
SUMS="${BINARY}_${BARE_VERSION}_SHA256SUMS"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

info "provider : ${HOSTNAME_}/${NAMESPACE}/${TYPE}"
info "version  : ${BARE_VERSION}  (tag ${TAG})"
info "platform : ${PLATFORM}"
info "target   : ${DEST}"
info ""

info "Downloading ${ARCHIVE}..."
gh release download "$TAG" --repo "$REPO" --pattern "$ARCHIVE" --dir "$TMP" 2>/dev/null \
  || die "could not download ${ARCHIVE} from ${TAG}. Check the release exists and includes ${PLATFORM}: gh release view ${TAG} --repo ${REPO}"

# --- verify ------------------------------------------------------------------

if gh release download "$TAG" --repo "$REPO" --pattern "$SUMS" --dir "$TMP" 2>/dev/null; then
  expected="$(grep -F " ${ARCHIVE}" "${TMP}/${SUMS}" | awk '{print $1}' || true)"
  [ -n "$expected" ] || die "${ARCHIVE} is not listed in ${SUMS} — refusing to install an unverifiable artifact"

  actual="$(sha256 "${TMP}/${ARCHIVE}")"
  if [ "$expected" != "$actual" ]; then
    die "checksum mismatch for ${ARCHIVE}
  expected ${expected}
  actual   ${actual}
Refusing to install. Re-download, and treat a repeat mismatch as a compromised release."
  fi
  info "Checksum OK."

  # Provenance, when the release was signed. Absence is not an error: signing is
  # optional (see docs/private-distribution.md).
  if gh release download "$TAG" --repo "$REPO" --pattern "${SUMS}.sig" --dir "$TMP" 2>/dev/null; then
    if command -v gpg >/dev/null 2>&1; then
      if gpg --verify "${TMP}/${SUMS}.sig" "${TMP}/${SUMS}" >/dev/null 2>&1; then
        info "Signature OK."
      else
        die "signature verification FAILED for ${SUMS} — do not install this artifact"
      fi
    else
      info "note: release is signed but gpg is not installed; skipping provenance check."
    fi
  fi
else
  info "warning: no ${SUMS} in the release — integrity cannot be verified."
fi

# --- install -----------------------------------------------------------------

mkdir -p "$DEST"
unzip -o -q "${TMP}/${ARCHIVE}" -d "$DEST"

# GoReleaser names the binary terraform-provider-kala_v<version>, which is
# exactly what the filesystem mirror expects — but normalise anyway so a plain
# binary name also works.
if [ ! -f "${DEST}/${BINARY}_v${BARE_VERSION}" ] && [ -f "${DEST}/${BINARY}" ]; then
  mv "${DEST}/${BINARY}" "${DEST}/${BINARY}_v${BARE_VERSION}"
fi
chmod +x "${DEST}/${BINARY}"_v* 2>/dev/null || true

info ""
info "Installed:"
ls -1 "$DEST" | sed 's/^/  /' >&2
info ""
info "Use it with:"
cat >&2 <<EOF

  terraform {
    required_providers {
      ${TYPE} = {
        source  = "${HOSTNAME_}/${NAMESPACE}/${TYPE}"
        version = "${BARE_VERSION}"
      }
    }
  }

EOF

# The provider_installation block is REQUIRED, including for the default
# plugin directory: Terraform treats ~/.terraform.d/plugins as an implicit
# mirror but still queries the public registry first for
# registry.terraform.io/* addresses, which fails because nothing is published
# there. The direct{exclude} is what stops that fall-through.
cat >&2 <<EOF
This step is REQUIRED — the binary on disk is not enough. Add to ~/.terraformrc
(or the file named by TF_CLI_CONFIG_FILE):

  provider_installation {
    filesystem_mirror {
      path    = "${MIRROR_DIR}"
      include = ["${HOSTNAME_}/${NAMESPACE}/*"]
    }
    direct {
      exclude = ["${HOSTNAME_}/${NAMESPACE}/*"]
    }
  }

Without it, terraform init fails with:
  "provider registry registry.terraform.io does not have a provider named
   ${HOSTNAME_}/${NAMESPACE}/${TYPE}"

EOF
