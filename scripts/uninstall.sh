#!/usr/bin/env bash
set -Eeuo pipefail
IFS=$'\n\t'

readonly SERVICE_NAME="sub2api-limit-portal"
readonly SERVICE_USER="sub2api-limit-portal"
readonly SERVICE_GROUP="sub2api-limit-portal"
readonly INSTALL_ROOT="/opt/sub2api5hlimit"
readonly BIN_DIR="${INSTALL_ROOT}/bin"
readonly BIN_PATH="${BIN_DIR}/sub2api-limit-portal"
readonly UPDATER_PATH="${BIN_DIR}/sub2api-limit-updater"
readonly UNINSTALL_PATH="${INSTALL_ROOT}/uninstall.sh"
readonly UNIT_PATH="/etc/systemd/system/sub2api-limit-portal.service"
readonly UPDATE_UNIT_PATH="/etc/systemd/system/sub2api-limit-portal-update.service"
readonly UPDATE_PATH_UNIT_PATH="/etc/systemd/system/sub2api-limit-portal-update.path"
readonly CONFIG_DIR="${INSTALL_ROOT}/config"
readonly STATE_DIR="${INSTALL_ROOT}/data"
readonly BACKUP_DIR="${INSTALL_ROOT}/backups"
readonly UPDATE_DIR="${INSTALL_ROOT}/update"
readonly UPDATE_REQUEST_PATH="${STATE_DIR}/update.request"
readonly UPDATE_LOCK_PATH="${UPDATE_DIR}/apply.lock"
readonly UPDATE_TRANSACTION_PATH="${UPDATE_DIR}/transaction.json"

purge=false
update_path_was_active=false
update_path_was_enabled=false
update_lock_fd=""
watcher_suspended=false
portal_suspended=false
portal_was_active=false
portal_was_enabled=false
uninstall_committed=false

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

wait_for_unit_stopped() {
	local unit_name=$1
	local unit_path=$2
	local state
	local attempt
	for ((attempt = 1; attempt <= 60; attempt++)); do
		state=$(systemctl is-active "$unit_name" 2>/dev/null || true)
		case "$state" in
		inactive|failed) return 0 ;;
		unknown)
			[[ ! -e "$unit_path" ]] && return 0
			;;
		esac
		sleep 1
	done
	printf 'unit %s did not reach a stopped state (last state: %s)\n' "$unit_name" "$state" >&2
	return 1
}

release_update_lock() {
	if [[ -n "$update_lock_fd" ]]; then
		flock -u "$update_lock_fd" >/dev/null 2>&1 || true
		exec {update_lock_fd}>&-
		update_lock_fd=""
	fi
}

restore_update_watcher() {
	[[ "$watcher_suspended" == true ]] || return 0
	if [[ ! -f "$UPDATE_PATH_UNIT_PATH" || -L "$UPDATE_PATH_UNIT_PATH" ]]; then
		printf 'cannot restore the automatic-update watcher because its unit is unavailable\n' >&2
		return 1
	fi
	local restore_failed=false
	systemctl daemon-reload || restore_failed=true
	if [[ "$update_path_was_enabled" == true ]]; then
		systemctl enable "${SERVICE_NAME}-update.path" >/dev/null || restore_failed=true
	else
		systemctl disable "${SERVICE_NAME}-update.path" >/dev/null || restore_failed=true
	fi
	if [[ "$update_path_was_active" == true ]]; then
		systemctl start "${SERVICE_NAME}-update.path" >/dev/null || restore_failed=true
	else
		systemctl stop "${SERVICE_NAME}-update.path" >/dev/null || restore_failed=true
	fi
	[[ "$restore_failed" == false ]] || return 1
	watcher_suspended=false
}

restore_portal_service() {
	[[ "$portal_suspended" == true ]] || return 0
	if [[ ! -f "$UNIT_PATH" || -L "$UNIT_PATH" ]]; then
		printf 'cannot restore the portal service because its unit is unavailable\n' >&2
		return 1
	fi
	local restore_failed=false
	systemctl daemon-reload || restore_failed=true
	if [[ "$portal_was_enabled" == true ]]; then
		systemctl enable "${SERVICE_NAME}.service" >/dev/null || restore_failed=true
	else
		systemctl disable "${SERVICE_NAME}.service" >/dev/null || restore_failed=true
	fi
	if [[ "$portal_was_active" == true ]]; then
		systemctl start "${SERVICE_NAME}.service" >/dev/null || restore_failed=true
	else
		systemctl stop "${SERVICE_NAME}.service" >/dev/null || restore_failed=true
	fi
	[[ "$restore_failed" == false ]] || return 1
	portal_suspended=false
}

