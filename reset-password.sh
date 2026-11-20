#!/usr/bin/env bash
# =============================================================================
# ARTEX Script for resetting admin password
#
# Login username fixed as ARTEX; password stored in the database settings table using bcrypt hash
# auth.password_hash Key. After connecting to the database, this script uses pgcrypto to generate a bcrypt hash within the library.
# And write back to that key——Consistent with backend login verification (golang).org/x/crypto/bcrypt)Fully compatible.
#
# Two deployment modes:
# local (Default)—— The host directly connects to the database using psql. Connection information is obtained according to the following priority:
# Command line arguments > --dsn/$ARTEX_PG_DSN > config.json the database.*
# docker —— Success `docker compose exec` (Or `docker exec`)In postgres
# Executes psql within the container (compose does not expose port 5432 to the host by default, so it runs inside the container).
#
# Usage example:
# ./reset-password.sh # Local, automatically read config.json/Environment, interactively input new password.
# ./reset-password.sh -p 'NewPass!' # Locally, directly give new password
# ./reset-password.sh --dsn postgres://u:p@h:5432/artex
# ./reset-password.sh -H 127.0.0.1 -P 5433 -U autopentest -W pass -d artex
# ./reset-password.sh -m docker # docker Deployment (read .env POSTGRES_*)
# ./reset-password.sh -m docker -c pgContainer name --exec docker
#
# Security: New password via environment variable + psql \getenv Passed in (not entering process argv), and using:'var'
# Auto-escape (to prevent SQL injection); database password passed via PGPASSWORD, not in argv.
# =============================================================================
set -euo pipefail

PASS_KEY="auth.password_hash"
BCRYPT_COST=10

MODE="" # local | docker (empty=Auto-detection)
DSN=""
HOST="" PORT="" USER="" DBPASS="" DBNAME="" SSLMODE=""
CONFIG=""
CONTAINER="" # docker Pattern postgres service/container name (default postgres)
EXEC_KIND="" # compose | docker (docker Which exec to use in mode; empty=Auto)
NEWPASS=""
ASSUME_YES=0

die() { echo "Error:$*" >&2; exit 1; }
info() { echo "· $*" >&2; }

usage() { sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# ---- Parameter parsing -------------------------------------------------------------
while [[ $# -gt 0 ]]; do
 case "$1" in
 -m|--mode) MODE="${2:-}"; shift 2;;
 --dsn) DSN="${2:-}"; shift 2;;
 -H|--host) HOST="${2:-}"; shift 2;;
 -P|--port) PORT="${2:-}"; shift 2;;
 -U|--user) USER="${2:-}"; shift 2;;
 -W|--db-password) DBPASS="${2:-}"; shift 2;;
 -d|--dbname) DBNAME="${2:-}"; shift 2;;
 --sslmode) SSLMODE="${2:-}"; shift 2;;
 --config) CONFIG="${2:-}"; shift 2;;
 -c|--container) CONTAINER="${2:-}"; shift 2;;
 --exec) EXEC_KIND="${2:-}"; shift 2;;
 -p|--new-password) NEWPASS="${2:-}"; shift 2;;
 -y|--yes) ASSUME_YES=1; shift;;
 -h|--help) usage;;
 *) die "Unknown parameter:$1 (-h View Usage)";;
 esac
done

# ---- From config.json Read from database.* (Only for local mode, and when not explicitly given connection -----
# Prefer using python3 for parsing (robust); fall back to grep when missing python3 (config.json For neat field separation).
read_config_json() {
 local path="$1"
 [[ -f "$path" ]] || return 1
 if command -v python3 >/dev/null 2>&1; then
 python3 - "$path" <<'PY'
import json, sys
try:
 d = json.load(open(sys.argv[1])).get("database", {})
except Exception:
 sys.exit(1)
# Support direct dsn or field-by-field
if d.get("dsn"):
 print("DSN\t" + d["dsn"]); sys.exit(0)
for k in ("host","port","user","password","dbname","sslmode"):
 if d.get(k) is not None:
 print(k.upper() + "\t" + str(d[k]))
PY
 else
 # Minimal fallback: grep each key (value as string or number)
 local k
 for k in host port user password dbname sslmode; do
 local v
 v=$(grep -oE "\"$k\"[[:space:]]*:[[:space:]]*(\"[^\"]*\"|[0-9]+)" "$path" 2>/dev/null \
 | head -1 | sed -E "s/.*:[[:space:]]*//; s/^\"//; s/\"$//") || true
 [[ -n "$v" ]] && echo -e "${k^^}\t$v"
 done
 fi
}

apply_config_fields() {
 local line key val
 while IFS=$'\t' read -r key val; do
 [[ -z "$key" ]] && continue
 case "$key" in
 DSN) [[ -z "$DSN" ]] && DSN="$val";;
 HOST) [[ -z "$HOST" ]] && HOST="$val";;
 PORT) [[ -z "$PORT" ]] && PORT="$val";;
 USER) [[ -z "$USER" ]] && USER="$val";;
 PASSWORD) [[ -z "$DBPASS" ]] && DBPASS="$val";;
 DBNAME) [[ -z "$DBNAME" ]] && DBNAME="$val";;
 SSLMODE) [[ -z "$SSLMODE" ]] && SSLMODE="$val";;
 esac
 done
}

# ---- Automatic determination mode ---------------------------------------------------------
if [[ -z "$MODE" ]]; then
 if [[ -n "$DSN$HOST$USER$DBNAME" || -n "${ARTEX_PG_DSN:-}" || -f "${CONFIG:-config.json}" ]]; then
 MODE="local"
 elif command -v docker >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
 MODE="docker"
 else
 MODE="local"
 fi
