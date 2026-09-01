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
readonly ENV_PATH="${CONFIG_DIR}/sub2api-limit-portal.env"
readonly STATE_DIR="${INSTALL_ROOT}/data"
readonly DB_PATH="${STATE_DIR}/app.db"
readonly BACKUP_ROOT="${INSTALL_ROOT}/backups"
readonly UPDATE_DIR="${INSTALL_ROOT}/update"
readonly UPDATE_REQUEST_PATH="${STATE_DIR}/update.request"
readonly UPDATE_TRANSACTION_PATH="${UPDATE_DIR}/transaction.json"
readonly UPDATE_LOCK_PATH="${UPDATE_DIR}/apply.lock"

readonly LEGACY_BIN_PATH="/usr/local/bin/sub2api-limit-portal"
readonly LEGACY_UNINSTALL_PATH="/usr/local/sbin/sub2api-limit-portal-uninstall"
readonly LEGACY_CONFIG_DIR="/etc/sub2api-limit-portal"
readonly LEGACY_STATE_DIR="/var/lib/sub2api-limit-portal"
readonly LEGACY_BACKUP_ROOT="/var/backups/sub2api-limit-portal"

assume_yes=false
binary_arg=""
updater_binary_arg=""
verify_dir=""
deployment_started=false
rollback_files=false
db_backup_complete=false
had_binary=false
had_updater=false
had_unit=false
had_update_unit=false
had_update_path_unit=false
had_uninstaller=false
was_active=false
was_enabled=false
update_path_was_active=false
update_path_was_enabled=false
backup_dir=""
configured_listen="0.0.0.0:2556"
update_lock_fd=""
watcher_suspended=false
watcher_restore_blocked=false

usage() {
	cat <<'USAGE'
Usage: sudo bash ./scripts/install.sh [--binary FILE] [--updater-binary FILE] [--yes]

Installs or upgrades sub2api-limit-portal. Without --binary, the script selects
the portal and updater binaries for the current machine from dist/.

  --binary FILE          Use an explicit Linux portal binary.
  --updater-binary FILE  Use an explicit Linux updater binary.
  --yes                  Accept the displayed action non-interactively.
  --help                 Show this help.
USAGE
}

fail() {
	printf 'error: %s\n' "$*" >&2
	exit 1
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
	[[ "$watcher_restore_blocked" != true ]] || return 1
	if [[ "$update_path_was_active" == true ]]; then
		[[ -f "$UPDATE_PATH_UNIT_PATH" && ! -L "$UPDATE_PATH_UNIT_PATH" ]] || return 1
		systemctl start "${SERVICE_NAME}-update.path" >/dev/null 2>&1 || return 1
		systemctl is-active --quiet "${SERVICE_NAME}-update.path" || return 1
	fi
	watcher_suspended=false
}

start_update_watcher() {
	[[ -f "$UPDATE_PATH_UNIT_PATH" && ! -L "$UPDATE_PATH_UNIT_PATH" ]] || return 1
	systemctl start "${SERVICE_NAME}-update.path" || return 1
	systemctl is-active --quiet "${SERVICE_NAME}-update.path" || return 1
	watcher_suspended=false
}