finish() {
	local status=$?
	local cleanup_failed=false
	trap - EXIT
	set +e
	if [[ $status -ne 0 && "$uninstall_committed" != true && "$portal_suspended" == true ]]; then
		restore_portal_service || cleanup_failed=true
	fi
	release_update_lock
	if [[ $status -ne 0 && "$uninstall_committed" != true && "$watcher_suspended" == true ]]; then
		restore_update_watcher || cleanup_failed=true
	fi
	if [[ "$cleanup_failed" == true ]]; then
		printf 'CRITICAL: a suspended systemd unit could not be restored; inspect the portal and update watcher before retrying.\n' >&2
	fi
	exit "$status"
}
trap finish EXIT

validate_purge_path() {
	local target=$1
	case "$target" in
	"$INSTALL_ROOT" | "$BIN_DIR" | "$CONFIG_DIR" | "$STATE_DIR" | "$BACKUP_DIR" | "$UPDATE_DIR") ;;
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
command -v flock >/dev/null 2>&1 || fail "flock was not found"
[[ -r /dev/tty ]] || fail "an interactive terminal is required for explicit confirmation"
if systemctl is-active --quiet "${SERVICE_NAME}-update.service"; then
	fail "an automatic update is currently running; wait for it to finish before uninstalling"
fi
if [[ -e "$UPDATE_TRANSACTION_PATH" || -L "$UPDATE_TRANSACTION_PATH" ]]; then
	fail "an interrupted automatic update is awaiting recovery; let it finish before uninstalling"
fi
if systemctl is-active --quiet "${SERVICE_NAME}-update.path"; then
	update_path_was_active=true
fi
if systemctl is-enabled --quiet "${SERVICE_NAME}-update.path"; then
	update_path_was_enabled=true
fi
if systemctl is-active --quiet "${SERVICE_NAME}.service"; then
	portal_was_active=true
fi
if systemctl is-enabled --quiet "${SERVICE_NAME}.service"; then
	portal_was_enabled=true
fi

if [[ -e "$INSTALL_ROOT" || -L "$INSTALL_ROOT" ]]; then
	[[ -d "$INSTALL_ROOT" && ! -L "$INSTALL_ROOT" ]] || fail "refusing non-directory installation root: ${INSTALL_ROOT}"
	resolved_root=$(readlink -f -- "$INSTALL_ROOT")
	[[ "$resolved_root" == "$INSTALL_ROOT" ]] || fail "installation root resolved outside its expected location: ${INSTALL_ROOT}"
fi
if [[ -e "$UPDATE_DIR" || -L "$UPDATE_DIR" ]]; then
	[[ -d "$UPDATE_DIR" && ! -L "$UPDATE_DIR" ]] || fail "refusing non-directory update metadata path: ${UPDATE_DIR}"
	resolved_update_dir=$(readlink -f -- "$UPDATE_DIR")
	[[ "$resolved_update_dir" == "$UPDATE_DIR" ]] || fail "update metadata path resolved outside its expected location"
fi
for file_target in "$BIN_PATH" "$UPDATER_PATH" "$UNINSTALL_PATH" "$UNIT_PATH" "$UPDATE_UNIT_PATH" "$UPDATE_PATH_UNIT_PATH" \
	"$UPDATE_REQUEST_PATH" "${UPDATE_DIR}/status.json" "$UPDATE_TRANSACTION_PATH" "${UPDATE_DIR}/apply.lock"; do
	if [[ -e "$file_target" || -L "$file_target" ]]; then
		[[ -f "$file_target" && ! -L "$file_target" ]] || fail "refusing unexpected program or metadata target: ${file_target}"
	fi
done

