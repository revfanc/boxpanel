#!/usr/bin/env bash
# boxpanel installer; business operations belong to the boxpanel CLI.
# Requires Bash 3.2+ (also testable on macOS). No eval or unverified executable.
REPO="${BOXPANEL_REPO:-revfanc/boxpanel}"
BINARY=/usr/local/bin/boxpanel
DATA_DIR=/etc/boxpanel
CORE_BINARY=/usr/local/share/boxpanel/sing-box
UNIT_DIR=/etc/systemd/system
LOCK_DIR=/run/boxpanel-installer.lock
OS_RELEASE=/etc/os-release
SYSTEMD_DIR=/run/systemd/system
COMMAND=install
TAG=
PORT=
NON_INTERACTIVE=false
DRY_RUN=false
YES=false
PURGE=false
TEMP_DIR=
STAGED_BINARY=
LOCKED=false

say() { printf '%s\n' "$*"; }
die() { say "错误：$*" >&2; exit 1; }
progress() { say "[$1/8] $2"; }

usage() {
    cat <<'EOF'
用法：bash install.sh [install|update|uninstall] [选项]
  --version <vX.Y.Z[-rc.N]>  指定 Release tag；省略使用最新稳定版
  --port <1-65535>           仅用于首次初始化；省略由 CLI 随机选择
  --non-interactive         不读取输入；安装时默认尝试开启 BBR
  --yes                     确认卸载（非交互卸载必需）
  --purge                   卸载时删除 /etc/boxpanel（默认保留）
  --dry-run                 只检查平台并打印计划，不下载、不修改系统
  --help                    显示帮助
环境变量：BOXPANEL_REPO（默认 revfanc/boxpanel）、BOXPANEL_PUBLIC_IP。
EOF
}

valid_tag() { [[ $1 =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9]+(\.[A-Za-z0-9]+)*)?$ ]]; }

