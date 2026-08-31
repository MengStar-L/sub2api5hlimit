#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'

readonly SERVICE_NAME="sub2api-limit-portal"
readonly SERVICE_USER="sub2api-limit-portal"
readonly SERVICE_GROUP="sub2api-limit-portal"
readonly INSTALL_ROOT="/opt/sub2api5hlimit"
readonly BIN_DIR="${INSTALL_ROOT}/bin"
readonly BIN_PATH="${BIN_DIR}/sub2api-limit-portal"
readonly UNINSTALL_PATH="${INSTALL_ROOT}/uninstall.sh"
readonly UNIT_PATH="/etc/systemd/system/sub2api-limit-portal.service"
readonly CONFIG_DIR="${INSTALL_ROOT}/config"
readonly STATE_DIR="${INSTALL_ROOT}/data"
readonly BACKUP_DIR="${INSTALL_ROOT}/backups"

purge=false

usage() {
	cat <<'USAGE'
Usage: sudo /opt/sub2api5hlimit/uninstall.sh [--purge]

Without --purge, removes the binary and systemd unit while preserving the
configuration, database, backups and service account. --purge permanently
removes all of them after a stronger confirmation.
USAGE
}

fail() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

validate_purge_path() {
	local target=$1
	case "$target" in
	"$INSTALL_ROOT" | "$BIN_DIR" | "$CONFIG_DIR" | "$STATE_DIR" | "$BACKUP_DIR") ;;
	*) fail "internal path validation rejected: ${target}" ;;
	esac
	[[ "$target" != "/" && "$target" != "/opt" ]] || fail "refusing broad purge path: ${target}"
	[[ ! -L "$target" ]] || fail "refusing to purge a symbolic link: ${target}"
	resolved=$(readlink -m -- "$target")
	[[ "$resolved" == "$target" ]] || fail "purge path resolved outside its expected location: ${target}"
	if [[ -e "$target" ]] && mountpoint -q -- "$target"; then
		fail "refusing to purge a mount point: ${target}"
	fi
}

while [[ $# -gt 0 ]]; do
	case "$1" in
	--purge)
		purge=true
		shift
		;;
	--help | -h)
		usage
		exit 0
		;;
	*)
		fail "unknown argument: $1"
		;;
	esac
done

[[ $(id -u) -eq 0 ]] || fail "run this script as root (for example, with sudo)"
[[ $(uname -s) == "Linux" ]] || fail "this uninstaller only supports Linux"
command -v systemctl >/dev/null 2>&1 || fail "systemctl was not found"
command -v readlink >/dev/null 2>&1 || fail "readlink was not found"
command -v getent >/dev/null 2>&1 || fail "getent was not found"
command -v mountpoint >/dev/null 2>&1 || fail "mountpoint was not found"
[[ -r /dev/tty ]] || fail "an interactive terminal is required for explicit confirmation"

if [[ -e "$INSTALL_ROOT" || -L "$INSTALL_ROOT" ]]; then
	[[ -d "$INSTALL_ROOT" && ! -L "$INSTALL_ROOT" ]] || fail "refusing non-directory installation root: ${INSTALL_ROOT}"
	resolved_root=$(readlink -f -- "$INSTALL_ROOT")
	[[ "$resolved_root" == "$INSTALL_ROOT" ]] || fail "installation root resolved outside its expected location: ${INSTALL_ROOT}"
fi

printf 'The following program files will be removed:\n'
printf '  %s\n  %s\n  %s\n' "$BIN_PATH" "$UNINSTALL_PATH" "$UNIT_PATH"
if [[ "$purge" == true ]]; then
	printf 'The following data will also be permanently deleted:\n'
	printf '  %s\n  %s\n  %s\n' "$CONFIG_DIR" "$STATE_DIR" "$BACKUP_DIR"
	expected_reply="PURGE sub2api-limit-portal"
else
	printf 'Configuration, database and backups will be preserved.\n'
	expected_reply="UNINSTALL sub2api-limit-portal"
fi
printf 'Type exactly "%s" to continue: ' "$expected_reply" >/dev/tty
read -r reply </dev/tty
[[ "$reply" == "$expected_reply" ]] || fail "confirmation did not match; nothing was removed"

if [[ "$purge" == true ]]; then
	for target in "$INSTALL_ROOT" "$BIN_DIR" "$CONFIG_DIR" "$STATE_DIR" "$BACKUP_DIR"; do
		validate_purge_path "$target"
	done
fi

systemctl stop "${SERVICE_NAME}.service" >/dev/null 2>&1 || true
systemctl disable "${SERVICE_NAME}.service" >/dev/null 2>&1 || true

for target in "$BIN_PATH" "$UNIT_PATH"; do
	if [[ -e "$target" || -L "$target" ]]; then
		[[ ! -d "$target" ]] || fail "refusing to remove directory at file target: ${target}"
		rm -f -- "$target"
	fi
done
# Removing the running script is safe on Linux; its open file remains readable
# until this process exits.
if [[ -e "$UNINSTALL_PATH" || -L "$UNINSTALL_PATH" ]]; then
	[[ ! -d "$UNINSTALL_PATH" ]] || fail "refusing to remove directory at uninstall target"
	rm -f -- "$UNINSTALL_PATH"
fi
systemctl daemon-reload

if [[ "$purge" == true ]]; then
	rm -rf --one-file-system -- "$INSTALL_ROOT"
	if id "$SERVICE_USER" >/dev/null 2>&1; then
		userdel "$SERVICE_USER"
	fi
	if getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
		groupdel "$SERVICE_GROUP"
	fi
	printf 'Program, configuration, database, backups and service account were removed.\n'
else
	printf 'Program files were removed. Preserved data:\n'
	printf '  %s\n  %s\n  %s\n' "$CONFIG_DIR" "$STATE_DIR" "$BACKUP_DIR"
	printf 'Reinstalling will reuse the existing environment and database.\n'
fi
