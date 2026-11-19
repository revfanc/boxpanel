#!/usr/bin/env bash
# Offline black-box tests: real tar/hash/files, mocked OS/network/services/CLI.
set -euo pipefail
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
contains() { grep -Fq -- "$2" "$1" || fail "missing '$2' in $1"; }
absent() { if grep -Fq -- "$2" "$1"; then fail "unexpected '$2' in $1"; fi; }
reject() { if bash "$FIXTURE/driver" "$@" >"$FIXTURE/output" 2>&1; then fail "accepted: $*"; fi; }
run_install() { bash "$FIXTURE/driver" "$@" >"$FIXTURE/output" 2>&1; }

archive() {
    local tag=$1
    sed "s/@TAG@/$tag/g" "$FIXTURE/cli-template" >"$FIXTURE/release/boxpanel"
    chmod 0755 "$FIXTURE/release/boxpanel"
    (cd "$FIXTURE/release" && tar -czf boxpanel-linux-amd64.tar.gz boxpanel)
    checksum
}

checksum() {
    (cd "$FIXTURE/release" && if command -v sha256sum >/dev/null; then
        sha256sum boxpanel-linux-amd64.tar.gz
    else shasum -a 256 boxpanel-linux-amd64.tar.gz; fi) >"$FIXTURE/release/checksums.txt"
}

fixture() {
    FIXTURE=$(mktemp -d)
    export FIXTURE INSTALL_SCRIPT="$SCRIPT_DIR/install.sh"
    trap 'status=$?; if ((status != 0)) && [[ -f $FIXTURE/output ]]; then cat "$FIXTURE/output"; fi; rm -rf -- "$FIXTURE"' EXIT
    mkdir -p "$FIXTURE/bin" "$FIXTURE/release" "$FIXTURE/systemd" "$FIXTURE/units"
    : >"$FIXTURE/calls"
    printf 'ID=ubuntu\nVERSION_ID="24.04"\n' >"$FIXTURE/os-release"
    cat >"$FIXTURE/driver" <<'EOF'
#!/usr/bin/env bash
# shellcheck source=scripts/install.sh
. "$INSTALL_SCRIPT"
BINARY="$FIXTURE/local/bin/boxpanel"
DATA_DIR="$FIXTURE/data"
CORE_BINARY="$FIXTURE/local/share/sing-box"
UNIT_DIR="$FIXTURE/units"
LOCK_DIR="$FIXTURE/lock"
OS_RELEASE="$FIXTURE/os-release"
SYSTEMD_DIR="$FIXTURE/systemd"
export PATH="$FIXTURE/bin:$PATH"
main "$@"
EOF
    cat >"$FIXTURE/bin/uname" <<'EOF'
#!/usr/bin/env bash
if [[ $1 == -s ]]; then echo "${MOCK_KERNEL:-Linux}"; else echo "${MOCK_ARCH:-x86_64}"; fi
EOF
    cat >"$FIXTURE/bin/id" <<'EOF'
#!/usr/bin/env bash
echo "${MOCK_UID:-0}"
EOF
    cat >"$FIXTURE/bin/systemctl" <<'EOF'
#!/usr/bin/env bash
set -eu
echo "systemctl $*" >>"$FIXTURE/calls"
if [[ ${FAIL_SYSTEMCTL:-} == "$1" ]]; then exit 1; fi
if [[ $1 == restart && -f $FIXTURE/fail-restart-once ]]; then rm "$FIXTURE/fail-restart-once"; exit 1; fi
if [[ $1 == show ]]; then echo loaded; fi
EOF
    cat >"$FIXTURE/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -eu
echo "curl $*" >>"$FIXTURE/calls"
[[ ${FAIL_DOWNLOAD:-false} != true ]] || exit 22
dest=''
while (($#)); do
    if [[ $1 == --output ]]; then dest=$2; shift 2; else url=$1; shift; fi
done
if [[ $url == */latest ]]; then echo "https://github.com/revfanc/boxpanel/releases/tag/v0.1.0"; else cp "$FIXTURE/release/${url##*/}" "$dest"; fi
EOF
    cat >"$FIXTURE/bin/apt-get" <<'EOF'
#!/usr/bin/env bash
echo "${0##*/} $*" >>"$FIXTURE/calls"
if [[ -f $FIXTURE/package-error ]]; then cat "$FIXTURE/package-error"; exit 1; fi
if [[ -f $FIXTURE/package-locked && ( -z ${LOCK_OPERATION:-} || " $* " == *" ${LOCK_OPERATION} "* ) ]]; then
    count=$(cat "$FIXTURE/package-locked")
    if ((count > 0)); then
        echo $((count - 1)) >"$FIXTURE/package-locked"
        if [[ ${0##*/} == dnf ]]; then
            echo 'metadata already locked by 123'
            exit 200
        else
            echo 'E: Could not get lock /var/lib/apt/lists/lock. It is held by process 123 (apt-get)'
        fi
        exit 1
    fi
fi
EOF
    cp "$FIXTURE/bin/apt-get" "$FIXTURE/bin/dnf"
    # Avoid sleeps or host firewall discovery in tests.
    printf '#!/usr/bin/env bash\nexit 0\n' >"$FIXTURE/bin/sleep"
    cp "$FIXTURE/bin/sleep" "$FIXTURE/bin/nft"
    chmod +x "$FIXTURE/bin/"*
    cat >"$FIXTURE/cli-template" <<'EOF'
#!/usr/bin/env bash
set -eu
echo "cli @TAG@ $*" >>"$FIXTURE/calls"
[[ ${FAIL_CLI:-} != "$*" ]] || exit 1
if [[ $* == 'system ready' && ${FAIL_READY_TAG:-} == '@TAG@' ]]; then exit 1; fi
case "$1" in
    version) echo 'boxpanel @TAG@' ;;
    info) [[ -s $BOXPANEL_DB_PATH ]] || exit 1; echo 'panel address and username' ;;
    init) [[ ! -s $BOXPANEL_DB_PATH ]] || exit 1; echo initialized >"$BOXPANEL_DB_PATH"; echo 'one-time password' ;;
    join) echo agent >"$BOXPANEL_DB_PATH" ;;
    singbox)
        mkdir -p "${BOXPANEL_SINGBOX_PATH%/*}"
        echo core >"$BOXPANEL_SINGBOX_PATH"
        touch "$BOXPANEL_UNIT_DIR/boxpanel.service" "$BOXPANEL_UNIT_DIR/boxpanel-singbox.service" ;;
    restore)
        [[ $* == *--install* ]] || exit 1
        echo restored >"$BOXPANEL_DB_PATH"
        mkdir -p "${BOXPANEL_SINGBOX_PATH%/*}"
        echo original-core >"$BOXPANEL_SINGBOX_PATH"
        touch "$BOXPANEL_UNIT_DIR/boxpanel.service" "$BOXPANEL_UNIT_DIR/boxpanel-singbox.service" ;;
    apply|system) : ;;
    *) exit 99 ;;