printf 'The following program files will be removed:\n'
printf '  %s\n  %s\n  %s\n' "$BIN_PATH" "$UPDATER_PATH" "$UNINSTALL_PATH"
printf '  %s\n  %s\n  %s\n' "$UNIT_PATH" "$UPDATE_UNIT_PATH" "$UPDATE_PATH_UNIT_PATH"
if [[ "$purge" == true ]]; then
	printf 'The following data will also be permanently deleted:\n'
	printf '  %s\n  %s\n  %s\n  %s\n' "$CONFIG_DIR" "$STATE_DIR" "$BACKUP_DIR" "$UPDATE_DIR"
	expected_reply="PURGE sub2api-limit-portal"
else
	printf 'Configuration, database and backups will be preserved.\n'
	expected_reply="UNINSTALL sub2api-limit-portal"
fi
printf 'Type exactly "%s" to continue: ' "$expected_reply" >/dev/tty
read -r reply </dev/tty
[[ "$reply" == "$expected_reply" ]] || fail "confirmation did not match; nothing was removed"

if [[ "$purge" == true ]]; then
	for target in "$INSTALL_ROOT" "$BIN_DIR" "$CONFIG_DIR" "$STATE_DIR" "$BACKUP_DIR" "$UPDATE_DIR"; do
		validate_purge_path "$target"
	done
fi

watcher_suspended=true
if ! systemctl stop "${SERVICE_NAME}-update.path" >/dev/null 2>&1; then
	wait_for_unit_stopped "${SERVICE_NAME}-update.path" "$UPDATE_PATH_UNIT_PATH" || fail "automatic-update watcher could not be stopped"
fi
wait_for_unit_stopped "${SERVICE_NAME}-update.path" "$UPDATE_PATH_UNIT_PATH" || fail "automatic-update watcher is still running"
systemctl disable "${SERVICE_NAME}-update.path" >/dev/null || fail "automatic-update watcher could not be disabled"
if [[ -d "$UPDATE_DIR" ]]; then
	exec {update_lock_fd}>"$UPDATE_LOCK_PATH"
	if ! flock -n "$update_lock_fd"; then
		fail "an automatic update is running; it was not interrupted"
	fi
fi
wait_for_unit_stopped "${SERVICE_NAME}-update.service" "$UPDATE_UNIT_PATH" || fail "automatic updater is still running"
if [[ -e "$UPDATE_TRANSACTION_PATH" || -L "$UPDATE_TRANSACTION_PATH" ]]; then
	fail "an interrupted automatic update is awaiting recovery; let it finish before uninstalling"
fi
portal_suspended=true
if ! systemctl stop "${SERVICE_NAME}.service" >/dev/null 2>&1; then
	wait_for_unit_stopped "${SERVICE_NAME}.service" "$UNIT_PATH" || fail "portal service could not be stopped"
fi
wait_for_unit_stopped "${SERVICE_NAME}.service" "$UNIT_PATH" || fail "portal service is still running"
systemctl disable "${SERVICE_NAME}.service" >/dev/null || fail "portal service could not be disabled"
for target in "$BIN_PATH" "$UPDATER_PATH" "$UNIT_PATH" "$UPDATE_UNIT_PATH" "$UPDATE_PATH_UNIT_PATH"; do
	if [[ -e "$target" || -L "$target" ]]; then
		[[ ! -d "$target" ]] || fail "refusing to remove directory at file target: ${target}"
		rm -f -- "$target"
		uninstall_committed=true
	fi
done
if [[ -e "$UPDATE_REQUEST_PATH" || -L "$UPDATE_REQUEST_PATH" ]]; then
	[[ -f "$UPDATE_REQUEST_PATH" && ! -L "$UPDATE_REQUEST_PATH" ]] || fail "refusing unexpected update request target: ${UPDATE_REQUEST_PATH}"
	rm -f -- "$UPDATE_REQUEST_PATH"
fi
if [[ -d "$UPDATE_DIR" && ! -L "$UPDATE_DIR" ]]; then
	for update_file in "${UPDATE_DIR}/status.json" "$UPDATE_TRANSACTION_PATH"; do
		if [[ -e "$update_file" || -L "$update_file" ]]; then
			[[ -f "$update_file" && ! -L "$update_file" ]] || fail "refusing unexpected update metadata target: ${update_file}"
			rm -f -- "$update_file"
		fi
	done
	rmdir -- "$UPDATE_DIR" 2>/dev/null || true
fi
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
release_update_lock