parse_args() {
    local command_seen=false
    while (($#)); do
        case "$1" in
            install|update|uninstall)
                [[ $command_seen == false ]] || die '只能指定一个子命令；运行 --help 查看用法。'
                COMMAND=$1; command_seen=true; shift ;;
            --non-interactive) NON_INTERACTIVE=true; shift ;;
            --yes) YES=true; shift ;;
            --purge) PURGE=true; shift ;;
            --dry-run) DRY_RUN=true; shift ;;
            --version|--port)
                [[ $# -ge 2 && -n $2 ]] || die '选项缺少值；运行 --help 查看用法。'
                if [[ $1 == --version ]]; then TAG=$2; else PORT=$2; fi
                shift 2 ;;
            --help|-h) usage; exit 0 ;;
            *) die "未知参数 $1；运行 --help 查看用法。" ;;
        esac
    done
    [[ $REPO =~ ^[A-Za-z0-9_-]+/[A-Za-z0-9_.-]+$ ]] || die '仓库名无效；请将 BOXPANEL_REPO 设为 owner/repo。'
    [[ -z $TAG ]] || valid_tag "$TAG" || die '版本必须是 vX.Y.Z 或 vX.Y.Z-rc.N 格式；请检查 Release tag。'
    if [[ -n $PORT ]]; then
        [[ $PORT =~ ^[0-9]{1,5}$ ]] || die '端口无效；请选择 1–65535。'
        PORT=$((10#$PORT))
        ((PORT >= 1 && PORT <= 65535)) || die '端口无效；请选择 1–65535。'
        [[ $COMMAND == install ]] || die '--port 仅用于 install；请通过面板设置修改端口。'
    fi
    [[ $COMMAND != uninstall || -z $TAG ]] || die '卸载不接受 --version；请移除此选项。'
    [[ $COMMAND == uninstall || ( $YES == false && $PURGE == false ) ]] || die '--yes/--purge 仅用于 uninstall；请检查参数。'
    # Piped input is never treated as a confirmation.
    if [[ ! -t 0 ]]; then NON_INTERACTIVE=true; fi
}

detect_platform() {
    [[ $(uname -s) == Linux ]] || die '仅支持 Linux/systemd；请在 Linux VPS 上运行。'
    case "$(uname -m)" in
        x86_64|amd64) ARCH=amd64 ;;
        aarch64|arm64) ARCH=arm64 ;;
        *) die '仅支持 amd64/arm64；请更换受支持的 VPS 架构。' ;;
    esac
    [[ -r $OS_RELEASE ]] || die '缺少 /etc/os-release；请使用受支持的 Linux 发行版。'
    local ID='' VERSION_ID='' NAME='' major minor
    # This is a root-owned OS file, never downloaded content.
    # shellcheck disable=SC1090
    . "$OS_RELEASE"
    [[ $VERSION_ID =~ ^[0-9]+(\.[0-9]+)*$ ]] || die '无法识别系统版本；请使用 Debian 11+、Ubuntu 20.04+ 或受支持的 RHEL 系发行版。'
    major=${VERSION_ID%%.*}
    minor=${VERSION_ID#*.}; minor=${minor%%.*}
    case "$ID" in
        debian) ((major >= 11)) || die '请先升级到 Debian 11 或更新版本。'; PACKAGE_MANAGER=apt ;;
        ubuntu)
            ((major > 20 || (major == 20 && 10#$minor >= 4))) || die '请先升级到 Ubuntu 20.04 或更新版本。'
            PACKAGE_MANAGER=apt ;;
        centos)
            if [[ $NAME != *Stream* ]] || ((major < 9)); then die '仅支持 CentOS Stream 9+；请更换系统。'; fi
            PACKAGE_MANAGER=dnf ;;
        rocky|almalinux) ((major >= 8)) || die '请先升级到 Rocky/AlmaLinux 8 或更新版本。'; PACKAGE_MANAGER=dnf ;;
        *) die '发行版不支持（Alpine 尚未支持）；请使用 Debian、Ubuntu、CentOS Stream、Rocky 或 AlmaLinux。' ;;
    esac
}

preflight() {
    [[ $(id -u) == 0 ]] || die '需要 root；请使用 sudo bash install.sh。'
    if [[ ! -d $SYSTEMD_DIR ]] || ! command -v systemctl >/dev/null; then die '需要运行中的 systemd；请使用以 systemd 启动的 VPS/容器。'; fi
    systemctl show --property=Version --value >/dev/null || die '无法连接 systemd；请检查系统服务管理器后重试。'
}

configure_cli() {
    # Keep all CLI invocations and generated units on the same production paths.
    export BOXPANEL_DATA_DIR="$DATA_DIR" BOXPANEL_DB_PATH="$DATA_DIR/boxpanel.db"
    export BOXPANEL_CERT_FILE="$DATA_DIR/certs/panel.crt" BOXPANEL_KEY_FILE="$DATA_DIR/certs/panel.key"
    export BOXPANEL_SINGBOX_PATH="$CORE_BINARY" BOXPANEL_SINGBOX_CONFIG="$DATA_DIR/sing-box.json"
    export BOXPANEL_UNIT_DIR="$UNIT_DIR" BOXPANEL_BINARY="$BINARY"
    unset BOXPANEL_LISTEN_HOST
}

cleanup() {
    local status=$?
    trap - EXIT
    [[ -z $STAGED_BINARY ]] || rm -f -- "$STAGED_BINARY"
    [[ -z $TEMP_DIR ]] || rm -rf -- "$TEMP_DIR"
    if [[ $LOCKED == true ]]; then rmdir -- "$LOCK_DIR"; fi
    if ((status != 0)); then
        say '操作未完成。请按上方提示修复后重试相同命令；安装可续接，忘记初始密码可运行 boxpanel reset-admin。' >&2
    fi
    exit "$status"
}