esac
EOF
    archive v0.1.0
}

test_install() {
    run_install --non-interactive --port 23456
    contains "$FIXTURE/calls" 'init --port 23456'
    contains "$FIXTURE/calls" 'system firewall-open'
    contains "$FIXTURE/calls" 'system bbr'
    contains "$FIXTURE/calls" 'system ready'
    contains "$FIXTURE/output" 'boxpanel set-host'
    [[ ! -e $FIXTURE/data/.installing && ! -e $FIXTURE/lock ]] || fail 'unfinished installation'
    local first
    first=$(grep -c '^curl ' "$FIXTURE/calls")
    run_install install --non-interactive
    [[ $(grep -c '^curl ' "$FIXTURE/calls") == "$first" ]] || fail 'repeat downloaded again'
    [[ $(grep -c 'cli .* init' "$FIXTURE/calls") == 1 ]] || fail 'repeat initialized again'
    contains "$FIXTURE/output" 'update'
    contains "$FIXTURE/output" 'boxpanel set-host'
}

test_resume() {
    export FAIL_CLI='singbox install --preserve-version'
    reject install --non-interactive --version v0.1.0
    [[ -f $FIXTURE/data/.installing && ! -e $FIXTURE/lock ]] || fail 'missing retry marker or stale lock'
    unset FAIL_CLI
    run_install install --non-interactive --version v0.1.0
    [[ $(grep -c 'cli .* init' "$FIXTURE/calls") == 1 ]] || fail 'resume reset credentials'
}