fi
info "Deployment mode:$MODE"

# ---- Collect new password -----------------------------------------------------------
if [[ -z "$NEWPASS" ]]; then
 read -r -s -p "Enter new password (username fixed as ARTEX):" NEWPASS; echo >&2
 [[ -n "$NEWPASS" ]] || die "Password cannot be empty."
 read -r -s -p "Re-enter to confirm:" NEWPASS2; echo >&2
 [[ "$NEWPASS" == "$NEWPASS2" ]] || die "Inputs do not match"
fi
[[ -n "$NEWPASS" ]] || die "Password cannot be empty."

# Pass the password to psql via environment variable (\getenv Read, do not enter argv/ps)
export ARTEX_RESET_NEWPASS="$NEWPASS"

# Library generates bcrypt and upserts; password is:'newpw' Automatic escaping. CREATE EXTENSION is idempotent,
# If the database role lacks permission to create extensions, an error will be reported here (details see below in the failed branch of run).
SQL=$(cat <<SQL
\\set ON_ERROR_STOP on
\\getenv newpw ARTEX_RESET_NEWPASS
CREATE EXTENSION IF NOT EXISTS pgcrypto;
INSERT INTO settings(key, value)
VALUES ('$PASS_KEY', crypt(:'newpw', gen_salt('bf', $BCRYPT_COST)))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
SQL
)

# ---- Execution -----------------------------------------------------------------
if [[ "$MODE" == "local" ]]; then
 # Connection info priority: command line. > --dsn/$ARTEX_PG_DSN > config.json
 if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
 [[ -n "${ARTEX_PG_DSN:-}" ]] && DSN="$ARTEX_PG_DSN"
 fi
 if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
 cfg="${CONFIG:-config.json}"
 if [[ -f "$cfg" ]]; then
 info "From $cfg Read database configuration"
 apply_config_fields < <(read_config_json "$cfg")
 fi
 fi

 command -v psql >/dev/null 2>&1 || die "psql not found on this machine (please install postgresql-client, or switch to -m docker)."

 declare -a PSQL_ARGS=()
 if [[ -n "$DSN" ]]; then
 PSQL_ARGS=("$DSN")
 target="$DSN"
 else
 [[ -n "$USER" ]] || die "Missing database user (-U) or valid config.json/DSN"
 [[ -n "$DBNAME" ]] || die "Missing database name (-d) or valid config..json/DSN"
 HOST="${HOST:-127.0.0.1}"; PORT="${PORT:-5432}"; SSLMODE="${SSLMODE:-disable}"
 PSQL_ARGS=(-h "$HOST" -p "$PORT" -U "$USER" -d "$DBNAME")
 [[ -n "$SSLMODE" ]] && export PGSSLMODE="$SSLMODE"
 [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
 target="$USER@$HOST:$PORT/$DBNAME"
 fi

 info "Target database: $target"
 if [[ "$ASSUME_YES" -ne 1 ]]; then
 read -r -p "Confirm resetting ARTEX password in this library?[y/N] " ans
 [[ "$ans" == "y" || "$ans" == "Y" ]] || die "Cancelled"
 fi

 if! printf '%s\n' "$SQL" | psql "${PSQL_ARGS[@]}" -v ON_ERROR_STOP=1 -q >/dev/null; then
 die "Write failed. If it reports pgcrypto permission/missing, use a role with extension creation permissions, or manually execute CREATE EXTENSION pgcrypto first."
 fi

else
 # ---- docker ----
 command -v docker >/dev/null 2>&1 || die "Docker not found"
 CONTAINER="${CONTAINER:-postgres}"

 # Select exec method: prioritize docker compose exec (service name), otherwise docker exec (container name)
 if [[ -z "$EXEC_KIND" ]]; then
 if docker compose version >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
 EXEC_KIND="compose"
 else
 EXEC_KIND="docker"
 fi
 fi

 # Credentials for psql inside the container: prioritize command line, then .env POSTGRES_*, Return to compose default.(artex)
 if [[ -f .env ]]; then
 # shellcheck disable=SC1091
 set -a; . ./.env; set +a
 fi
 DUSER="${USER:-${POSTGRES_USER:-artex}}"
 DNAME="${DBNAME:-${POSTGRES_DB:-artex}}"
 [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
 [[ -z "${PGPASSWORD:-}" && -n "${POSTGRES_PASSWORD:-}" ]] && export PGPASSWORD="$POSTGRES_PASSWORD"

 info "Target: Container $CONTAINER Internal psql -U $DUSER -d $DNAME (exec=$EXEC_KIND)"
 if [[ "$ASSUME_YES" -ne 1 ]]; then
 read -r -p "confirm resetting ARTEX password in this container database?[y/N] " ans
 [[ "$ans" == "y" || "$ans" == "Y" ]] || die "Cancelled"
 fi

 # -e Only name no value → Inherited from current environment; password does not appear in docker command argv.
 declare -a EXEC_CMD
 if [[ "$EXEC_KIND" == "compose" ]]; then
 EXEC_CMD=(docker compose exec -T -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
 psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
 else
 EXEC_CMD=(docker exec -i -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
 psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
 fi

 if! printf '%s\n' "$SQL" | "${EXEC_CMD[@]}" >/dev/null; then
 die "Write failed. Please confirm the container name (-c) and database account (user)..env POSTGRES_*), and has roles with pgcrypto permissions."
 fi
fi

unset ARTEX_RESET_NEWPASS
echo "✓ The ARTEX administrator password has been reset. Please use the username ARTEX. + New password login (no need to restart services)."