portal_ready_url() {
	local listen=$1
	local host port_number port
	if [[ "$listen" == \"*\" && "$listen" == *\" ]]; then
		listen=${listen:1:${#listen}-2}
	elif [[ "$listen" == \'*\' && "$listen" == *\' ]]; then
		listen=${listen:1:${#listen}-2}
	fi
	if [[ "$listen" =~ ^\[([0-9A-Fa-f:]+)\]:([0-9]{1,5})$ ]]; then
		host=${BASH_REMATCH[1]}
		port=${BASH_REMATCH[2]}
	elif [[ "$listen" =~ ^([A-Za-z0-9._-]*):([0-9]{1,5})$ ]]; then
		host=${BASH_REMATCH[1]}
		port=${BASH_REMATCH[2]}
	else
		return 1
	fi
	port_number=$((10#$port))
	((port_number >= 1 && port_number <= 65535)) || return 1
	case "$host" in
	"" | "0.0.0.0") host="127.0.0.1" ;;
	"::") host="::1" ;;
	esac
	if [[ "$host" == *:* ]]; then
		printf 'http://[%s]:%d/readyz\n' "$host" "$port_number"
	else
		printf 'http://%s:%d/readyz\n' "$host" "$port_number"
	fi
}

wait_for_portal_ready() {
	local endpoint=$1
	local attempts=${2:-30}
	local attempt
	for ((attempt = 1; attempt <= attempts; attempt++)); do
		if systemctl is-active --quiet "${SERVICE_NAME}.service" &&
			curl --fail --silent --show-error --max-time 2 "$endpoint" >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	return 1
}

wait_for_unit_stopped() {
	local unit_name=$1
	local unit_path=$2
	local require_main_pid=${3:-false}
	local state main_pid attempt
	for ((attempt = 1; attempt <= 60; attempt++)); do
		state=$(systemctl is-active "$unit_name" 2>/dev/null || true)
		case "$state" in
		inactive | failed)
			if [[ "$require_main_pid" != true ]]; then
				return 0
			fi
			main_pid=$(systemctl show --property=MainPID --value "$unit_name" 2>/dev/null || true)
			if [[ "$main_pid" =~ ^[0-9]+$ ]] && { [[ "$main_pid" == 0 ]] || ! kill -0 "$main_pid" 2>/dev/null; }; then
				return 0
			fi
			;;
		unknown)
			[[ ! -e "$unit_path" ]] && return 0
			;;
		esac
		sleep 1
	done
	printf 'unit %s did not reach a stopped state (last state: %s)\n' "$unit_name" "$state" >&2
	return 1
}

restore_previous_install() {
	local rollback_failed=false
	trap 'rollback_failed=true' ERR
	set +e
	printf 'Installation did not complete; restoring the previous service files.\n' >&2
	if [[ "$rollback_files" == true ]]; then
		systemctl stop "${SERVICE_NAME}-update.path" >/dev/null 2>&1
		systemctl stop "${SERVICE_NAME}.service" >/dev/null 2>&1
		if ! wait_for_unit_stopped "${SERVICE_NAME}-update.path" "$UPDATE_PATH_UNIT_PATH" false ||
			! wait_for_unit_stopped "${SERVICE_NAME}.service" "$UNIT_PATH" true; then
			watcher_restore_blocked=true
			trap - ERR
			printf 'CRITICAL: rollback could not confirm that the portal and update watcher stopped; no database or program files were restored. Preserve backup %s and inspect both systemd units.\n' "$backup_dir" >&2
			return 0
		fi
		if [[ "$db_backup_complete" == true ]]; then
			for database_file in "$DB_PATH" "${DB_PATH}-wal" "${DB_PATH}-shm"; do
				rm -f -- "$database_file"
				database_name=$(basename -- "$database_file")
				if [[ -f "${backup_dir}/${database_name}" ]]; then
					cp --preserve=mode,ownership,timestamps -- "${backup_dir}/${database_name}" "$database_file"
				fi
			done
		fi
		if [[ "$had_binary" == true && -f "${backup_dir}/sub2api-limit-portal" ]]; then
			install -m 0755 -o root -g root "${backup_dir}/sub2api-limit-portal" "$BIN_PATH"
		else
			rm -f -- "$BIN_PATH"
		fi
		if [[ "$had_updater" == true && -f "${backup_dir}/sub2api-limit-updater" ]]; then
			install -m 0755 -o root -g root "${backup_dir}/sub2api-limit-updater" "$UPDATER_PATH"
		else
			rm -f -- "$UPDATER_PATH"
		fi
		if [[ "$had_unit" == true && -f "${backup_dir}/sub2api-limit-portal.service" ]]; then
			install -m 0644 -o root -g root "${backup_dir}/sub2api-limit-portal.service" "$UNIT_PATH"
		else
			rm -f -- "$UNIT_PATH"
		fi
		if [[ "$had_update_unit" == true && -f "${backup_dir}/sub2api-limit-portal-update.service" ]]; then
			install -m 0644 -o root -g root "${backup_dir}/sub2api-limit-portal-update.service" "$UPDATE_UNIT_PATH"
		else
			rm -f -- "$UPDATE_UNIT_PATH"
		fi
		if [[ "$had_update_path_unit" == true && -f "${backup_dir}/sub2api-limit-portal-update.path" ]]; then
			install -m 0644 -o root -g root "${backup_dir}/sub2api-limit-portal-update.path" "$UPDATE_PATH_UNIT_PATH"
		else
			rm -f -- "$UPDATE_PATH_UNIT_PATH"
		fi
		if [[ "$had_uninstaller" == true && -f "${backup_dir}/sub2api-limit-portal-uninstall" ]]; then
			install -m 0755 -o root -g root "${backup_dir}/sub2api-limit-portal-uninstall" "$UNINSTALL_PATH"
		else
			rm -f -- "$UNINSTALL_PATH"
		fi
	fi
	if [[ "$rollback_files" == true ]]; then
		systemctl daemon-reload >/dev/null 2>&1
		if [[ "$had_unit" == true ]]; then
			if [[ "$was_enabled" == true ]]; then
				systemctl enable "${SERVICE_NAME}.service" >/dev/null 2>&1
			else
				systemctl disable "${SERVICE_NAME}.service" >/dev/null 2>&1
			fi
			if [[ "$was_active" == true ]]; then
				systemctl start "${SERVICE_NAME}.service" >/dev/null 2>&1
				if [[ -n "${ready_endpoint:-}" ]] && ! wait_for_portal_ready "$ready_endpoint" 30; then
					rollback_failed=true
					printf 'Previous portal did not become ready after rollback.\n' >&2
					journalctl -u "${SERVICE_NAME}.service" -n 30 --no-pager >&2
				fi
			fi
		else
			systemctl disable "${SERVICE_NAME}.service" >/dev/null 2>&1
		fi
		if [[ "$had_update_path_unit" == true ]]; then
			if [[ "$update_path_was_enabled" == true ]]; then
				systemctl enable "${SERVICE_NAME}-update.path" >/dev/null 2>&1
			else
				systemctl disable "${SERVICE_NAME}-update.path" >/dev/null 2>&1
			fi
		else
			systemctl disable "${SERVICE_NAME}-update.path" >/dev/null 2>&1
		fi
	fi
	release_update_lock
	restore_update_watcher
	trap - ERR
	if [[ "$rollback_failed" == true ]]; then
		printf 'CRITICAL: installation rollback encountered errors; inspect the service journal and backup %s before retrying.\n' "$backup_dir" >&2
	fi
}

finish() {
	status=$?
	trap - EXIT
	set +e
	if [[ -n "$verify_dir" && -d "$verify_dir" ]]; then
		rm -f -- "${verify_dir}/sub2api-limit-portal" "${verify_dir}/sub2api-limit-updater"
		rmdir -- "$verify_dir" 2>/dev/null
	fi
	rm -f -- "${BIN_PATH}.new.$$" "${UPDATER_PATH}.new.$$" "${UNIT_PATH}.new.$$" \
		"${UPDATE_UNIT_PATH}.new.$$" "${UPDATE_PATH_UNIT_PATH}.new.$$" "${UNINSTALL_PATH}.new.$$" "${ENV_PATH}.new.$$"
	if [[ $status -ne 0 && "$deployment_started" == true ]]; then
		restore_previous_install
	fi
	release_update_lock
	if ! restore_update_watcher; then
		printf 'CRITICAL: the automatic-update watcher could not be restored; inspect its systemd unit before retrying.\n' >&2
	fi
	exit "$status"
}
trap finish EXIT

while [[ $# -gt 0 ]]; do
	case "$1" in
	--binary)
		[[ $# -ge 2 ]] || fail "--binary requires a file path"
		binary_arg=$2
		shift 2
		;;
	--updater-binary)
		[[ $# -ge 2 ]] || fail "--updater-binary requires a file path"
		updater_binary_arg=$2
		shift 2
		;;
	--yes)
		assume_yes=true
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
[[ $(uname -s) == "Linux" ]] || fail "this installer only supports Linux"

for command_name in install systemctl useradd groupadd getent readlink mktemp cp mv cut date basename journalctl curl flock; do
	command -v "$command_name" >/dev/null 2>&1 || fail "required command not found: ${command_name}"
done

# A legacy deployment contains the encryption key and database in paths that
# this layout must never silently replace. Only check while the new root is
# absent so a completed migration may retain old files for manual cleanup.
if [[ ! -e "$INSTALL_ROOT" && ! -L "$INSTALL_ROOT" ]]; then
	for legacy_path in \
		"$LEGACY_BIN_PATH" \
		"$LEGACY_UNINSTALL_PATH" \
		"$LEGACY_CONFIG_DIR" \
		"$LEGACY_STATE_DIR" \
		"$LEGACY_BACKUP_ROOT" \
		"$UNIT_PATH"; do
		if [[ -e "$legacy_path" || -L "$legacy_path" ]]; then
			fail "legacy installation detected at ${legacy_path}; migrate it to ${INSTALL_ROOT} before installing"
		fi
	done
fi

for managed_dir in "$INSTALL_ROOT" "$BIN_DIR" "$CONFIG_DIR" "$STATE_DIR" "$BACKUP_ROOT" "$UPDATE_DIR"; do
	if [[ -e "$managed_dir" || -L "$managed_dir" ]]; then
		[[ -d "$managed_dir" && ! -L "$managed_dir" ]] || fail "refusing non-directory managed path: ${managed_dir}"
		resolved_dir=$(readlink -f -- "$managed_dir")
		[[ "$resolved_dir" == "$managed_dir" ]] || fail "managed path resolved outside its expected location: ${managed_dir}"
	fi
done

case "$(uname -m)" in
x86_64 | amd64) machine_arch="amd64" ;;
aarch64 | arm64) machine_arch="arm64" ;;
*) fail "unsupported architecture: $(uname -m); expected amd64 or arm64" ;;
esac

script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_root=$(CDPATH='' cd -- "${script_dir}/.." && pwd -P)
unit_source="${repo_root}/packaging/sub2api-limit-portal.service"
update_unit_source="${repo_root}/packaging/sub2api-limit-portal-update.service"
update_path_unit_source="${repo_root}/packaging/sub2api-limit-portal-update.path"
uninstall_source="${repo_root}/scripts/uninstall.sh"
if [[ -n "$binary_arg" ]]; then
	binary_source=$binary_arg
else
	binary_source="${repo_root}/dist/sub2api-limit-portal-linux-${machine_arch}"
fi
if [[ -n "$updater_binary_arg" ]]; then
	updater_binary_source=$updater_binary_arg
else
	updater_binary_source="${repo_root}/dist/sub2api-limit-updater-linux-${machine_arch}"
fi

for source_path in "$binary_source" "$updater_binary_source" "$unit_source" "$update_unit_source" "$update_path_unit_source" "$uninstall_source"; do
	[[ -f "$source_path" ]] || fail "required file not found: ${source_path}"
	[[ ! -L "$source_path" ]] || fail "refusing symbolic-link source: ${source_path}"
done
binary_source=$(readlink -f -- "$binary_source")
updater_binary_source=$(readlink -f -- "$updater_binary_source")
unit_source=$(readlink -f -- "$unit_source")
update_unit_source=$(readlink -f -- "$update_unit_source")
update_path_unit_source=$(readlink -f -- "$update_path_unit_source")
uninstall_source=$(readlink -f -- "$uninstall_source")

# Running keygen proves that the selected artifact is executable on this host and
# gives a key for a first install without exposing it in process arguments.
verify_dir=$(mktemp -d -t sub2api-limit-portal.XXXXXXXX)
install -m 0755 -- "$binary_source" "${verify_dir}/sub2api-limit-portal"
install -m 0755 -- "$updater_binary_source" "${verify_dir}/sub2api-limit-updater"
key_assignment=$("${verify_dir}/sub2api-limit-portal" keygen) || fail "selected binary could not run keygen"
if [[ ! "$key_assignment" =~ ^SUB2API_LIMIT_MASTER_KEY=([A-Za-z0-9+/]{43}=)$ ]]; then
	fail "keygen returned an unexpected value"
fi
generated_master_key=${BASH_REMATCH[1]}
portal_version_output=$("${verify_dir}/sub2api-limit-portal" version) || fail "selected portal binary could not report its version"
updater_version_output=$("${verify_dir}/sub2api-limit-updater" version) || fail "selected updater binary could not report its version"
if [[ ! "$portal_version_output" =~ ^sub2api-limit-portal\ ([0-9A-Za-z.+-]+)\ \(linux/${machine_arch}\)$ ]]; then
	fail "selected portal binary returned unexpected version metadata"
fi
portal_version=${BASH_REMATCH[1]}
if [[ ! "$updater_version_output" =~ ^sub2api-limit-updater\ ([0-9A-Za-z.+-]+)\ \(linux/${machine_arch}\)$ ]]; then
	fail "selected updater binary returned unexpected version metadata"
fi
updater_version=${BASH_REMATCH[1]}
[[ "$portal_version" == "$updater_version" ]] || fail "portal and updater binary versions do not match"

if [[ -e "$BIN_PATH" ]]; then
	[[ -f "$BIN_PATH" && ! -L "$BIN_PATH" ]] || fail "refusing non-regular binary target: ${BIN_PATH}"
	had_binary=true
fi
if [[ -e "$UPDATER_PATH" ]]; then
	[[ -f "$UPDATER_PATH" && ! -L "$UPDATER_PATH" ]] || fail "refusing non-regular updater target: ${UPDATER_PATH}"
	had_updater=true
fi
if [[ -e "$UNIT_PATH" ]]; then
	[[ -f "$UNIT_PATH" && ! -L "$UNIT_PATH" ]] || fail "refusing non-regular unit target: ${UNIT_PATH}"
	had_unit=true
fi
if [[ -e "$UPDATE_UNIT_PATH" ]]; then
	[[ -f "$UPDATE_UNIT_PATH" && ! -L "$UPDATE_UNIT_PATH" ]] || fail "refusing non-regular update unit target: ${UPDATE_UNIT_PATH}"
	had_update_unit=true
fi
if [[ -e "$UPDATE_PATH_UNIT_PATH" ]]; then
	[[ -f "$UPDATE_PATH_UNIT_PATH" && ! -L "$UPDATE_PATH_UNIT_PATH" ]] || fail "refusing non-regular update path unit target: ${UPDATE_PATH_UNIT_PATH}"
	had_update_path_unit=true
fi
if [[ -e "$UNINSTALL_PATH" ]]; then
	[[ -f "$UNINSTALL_PATH" && ! -L "$UNINSTALL_PATH" ]] || fail "refusing non-regular uninstall target: ${UNINSTALL_PATH}"
	had_uninstaller=true
fi
if [[ -e "$ENV_PATH" ]]; then
	[[ -f "$ENV_PATH" && ! -L "$ENV_PATH" ]] || fail "refusing non-regular environment file: ${ENV_PATH}"
	configured_db_path=""
	while IFS= read -r env_line || [[ -n "$env_line" ]]; do
		env_line=${env_line%$'\r'}
		case "$env_line" in
		SUB2API_LIMIT_DB_PATH=*) configured_db_path=${env_line#*=} ;;
		SUB2API_LIMIT_LISTEN=*) configured_listen=${env_line#*=} ;;
		esac
	done <"$ENV_PATH"
	if [[ "$configured_db_path" != "$DB_PATH" &&
		"$configured_db_path" != "\"${DB_PATH}\"" &&
		"$configured_db_path" != "'${DB_PATH}'" ]]; then
		fail "the packaged systemd unit only permits SUB2API_LIMIT_DB_PATH=${DB_PATH}"
	fi
fi
ready_endpoint=$(portal_ready_url "$configured_listen") || fail "invalid SUB2API_LIMIT_LISTEN in ${ENV_PATH}: ${configured_listen}"
if systemctl is-active --quiet "${SERVICE_NAME}.service"; then
	was_active=true
fi
if systemctl is-enabled --quiet "${SERVICE_NAME}.service"; then
	was_enabled=true
fi
if systemctl is-active --quiet "${SERVICE_NAME}-update.service"; then
	fail "an automatic update is currently running; wait for it to finish before using the installer"
fi
if [[ -e "$UPDATE_REQUEST_PATH" || -L "$UPDATE_REQUEST_PATH" || -e "$UPDATE_TRANSACTION_PATH" || -L "$UPDATE_TRANSACTION_PATH" ]]; then
	fail "an automatic update or recovery is pending; let it finish before installing manually"
fi
if systemctl is-active --quiet "${SERVICE_NAME}-update.path"; then
	update_path_was_active=true
fi
if systemctl is-enabled --quiet "${SERVICE_NAME}-update.path"; then
	update_path_was_enabled=true
fi

if [[ "$had_unit" == true || "$had_binary" == true ]]; then
	action="upgrade"
else
	action="install"
fi
printf '%s sub2api-limit-portal (%s)\n' "$action" "$machine_arch"
printf '  binary: %s -> %s\n' "$binary_source" "$BIN_PATH"
printf '  updater: %s -> %s\n' "$updater_binary_source" "$UPDATER_PATH"
printf '  config: %s (preserved when present)\n' "$ENV_PATH"
printf '  data:   %s (preserved; offline snapshot made on upgrade)\n' "$STATE_DIR"
if [[ "$assume_yes" != true ]]; then
	[[ -r /dev/tty ]] || fail "interactive confirmation unavailable; rerun with --yes after reviewing the paths"
	read -r -p "Continue? [y/N] " reply </dev/tty
	[[ "$reply" == "y" || "$reply" == "Y" ]] || fail "cancelled"
fi

deployment_started=true
timestamp=$(date -u +%Y%m%dT%H%M%SZ)

if ! getent group "$SERVICE_GROUP" >/dev/null 2>&1; then
	groupadd --system "$SERVICE_GROUP"
fi
if ! id "$SERVICE_USER" >/dev/null 2>&1; then
	nologin_shell=$(command -v nologin || true)
	[[ -n "$nologin_shell" ]] || nologin_shell="/usr/sbin/nologin"
	useradd --system --gid "$SERVICE_GROUP" --home-dir "$STATE_DIR" --shell "$nologin_shell" --no-create-home "$SERVICE_USER"
else
	[[ $(id -u "$SERVICE_USER") -ne 0 ]] || fail "service user unexpectedly has UID 0"
	expected_gid=$(getent group "$SERVICE_GROUP" | cut -d: -f3)
	[[ $(id -g "$SERVICE_USER") == "$expected_gid" ]] || fail "existing service user does not use ${SERVICE_GROUP} as its primary group"
fi

install -d -m 0750 -o root -g "$SERVICE_GROUP" "$INSTALL_ROOT"
install -d -m 0755 -o root -g root "$BIN_DIR"
install -d -m 0750 -o root -g "$SERVICE_GROUP" "$CONFIG_DIR"
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_GROUP" "$STATE_DIR"
install -d -m 0700 -o root -g root "$BACKUP_ROOT"
install -d -m 2750 -o root -g "$SERVICE_GROUP" "$UPDATE_DIR"

# Stop new automatic-update launches, then share the updater's kernel lock for
# the complete manual deployment and any rollback. A running updater is never
# interrupted; failure to acquire this lock leaves the current install alone.
systemctl stop "${SERVICE_NAME}-update.path" >/dev/null 2>&1 || true
watcher_suspended=true
exec {update_lock_fd}>"$UPDATE_LOCK_PATH"
if ! flock -n "$update_lock_fd"; then
	fail "an automatic update is running; wait for it to finish before retrying the installer"
fi
if systemctl is-active --quiet "${SERVICE_NAME}-update.service"; then
	fail "an automatic update is still active; wait for it to finish before retrying the installer"
fi
backup_dir=$(mktemp -d -p "$BACKUP_ROOT" "${timestamp}.XXXXXXXX")
chown root:root "$backup_dir"
chmod 0700 "$backup_dir"

if [[ -e "$ENV_PATH" ]]; then
	[[ -f "$ENV_PATH" && ! -L "$ENV_PATH" ]] || fail "refusing non-regular environment file: ${ENV_PATH}"
	chown root:"$SERVICE_GROUP" "$ENV_PATH"
	chmod 0640 "$ENV_PATH"
else
	umask 0077
	{
		printf 'SUB2API_LIMIT_LISTEN=0.0.0.0:2556\n'
		printf 'SUB2API_LIMIT_DB_PATH=/opt/sub2api5hlimit/data/app.db\n'
		printf 'SUB2API_LIMIT_MASTER_KEY=%s\n' "$generated_master_key"
		printf 'SUB2API_LIMIT_COOKIE_SECURE=false\n'
	} >"${ENV_PATH}.new.$$"
	chown root:"$SERVICE_GROUP" "${ENV_PATH}.new.$$"
	chmod 0640 "${ENV_PATH}.new.$$"
	mv -f -T -- "${ENV_PATH}.new.$$" "$ENV_PATH"
fi

if [[ "$had_binary" == true ]]; then
	cp --preserve=mode,timestamps -- "$BIN_PATH" "${backup_dir}/sub2api-limit-portal"
fi
if [[ "$had_updater" == true ]]; then
	cp --preserve=mode,timestamps -- "$UPDATER_PATH" "${backup_dir}/sub2api-limit-updater"
fi
if [[ "$had_unit" == true ]]; then
	cp --preserve=mode,timestamps -- "$UNIT_PATH" "${backup_dir}/sub2api-limit-portal.service"
fi
if [[ "$had_update_unit" == true ]]; then
	cp --preserve=mode,timestamps -- "$UPDATE_UNIT_PATH" "${backup_dir}/sub2api-limit-portal-update.service"
fi
if [[ "$had_update_path_unit" == true ]]; then
	cp --preserve=mode,timestamps -- "$UPDATE_PATH_UNIT_PATH" "${backup_dir}/sub2api-limit-portal-update.path"
fi
if [[ "$had_uninstaller" == true ]]; then
	cp --preserve=mode,timestamps -- "$UNINSTALL_PATH" "${backup_dir}/sub2api-limit-portal-uninstall"
fi

rollback_files=true

if [[ "$had_unit" == true || "$was_active" == true ]]; then
	systemctl stop "${SERVICE_NAME}.service"
fi
if [[ -e "$UPDATE_REQUEST_PATH" || -L "$UPDATE_REQUEST_PATH" || -e "$UPDATE_TRANSACTION_PATH" || -L "$UPDATE_TRANSACTION_PATH" ]]; then
	fail "an automatic update or recovery is pending; let it finish before installing manually"
fi

# The service is stopped before copying SQLite files so app.db, WAL and SHM form
# one coherent offline snapshot. Missing WAL/SHM files are normal.
for database_file in "$DB_PATH" "${DB_PATH}-wal" "${DB_PATH}-shm"; do
	if [[ -e "$database_file" ]]; then
		[[ -f "$database_file" && ! -L "$database_file" ]] || fail "refusing non-regular database file: ${database_file}"
		cp --preserve=mode,ownership,timestamps -- "$database_file" "$backup_dir/"
	fi
done
db_backup_complete=true

install -m 0755 -o root -g root -- "$binary_source" "${BIN_PATH}.new.$$"
mv -f -T -- "${BIN_PATH}.new.$$" "$BIN_PATH"
install -m 0755 -o root -g root -- "$updater_binary_source" "${UPDATER_PATH}.new.$$"
mv -f -T -- "${UPDATER_PATH}.new.$$" "$UPDATER_PATH"
install -m 0644 -o root -g root -- "$unit_source" "${UNIT_PATH}.new.$$"
mv -f -T -- "${UNIT_PATH}.new.$$" "$UNIT_PATH"
install -m 0644 -o root -g root -- "$update_unit_source" "${UPDATE_UNIT_PATH}.new.$$"
mv -f -T -- "${UPDATE_UNIT_PATH}.new.$$" "$UPDATE_UNIT_PATH"
install -m 0644 -o root -g root -- "$update_path_unit_source" "${UPDATE_PATH_UNIT_PATH}.new.$$"
mv -f -T -- "${UPDATE_PATH_UNIT_PATH}.new.$$" "$UPDATE_PATH_UNIT_PATH"
install -m 0755 -o root -g root -- "$uninstall_source" "${UNINSTALL_PATH}.new.$$"
mv -f -T -- "${UNINSTALL_PATH}.new.$$" "$UNINSTALL_PATH"

systemctl daemon-reload
if [[ "$had_unit" != true || "$was_enabled" == true ]]; then
	systemctl enable "${SERVICE_NAME}.service"
fi
if [[ "$had_update_path_unit" != true || "$update_path_was_enabled" == true ]]; then
	systemctl enable "${SERVICE_NAME}-update.path"
fi

if [[ "$had_unit" != true || "$was_active" == true ]]; then
	systemctl start "${SERVICE_NAME}.service"
	if [[ "$action" == "upgrade" ]]; then
		if ! wait_for_portal_ready "$ready_endpoint" 30; then
			journalctl -u "${SERVICE_NAME}.service" -n 30 --no-pager >&2 || true
			fail "upgraded service did not become ready at ${ready_endpoint}; see the journal output above"
		fi
	else
		for ((attempt = 1; attempt <= 10; attempt++)); do
			sleep 1
			if ! systemctl is-active --quiet "${SERVICE_NAME}.service"; then
				journalctl -u "${SERVICE_NAME}.service" -n 30 --no-pager >&2 || true
				fail "service did not remain active; see the journal output above"
			fi
		done
	fi
else
	printf 'Existing service was inactive; its inactive state was preserved.\n'
fi
if [[ "$had_update_path_unit" != true || "$update_path_was_active" == true ]]; then
	start_update_watcher || fail "automatic-update watcher did not become active; restoring the previous installation"
else
	printf 'Existing automatic-update path watcher was inactive; its inactive state was preserved.\n'
	watcher_suspended=false
fi
deployment_started=false
release_update_lock
printf '\n%s complete.\n' "$action"
printf 'Backup snapshot: %s\n' "$backup_dir"
printf 'Environment file: %s\n' "$ENV_PATH"
printf 'Update status: %s/status.json\n' "$UPDATE_DIR"
if [[ "$had_unit" != true ]]; then
	printf 'Read the 30-minute setup token with:\n'
	printf '  journalctl -u %s.service -n 50 --no-pager\n' "$SERVICE_NAME"
fi
printf 'Optional Nginx HTTPS example: packaging/nginx-sub2api-limit-portal.conf\n'