test_update() {
    run_install install --non-interactive
    cp "$FIXTURE/data/boxpanel.db" "$FIXTURE/db.before"
    cp "$FIXTURE/local/share/sing-box" "$FIXTURE/core.before"
    archive v0.2.0
    : >"$FIXTURE/calls"
    run_install update --version v0.2.0 --non-interactive
    contains "$FIXTURE/local/bin/boxpanel" 'boxpanel v0.2.0'
    cmp "$FIXTURE/db.before" "$FIXTURE/data/boxpanel.db"
    cmp "$FIXTURE/core.before" "$FIXTURE/local/share/sing-box"
    absent "$FIXTURE/calls" 'singbox install'
    absent "$FIXTURE/calls" ' init'
    archive v0.3.0
    touch "$FIXTURE/fail-restart-once"
    reject update --version v0.3.0 --non-interactive
    contains "$FIXTURE/local/bin/boxpanel" 'boxpanel v0.2.0'
    contains "$FIXTURE/output" '已恢复原二进制'
    export FAIL_READY_TAG=v0.3.0
    reject update --version v0.3.0 --non-interactive
    unset FAIL_READY_TAG
    contains "$FIXTURE/local/bin/boxpanel" 'boxpanel v0.2.0'
    contains "$FIXTURE/output" '已恢复原二进制'
    [[ ! -e $FIXTURE/lock ]] || fail 'failed update leaked lock'
}

test_uninstall() {
    run_install install --non-interactive
    reject uninstall --non-interactive
    [[ -x $FIXTURE/local/bin/boxpanel ]] || fail 'unconfirmed uninstall changed files'
    export FAIL_CLI='system firewall-cleanup'
    reject uninstall --non-interactive --yes
    [[ -f $FIXTURE/units/boxpanel.service ]] || fail 'deleted units before cleanup'
    unset FAIL_CLI
    run_install uninstall --non-interactive --yes
    [[ -s $FIXTURE/data/boxpanel.db && ! -e $FIXTURE/local/bin/boxpanel && ! -e $FIXTURE/local/share/sing-box ]] || fail 'uninstall result'
    run_install uninstall --non-interactive --yes
    run_install install --non-interactive
    [[ $(grep -c 'cli .* init' "$FIXTURE/calls") == 1 ]] || fail 'reinstall reset retained DB'
    run_install uninstall --non-interactive --yes --purge
    [[ ! -e $FIXTURE/data ]] || fail 'purge kept data'
}

test_piped_entry() {
    # curl ... | bash must run main (regression: it used to exit silently).
    local out
    out=$(bash -s -- --help <"$SCRIPT_DIR/install.sh") || fail 'piped --help failed'
    [[ $out == *'用法：'* ]] || fail 'piped invocation did not run main'
    out=$(bash <"$SCRIPT_DIR/install.sh" 2>&1) && fail 'piped install without root should fail' || true
    [[ -n $out ]] || fail 'piped install printed nothing'
}

test_rejections() {
    local invalid
    for invalid in 0 65536 abc -1 99999999999; do reject --port "$invalid"; done
    reject --version '../escape'
    reject --version
    reject update --port 2053
    reject install --purge
    reject install uninstall
    export MOCK_ARCH=riscv64; reject --dry-run; unset MOCK_ARCH
    export MOCK_KERNEL=Darwin; reject --dry-run; unset MOCK_KERNEL
    export MOCK_UID=501; reject install; unset MOCK_UID
    printf 'ID=debian\nVERSION_ID="10"\n' >"$FIXTURE/os-release"
    reject --dry-run
    printf 'ID=debian\nVERSION_ID="12"\n' >"$FIXTURE/os-release"
    export MOCK_ARCH=aarch64
    run_install --dry-run --version v0.1.0-rc.1
    contains "$FIXTURE/output" 'arch=arm64'
    unset MOCK_ARCH
    local distribution
    for distribution in 'debian 11' 'ubuntu 20.04' 'centos 9' 'rocky 8' 'almalinux 8'; do
        printf 'ID=%s\nVERSION_ID=%s\nNAME="CentOS Stream"\n' "${distribution% *}" "${distribution#* }" >"$FIXTURE/os-release"
        run_install --dry-run
    done
    rmdir "$FIXTURE/systemd"
    reject install
    absent "$FIXTURE/calls" 'curl '
    absent "$FIXTURE/calls" 'apt-get '
    absent "$FIXTURE/calls" 'cli '
    [[ ! -e $FIXTURE/local && ! -e $FIXTURE/lock ]] || fail 'dry run mutated system'
}