install_dependencies() {
    local packages=(curl tar ca-certificates)
    if ! command -v nft >/dev/null && ! { command -v iptables >/dev/null && command -v ip6tables >/dev/null; }; then
        packages+=(nftables)
    fi
    if [[ $PACKAGE_MANAGER == apt ]]; then
        DEBIAN_FRONTEND=noninteractive apt-get update
        DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${packages[@]}" iproute2 kmod coreutils
    else
        dnf install -y "${packages[@]}" iproute kmod coreutils
    fi
}

download() {
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
        --connect-timeout 15 --max-time 180 --retry 2 --retry-max-time 240 \
        --max-filesize "$3" --output "$2" "$1"
}

resolve_tag() {
    if [[ -z $TAG ]]; then
        local effective
        effective=$(curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
            --connect-timeout 15 --max-time 60 --output /dev/null --write-out '%{url_effective}' \
            "https://github.com/$REPO/releases/latest")
        [[ $effective == "https://github.com/$REPO/releases/tag/"* ]] || die '无法解析最新 Release；请用 --version 指定已发布 tag。'
        TAG=${effective##*/}
    fi
    valid_tag "$TAG" || die 'Release tag 格式无效；请用 --version 指定有效版本。'
}

sha256() {
    if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi
}

verify_archive() {
    local archive=$1 name=$2 checksums=$3 expected actual listing details
    expected=$(awk -v name="$name" '
        { file=$2; sub(/^\*/, "", file) }
        file == name { if (NF != 2) exit 1; count++; hash=$1 }
        END { if (count != 1) exit 1; print hash }
    ' "$checksums") || die 'checksums.txt 缺少唯一的压缩包条目；请检查 Release 后重试。'
    [[ $expected =~ ^[a-fA-F0-9]{64}$ ]] || die 'SHA-256 摘要格式无效；请重新发布正确的 checksums.txt。'
    actual=$(sha256 "$archive"); actual=${actual%% *}
    [[ $(printf '%s' "$expected" | tr 'A-F' 'a-f') == "$actual" ]] || die 'SHA-256 不匹配；已停止安装，请重新下载官方 Release。'
    # The release contract is exactly one regular file at archive root.
    listing=$(tar -tzf "$archive") || die '压缩包损坏；请重新下载。'
    [[ $listing == boxpanel ]] || die '压缩包必须只包含 boxpanel；请检查发布产物。'
    details=$(LC_ALL=C tar -tvzf "$archive") || die '无法检查压缩包；请重新下载。'
    [[ $details == -* && $details != *$'\n'* ]] || die '压缩包包含链接或特殊文件；请检查发布产物。'
}

stage_release() {
    resolve_tag
    local name="boxpanel-linux-$ARCH.tar.gz" base="https://github.com/$REPO/releases/download/$TAG" got
    TEMP_DIR=$(mktemp -d)
    download "$base/checksums.txt" "$TEMP_DIR/checksums.txt" 1048576
    download "$base/$name" "$TEMP_DIR/$name" 134217728
    verify_archive "$TEMP_DIR/$name" "$name" "$TEMP_DIR/checksums.txt"
    mkdir -p -- "${BINARY%/*}"
    STAGED_BINARY=$(mktemp "${BINARY%/*}/.boxpanel-new.XXXXXX")
    tar -xOzf "$TEMP_DIR/$name" boxpanel >"$STAGED_BINARY"
    [[ -s $STAGED_BINARY ]] || die '压缩包中的二进制为空；请检查 Release。'
    chmod 0755 "$STAGED_BINARY"
    got=$("$STAGED_BINARY" version)
    [[ $got == "boxpanel $TAG" ]] || die '二进制版本与 tag 不一致；请检查 Release 构建参数。'
}

replace_binary() {
    mv -f -- "$STAGED_BINARY" "$BINARY"
    STAGED_BINARY=
}

wait_panel() {
    local attempt
    # serve performs core startup/apply before binding HTTPS; allow that work.
    for ((attempt=0; attempt<30; attempt++)); do
        if systemctl is-active --quiet boxpanel.service && "$BINARY" system ready >/dev/null 2>&1; then
            sleep 1
            if systemctl is-active --quiet boxpanel.service && "$BINARY" system ready >/dev/null 2>&1; then return 0; fi
        fi
        sleep 1
    done
    return 1
}

confirm() {
    local reply
    printf '%s [y/N] ' "$1"
    read -r reply || return 1
    [[ $reply == y || $reply == Y || $reply == yes ]]
}

install_panel() {
    local marker="$DATA_DIR/.installing"
    if [[ -x $BINARY && -f $CORE_BINARY && -f $UNIT_DIR/boxpanel.service && -f $UNIT_DIR/boxpanel-singbox.service && ! -e $marker ]] && "$BINARY" info >/dev/null 2>&1; then
        say 'boxpanel 已安装；升级请运行 install.sh update。'
        "$BINARY" info
        return
    fi
    progress 3 '安装系统依赖'
    install_dependencies
    progress 4 '下载并校验 boxpanel'
    stage_release
    mkdir -p -- "$DATA_DIR"
    chmod 0700 "$DATA_DIR"
    touch "$marker"
    replace_binary
    progress 5 '初始化或续接已有数据库（凭证只显示一次，请保存）'
    if ! "$BINARY" info >/dev/null 2>&1; then
        if [[ -n $PORT ]]; then "$BINARY" init --port "$PORT"; else "$BINARY" init; fi
    else
        say '已初始化，沿用原账号、证书和设置；忘记密码请运行 boxpanel reset-admin。'
    fi
    progress 6 '通过 CLI 安装并校验锁定的 sing-box'
    "$BINARY" singbox install --preserve-version
    progress 7 '校验生效、放行面板端口并启用服务'
    "$BINARY" apply
    "$BINARY" system firewall-open
    systemctl enable boxpanel-singbox.service boxpanel.service
    systemctl restart boxpanel.service
    wait_panel || die '面板服务未存活；请运行 journalctl -u boxpanel 检查后重试 install。'
    progress 8 '完成安装及可选 BBR 设置'
    if [[ $NON_INTERACTIVE == true ]] || confirm '是否开启 BBR？'; then
        if ! "$BINARY" system bbr; then say 'BBR 未开启，面板仍可使用；请在面板自检中查看内核支持并重试。'; fi
    fi
    rm -f -- "$marker"
    say 'boxpanel 安装完成。'
    "$BINARY" info
    say '自签证书提示：核对地址后在浏览器选择“高级 → 继续访问”。请在云安全组放行面板地址中的 TCP 端口。'
}

update_panel() {
    [[ -x $BINARY && -f $UNIT_DIR/boxpanel.service && ! -e $DATA_DIR/.installing ]] || die '安装尚未完成；请先运行 install。'
    "$BINARY" info >/dev/null
    progress 3 '检查下载工具'
    for tool in curl tar; do command -v "$tool" >/dev/null || die "缺少 $tool；请通过系统包管理器安装后重试。"; done
    progress 4 '下载并校验新版本'
    stage_release
    if cmp -s "$STAGED_BINARY" "$BINARY"; then say '已是相同二进制，无需更新。'; return; fi
    progress 5 '暂存原二进制'
    cp -p -- "$BINARY" "$TEMP_DIR/boxpanel.previous"
    progress 6 '原子替换面板二进制'
    replace_binary
    progress 7 '重启面板并检查存活'
    if ! systemctl restart boxpanel.service || ! wait_panel; then
        STAGED_BINARY=$(mktemp "${BINARY%/*}/.boxpanel-restore.XXXXXX")
        cp -p -- "$TEMP_DIR/boxpanel.previous" "$STAGED_BINARY"
        replace_binary
        if systemctl restart boxpanel.service && wait_panel; then
            die '新版启动失败，已恢复原二进制；请检查 journalctl -u boxpanel 后重试。'
        fi
        die '新版启动失败且旧版未恢复运行；已恢复原二进制，请检查数据库兼容性和 journalctl -u boxpanel。'
    fi
    progress 8 '更新完成'
    "$BINARY" info
}

uninstall_panel() {
    if [[ $YES != true ]]; then
        [[ $NON_INTERACTIVE != true ]] || die '非交互卸载需显式 --yes；默认保留数据，删除数据另加 --purge。'
        confirm '卸载会停止面板及所有代理连接，确认卸载？' || { say '已取消。'; return; }
    fi
    if [[ $NON_INTERACTIVE != true && $PURGE == false ]]; then
        if confirm '默认保留 /etc/boxpanel；是否改为永久删除这些数据？'; then PURGE=true; fi
    fi
    if [[ ! -e $BINARY && ! -e $UNIT_DIR/boxpanel.service && ! -e $UNIT_DIR/boxpanel-singbox.service && ! -e $CORE_BINARY ]]; then
        say 'boxpanel 已卸载。'
        if [[ $PURGE == true ]]; then rm -rf -- "$DATA_DIR"; else say "数据目录保留在 ${DATA_DIR}。"; fi
        return
    fi
    [[ -x $BINARY ]] || die '缺少可用 CLI，无法安全清理规则；请先运行 install 恢复二进制后重试卸载。'
    progress 3 '停止并禁用受管服务'
    local unit state
    for unit in boxpanel.service boxpanel-singbox.service; do
        state=$(systemctl show --property=LoadState --value "$unit")
        if [[ $state != not-found ]]; then
            systemctl stop "$unit"
            systemctl disable "$unit"
        fi
    done
    progress 4 '通过 CLI 清理端口和 UDP 跳跃规则'
    if [[ -e $DATA_DIR/boxpanel.db ]]; then "$BINARY" system firewall-cleanup; fi
    progress 5 '删除受管单元'
    rm -f -- "$UNIT_DIR/boxpanel.service" "$UNIT_DIR/boxpanel-singbox.service"
    systemctl daemon-reload
    progress 6 '删除受管二进制'
    rm -f -- "$CORE_BINARY" "$BINARY"
    progress 7 '处理数据目录'
    if [[ $PURGE == true ]]; then
        rm -rf -- "$DATA_DIR"
    else
        rm -f -- "$DATA_DIR/.installing"
        say "数据保留在 ${DATA_DIR}；重新安装将复用原设置和凭证。"
    fi
    progress 8 '卸载完成；系统依赖与 BBR 设置保留'
}

main() {
    set -euo pipefail
    umask 077
    parse_args "$@"
    progress 1 '检测 Linux 发行版和架构'
    detect_platform
    if [[ $DRY_RUN == true ]]; then
        say "DRY-RUN: $COMMAND repo=$REPO version=${TAG:-latest} arch=$ARCH port=${PORT:-random}"
        say 'install: dependencies → download + SHA-256 → init → singbox install → apply → firewall-open → enable/restart → bbr → info'
        say 'update: download + SHA-256 → atomic replace → restart → health check (restore binary on failure)'
        say "uninstall: confirmation → stop/disable → firewall-cleanup → remove units/binaries → purge=$PURGE"
        return
    fi
    progress 2 '检查 root/systemd 并锁定安装操作'
    preflight
    mkdir -- "$LOCK_DIR" 2>/dev/null || die "另一个安装进程可能正在运行；确认没有运行后删除 $LOCK_DIR 再重试。"
    LOCKED=true
    trap cleanup EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    configure_cli
    case "$COMMAND" in
        install) install_panel ;;
        update) update_panel ;;
        uninstall) uninstall_panel ;;
    esac
}

# Also run when piped (curl ... | bash), where BASH_SOURCE[0] is empty.
if [[ ${BASH_SOURCE[0]:-$0} == "$0" ]]; then main "$@"; fi
