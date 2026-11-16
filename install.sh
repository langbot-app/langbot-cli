#!/bin/sh
set -eu
export LBCTL_INSTALL_SOURCE=install.sh

language=""
for candidate in "${LBCTL_LANG:-}" "${LANGBOT_LANG:-}" "${LC_ALL:-}" "${LC_MESSAGES:-}" "${LANG:-}"; do
  case "$candidate" in ""|C|C.*|POSIX|POSIX.*|c|c.*|posix|posix.*) continue ;; esac
  language=$candidate
  break
done
if [ -z "$language" ] && [ "$(uname -s)" = Darwin ]; then
  language=$(defaults read -g AppleLanguages 2>/dev/null | sed -n '2s/[", ()]//gp')
fi
case "$language" in zh*|ZH*) language=zh-Hans ;; *) language=en ;; esac
export LBCTL_LANG="$language"
message() {
  if [ "$language" = zh-Hans ]; then printf '%s\n' "$2"; else printf '%s\n' "$1"; fi
}

if [ -t 1 ] && [ "${NO_COLOR+x}" != x ] && [ "${TERM:-}" != dumb ]; then printf '\033[1;36m'; fi
message "LangBot · Production-grade platform for building agentic IM bots (open source)" "LangBot · 生产级 AI 即时通信机器人开发平台（开源）"
if [ -t 1 ] && [ "${NO_COLOR+x}" != x ] && [ "${TERM:-}" != dumb ]; then printf '\033[0m'; fi
message "Connect LLMs, agents, knowledge bases and plugins to QQ, WeChat/WeCom, Feishu, DingTalk, Telegram, Discord, Slack and more" "把大模型、Agent、知识库与插件接入 QQ、微信/企业微信、飞书、钉钉、Telegram、Discord、Slack 等平台"
message "Website https://langbot.app · Docs https://docs.langbot.app · Plugins https://space.langbot.app" "官网 https://langbot.app · 文档 https://docs.langbot.app · 插件市场 https://space.langbot.app"

repository="langbot-app/langbot-cli"
version="${LBCTL_VERSION:-latest}"
install_dir="${LBCTL_INSTALL_DIR:-${HOME}/.local/bin}"

if ! command -v curl >/dev/null 2>&1; then
  message "[FAIL] curl is required" "[失败] 需要安装 curl" >&2
  exit 1
fi

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  MINGW*|MSYS*|CYGWIN*) os="windows" ;;
  *)
    message "[FAIL] Unsupported operating system: $(uname -s)" "[失败] 不支持的操作系统：$(uname -s)" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *)
    message "[FAIL] Unsupported architecture: $(uname -m)" "[失败] 不支持的架构：$(uname -m)" >&2
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

message "[1/3] Downloading ${asset} ..." "[1/3] 下载 ${asset} ..."
if ! curl -fsSL "${download_base}/${asset}" -o "${temp_dir}/${asset}" ||
   ! curl -fsSL "${download_base}/checksums.txt" -o "${temp_dir}/checksums.txt"; then
  message "[FAIL] Download failed; retry the installer." "[失败] 下载失败；请重试安装。" >&2
  exit 1
fi
message "[2/3] Verifying download ..." "[2/3] 验证下载 ..."

expected="$(awk -v asset="$asset" '$2 == asset { print $1; exit }' "${temp_dir}/checksums.txt")"
if [ -z "$expected" ]; then
  message "[FAIL] Checksum not found for ${asset}" "[失败] 未找到 ${asset} 的校验和" >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${temp_dir}/${asset}" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "${temp_dir}/${asset}" | awk '{ print $1 }')"
else
  message "[FAIL] sha256sum or shasum is required" "[失败] 需要 sha256sum 或 shasum" >&2
  exit 1
fi

if [ "$actual" != "$expected" ]; then
  message "[FAIL] Checksum verification failed for ${asset}" "[失败] ${asset} 校验失败" >&2
  exit 1
fi

mkdir -p "$install_dir"
cp "${temp_dir}/${asset}" "${install_dir}/${binary_name}"
chmod 0755 "${install_dir}/${binary_name}"
message "[3/3] Installed ${binary_name}: ${install_dir}/${binary_name}" "[3/3] 已安装 ${binary_name}：${install_dir}/${binary_name}"
case ":${PATH:-}:" in
  *:"${install_dir}":*) ;;
  *) message "Add ${install_dir} to PATH to run ${binary_name} from any terminal." "将 ${install_dir} 加入 PATH，即可在任意终端运行 ${binary_name}。" ;;
esac