test_optional_bbr_and_lock() {
    export FAIL_CLI='system bbr'
    run_install install --non-interactive
    unset FAIL_CLI
    contains "$FIXTURE/output" 'BBR 未开启'
    [[ ! -e $FIXTURE/data/.installing ]] || fail 'BBR failure prevented completion'
    mkdir "$FIXTURE/lock"
    reject update --version v0.1.0
    [[ -d $FIXTURE/lock ]] || fail 'removed another installer lock'
}

test_download_safety() {
    run_install install --non-interactive
    cp "$FIXTURE/local/bin/boxpanel" "$FIXTURE/before"
    archive v0.2.0
    printf '%064d  boxpanel-linux-amd64.tar.gz\n' 0 >"$FIXTURE/release/checksums.txt"
    reject update --version v0.2.0
    cmp "$FIXTURE/before" "$FIXTURE/local/bin/boxpanel"
    checksum
    cp "$FIXTURE/release/checksums.txt" "$FIXTURE/hash"
    cat "$FIXTURE/hash" >>"$FIXTURE/release/checksums.txt"
    reject update --version v0.2.0
    printf '%064d  other.tar.gz\n' 0 >"$FIXTURE/release/checksums.txt"
    reject update --version v0.2.0
    archive v0.2.0
    export FAIL_DOWNLOAD=true
    reject update --version v0.2.0
    unset FAIL_DOWNLOAD
    # Even a correctly hashed archive must not contain links or extra entries.
    rm "$FIXTURE/release/boxpanel"
    ln -s /etc/passwd "$FIXTURE/release/boxpanel"
    (cd "$FIXTURE/release" && tar -czf boxpanel-linux-amd64.tar.gz boxpanel)
    checksum
    reject update --version v0.2.0
    rm "$FIXTURE/release/boxpanel"
    archive v0.2.0
    touch "$FIXTURE/release/extra"
    (cd "$FIXTURE/release" && tar -czf boxpanel-linux-amd64.tar.gz boxpanel extra)
    checksum
    reject update --version v0.2.0
    cmp "$FIXTURE/before" "$FIXTURE/local/bin/boxpanel"
    archive v0.2.0
    reject update --version v0.3.0
    cmp "$FIXTURE/before" "$FIXTURE/local/bin/boxpanel"
    [[ ! -e $FIXTURE/lock ]] || fail 'failure leaked lock'
    [[ $(find "$FIXTURE/local/bin" -name '.boxpanel-*' | wc -l | tr -d ' ') == 0 ]] || fail 'staging leaked'
}

test_package_locks() {
    echo 2 >"$FIXTURE/package-locked"
    run_install --non-interactive --version v0.1.0
    contains "$FIXTURE/output" '系统正在自动更新软件，请稍候'
    contains "$FIXTURE/calls" 'apt-get -o DPkg::Lock::Timeout=0 install'
    [[ $(grep -c 'apt-get .* update' "$FIXTURE/calls") == 3 ]] || fail 'apt lock not retried'
}

test_apt_install_lock() {
    export LOCK_OPERATION=install
    echo 2 >"$FIXTURE/package-locked"
    run_install --non-interactive --version v0.1.0
    contains "$FIXTURE/output" '系统正在自动更新软件，请稍候'
    [[ $(grep -c 'apt-get .* install' "$FIXTURE/calls") == 3 ]] || fail 'apt install lock not retried'
    [[ $(grep -c 'apt-get .* update' "$FIXTURE/calls") == 1 ]] || fail 'apt update retried without lock'
}

test_package_lock_timeout() {
    echo 100 >"$FIXTURE/package-locked"
    reject --non-interactive --version v0.1.0
    contains "$FIXTURE/output" '等待软件管理器锁已超过 5 分钟'
    contains "$FIXTURE/output" '检查 apt-daily/dnf 服务状态'
    if grep -q '^curl ' "$FIXTURE/calls"; then fail 'downloaded after lock timeout'; fi
    [[ $(grep -c '^apt-get ' "$FIXTURE/calls") == 61 ]] || fail 'lock wait not bounded'
}

