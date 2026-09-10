#!/bin/sh
set -eu

repository="langbot-app/langbot-cli"
version="${LBCTL_VERSION:-latest}"
install_dir="${LBCTL_INSTALL_DIR:-${HOME}/.local/bin}"

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required" >&2
  exit 1
fi

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  MINGW*|MSYS*|CYGWIN*) os="windows" ;;
  *)
    echo "Unsupported operating system: $(uname -s)" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    echo "Unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac

suffix=""
binary_name="lbctl"
if [ "$os" = "windows" ]; then
  suffix=".exe"
  binary_name="lbctl.exe"
fi
asset="lbctl_${os}_${arch}${suffix}"

if [ "$version" = "latest" ]; then
  download_base="https://github.com/${repository}/releases/latest/download"
else
  download_base="https://github.com/${repository}/releases/download/${version}"
fi

temp_dir="$(mktemp -d)"
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

curl -fsSL "${download_base}/${asset}" -o "${temp_dir}/${asset}"
curl -fsSL "${download_base}/checksums.txt" -o "${temp_dir}/checksums.txt"

expected="$(awk -v asset="$asset" '$2 == asset { print $1; exit }' "${temp_dir}/checksums.txt")"
if [ -z "$expected" ]; then
  echo "Checksum not found for ${asset}" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${temp_dir}/${asset}" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "${temp_dir}/${asset}" | awk '{ print $1 }')"
else
  echo "sha256sum or shasum is required" >&2
  exit 1
fi

if [ "$actual" != "$expected" ]; then
  echo "Checksum verification failed for ${asset}" >&2
  exit 1
fi

mkdir -p "$install_dir"
cp "${temp_dir}/${asset}" "${install_dir}/${binary_name}"
chmod 0755 "${install_dir}/${binary_name}"
echo "Installed ${binary_name} to ${install_dir}/${binary_name}"
case ":${PATH:-}:" in
  *:"${install_dir}":*) ;;
  *) echo "Add ${install_dir} to PATH to run ${binary_name} from any terminal." ;;
esac
