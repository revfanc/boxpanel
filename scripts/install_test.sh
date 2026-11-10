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
echo "apt-get $*" >>"$FIXTURE/calls"
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
    singbox)
        mkdir -p "${BOXPANEL_SINGBOX_PATH%/*}"
        echo core >"$BOXPANEL_SINGBOX_PATH"
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

if [[ ${1:-} == --case ]]; then
    fixture
    "test_$2"
    exit 0
fi
for scenario in install resume update uninstall rejections download_safety optional_bbr_and_lock piped_entry; do
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