test_dnf_locks() {
    printf 'ID=rocky\nVERSION_ID="9"\n' >"$FIXTURE/os-release"
    echo 2 >"$FIXTURE/package-locked"
    run_install --non-interactive --version v0.1.0
    contains "$FIXTURE/calls" 'dnf --setopt=exit_on_lock=True install'
    contains "$FIXTURE/output" '系统正在自动更新软件，请稍候'
    [[ $(grep -c '^dnf ' "$FIXTURE/calls") == 3 ]] || fail 'dnf lock not retried'
}

test_dnf_lock_timeout() {
    printf 'ID=rocky\nVERSION_ID="9"\n' >"$FIXTURE/os-release"
    echo 100 >"$FIXTURE/package-locked"
    reject --non-interactive --version v0.1.0
    contains "$FIXTURE/output" '等待软件管理器锁已超过 5 分钟'
    [[ $(grep -c '^dnf ' "$FIXTURE/calls") == 61 ]] || fail 'dnf wait not bounded'
    if grep -q '^curl ' "$FIXTURE/calls"; then fail 'downloaded after lock timeout'; fi
}

test_package_permission_error() {
    printf '%s\n' 'E: Could not get lock /var/lib/dpkg/lock-frontend (13: Permission denied)' 'E: Unable to acquire the dpkg frontend lock' >"$FIXTURE/package-error"
    reject --non-interactive --version v0.1.0
    contains "$FIXTURE/output" '请检查 root 权限和文件系统状态'
    absent "$FIXTURE/output" '系统正在自动更新软件'
    [[ $(grep -c '^apt-get ' "$FIXTURE/calls") == 1 ]] || fail 'permission failure retried'
}

test_package_nonlock_error() {
    echo 'E: repository unavailable' >"$FIXTURE/package-error"
    reject --non-interactive --version v0.1.0
    contains "$FIXTURE/output" '请检查软件源、网络和包管理器状态'
    absent "$FIXTURE/output" '系统正在自动更新软件'
    [[ $(grep -c '^apt-get ' "$FIXTURE/calls") == 1 ]] || fail 'non-lock failure retried'
}

test_nft_dependency() {
    rm "$FIXTURE/bin/nft"
    cp "$FIXTURE/bin/sleep" "$FIXTURE/bin/iptables"
    cp "$FIXTURE/bin/sleep" "$FIXTURE/bin/ip6tables"
    # Override discovery, independent of nft installed on the test host.
    cat >"$FIXTURE/dependencies" <<'EOF_DEP'
. "$INSTALL_SCRIPT"
command() {
    if [[ $* == '-v nft' ]]; then return 1; fi
    builtin command "$@"
}
export PATH="$FIXTURE/bin:$PATH"
PACKAGE_MANAGER=$1
install_dependencies
EOF_DEP
    bash "$FIXTURE/dependencies" apt
    contains "$FIXTURE/calls" 'nftables iproute2'
    : >"$FIXTURE/calls"
    bash "$FIXTURE/dependencies" dnf
    contains "$FIXTURE/calls" 'nftables iproute'
}

test_update_nft_dependency() {
    run_install --non-interactive --version v0.1.0
    archive v0.2.0
    # Force nft discovery to fail even if the host has it installed.
    sed '/main "\$@"/i\
command() { if [[ $* == "-v nft" ]]; then return 1; fi; builtin command "$@"; }\
' "$FIXTURE/driver" >"$FIXTURE/driver.next"
    mv "$FIXTURE/driver.next" "$FIXTURE/driver"
    : >"$FIXTURE/calls"
    run_install update --non-interactive --version v0.2.0
    contains "$FIXTURE/calls" 'nftables iproute2'
    absent "$FIXTURE/calls" 'singbox install'
}

test_restore_install() {
    echo encrypted-fixture >"$FIXTURE/backup.enc"
    echo 'secret-passphrase-never-log' >"$FIXTURE/pass"
    chmod 0600 "$FIXTURE/pass"
    run_install --restore "$FIXTURE/backup.enc" --passphrase-file "$FIXTURE/pass" --non-interactive
    contains "$FIXTURE/calls" "restore $FIXTURE/backup.enc --install --passphrase-file $FIXTURE/pass"
    contains "$FIXTURE/data/boxpanel.db" restored
    absent "$FIXTURE/calls" ' init'
    absent "$FIXTURE/calls" ' apply'
    absent "$FIXTURE/output" 'secret-passphrase-never-log'
    reject --restore "$FIXTURE/backup.enc" --non-interactive
    contains "$FIXTURE/output" '已有实例请使用 boxpanel restore'
}

test_restore_failure() {
    echo encrypted-fixture >"$FIXTURE/backup.enc"
    export FAIL_CLI="restore $FIXTURE/backup.enc --install"
    reject --restore "$FIXTURE/backup.enc" --non-interactive
    [[ ! -e $FIXTURE/data/boxpanel.db ]] || fail 'failed restore initialized DB'
    absent "$FIXTURE/calls" ' init'
    absent "$FIXTURE/calls" 'singbox install'
    reject update --restore "$FIXTURE/backup.enc"
    reject --restore "$FIXTURE/backup.enc" --port 2053
}

test_join_install() {
    local token pin
    printf -v token '%043d' 0
    printf -v pin '%043d' 1
    run_install --join 'https://[2001:db8::10]:2053/mainpanel/' --token "$token" --master-pin "$pin" </dev/null
    contains "$FIXTURE/calls" 'singbox install --for-join --preserve-version'
    contains "$FIXTURE/calls" "join --master https://[2001:db8::10]:2053/mainpanel/ --token $token --master-pin $pin"
    contains "$FIXTURE/calls" 'systemctl restart boxpanel.service'
    contains "$FIXTURE/calls" 'system ready'
    contains "$FIXTURE/data/boxpanel.db" agent
    absent "$FIXTURE/calls" ' init'
    absent "$FIXTURE/calls" ' apply'
    absent "$FIXTURE/calls" 'firewall-open'
    absent "$FIXTURE/output" "$token"
    absent "$FIXTURE/output" "$pin"
    reject --join https://master.test/panel --token "$token"
    [[ $(grep -c 'cli .* join --master' "$FIXTURE/calls") == 1 ]] || fail 'consumed a second token on an initialized node'
}

test_join_dry_run() {
    local token
    printf -v token '%043d' 0
    run_install --join https://master.test/panel/ --token "$token" --dry-run
    contains "$FIXTURE/output" 'join: dependencies'
    contains "$FIXTURE/output" '(non-interactive)'
    absent "$FIXTURE/output" "$token"
    absent "$FIXTURE/output" 'firewall-open'
    reject --join http://master.test/panel --token "$token" --dry-run
    reject --join 'https://master.test/panel?x=1' --token "$token" --dry-run
    reject --join https://master.test:65536/panel --token "$token" --dry-run
    reject --join https://master.test/panel --dry-run
    reject --join https://master.test/panel --token "$token" --master-pin short --dry-run
    reject --join https://master.test/panel --token "$token" --port 2053
    reject --join https://master.test/panel --token "$token" --restore backup
    reject update --join https://master.test/panel --token "$token"
    reject --token "$token" --dry-run
    reject --master-pin "$token" --dry-run
    [[ ! -s $FIXTURE/calls && ! -e $FIXTURE/local && ! -e $FIXTURE/lock ]] || fail 'join dry-run modified system'
}

if [[ ${1:-} == --case ]]; then
    fixture
    "test_$2"
    exit 0
fi
for scenario in join_install join_dry_run restore_install restore_failure nft_dependency update_nft_dependency install resume update uninstall rejections download_safety optional_bbr_and_lock piped_entry package_locks apt_install_lock package_lock_timeout dnf_locks dnf_lock_timeout package_permission_error package_nonlock_error; do
    # A separate bash process preserves errexit in the installer under test.
    output=$(mktemp)
    if bash "$0" --case "$scenario" >"$output" 2>&1; then
        printf 'PASS install.sh: %s\n' "$scenario"
        rm "$output"
    else
        cat "$output"
        rm "$output"
        fail "$scenario"
    fi
done
