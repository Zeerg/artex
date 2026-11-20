#!/usr/bin/env bash
# ARTEX Install script:① All Docker ② Local compilation and execution
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){ printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){ printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){ local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
rand(){ head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }

# ── docker Environment detection / automatic installation ───────────────────
ensure_docker(){
 if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
 ok "Docker and docker compose detected."; return
 fi
 warn "Docker / docker compose not detected"
 case "$(uname -s)" in
 Linux)
 if [ "$(ask 'Automatically install Docker.? (y/n)' y)" = y ]; then
 curl -fsSL https://get.docker.com | sh
 sudo usermod -aG docker "$USER" || true
 ok "Docker Installation complete (user group changes require re-login to skip sudo)"
 else
 die "Please install docker yourself and retry."
 fi;;
 Darwin) die "macOS Please install Docker Desktop: https://www.docker.com/products/docker-desktop/";;
 *) die "Please install docker yourself and retry.";;
 esac
}

# ── ① All Docker ───────────────────────────────
install_docker(){
 ensure_docker
 if [! -f .env ]; then
 cp .env.example .env 2>/dev/null || true
 local pw key
 pw="$(ask 'Postgres Password (randomly generated on enter)' "$(rand)")"
 key="$(ask 'ANTHROPIC_API_KEY (Can be left empty, to be configured in the UI later' '')"
 sed -i.bak "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pw}|" .env
 sed -i.bak "s|^ANTHROPIC_API_KEY=.*|ANTHROPIC_API_KEY=${key}|" .env
 rm -f .env.bak
 ok "Generated .env (POSTGRES_PASSWORD set)"
 else
 info "Reuse existing .env"
 fi
 info "Pull image and start..."
 docker compose pull || true
 docker compose up -d
 ok "Startup Complete, → http://localhost:8787"
 info "View logs: docker compose logs -f artex"
}

# ── ② Local compilation and execution ──────────────────────────────
install_local(){
 echo "Database installation method:"
 echo " 1) Connect to existing PostgreSQL."
 echo " 2) Start a PostgreSQL with Docker (requires docker)."
 case "$(ask 'Select' 1)" in
 2)
 ensure_docker
 local pw; pw="$(ask 'Postgres Password (random on enter)' "$(rand)")"
 docker run -d --name artex-pg -p 5432:5432 \
 -e POSTGRES_USER=artex -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=artex \
 -v artex-pg:/var/lib/postgresql/data postgres:16-alpine
 DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=artex DB_PASS="$pw" DB_NAME=artex DB_SSL=disable;;
 *)
 DB_HOST="$(ask 'Database address' 127.0.0.1)"
 DB_PORT="$(ask 'Port' 5432)"
 DB_USER="$(ask 'Account' artex)"
 DB_PASS="$(ask 'Password' '')"
 DB_NAME="$(ask 'Database name' artex)"
 DB_SSL="$(ask 'sslmode (disable/require)' disable)";;
 esac

 # Generate config.json
 cat > config.json <<JSON
{
 "database": {
 "host": "${DB_HOST}",
 "port": ${DB_PORT},
 "user": "${DB_USER}",
 "password": "${DB_PASS}",
 "dbname": "${DB_NAME}",
 "sslmode": "${DB_SSL}"
 }
}
JSON
 ok "Config generated..json"

 # go Environment Check,
 command -v go >/dev/null 2>&1 || die "Go has not been detected. Please install Go first (>=1.26): https://go.dev/dl/"
 ok "Go: $(go version)"

 # Embedded frontend requires node to generate static assets.
 if command -v npm >/dev/null 2>&1; then
 info "Build frontend static products..."
 (cd web && npm ci && npm run build:static)
 rm -rf server/webui/dist && cp -r web/out server/webui/dist
 info "Compile an embedded single binary..."
 CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
 else
 warn "No npm detected: will compile**No embedded frontend**Backend (frontend needs to run npm run dev)"
 CGO_ENABLED=0 go build -o artex ./cmd/artex
 fi
 ok "Compilation Complete, → ./artex"

 info "Start... (Ctrl-C Exit)"
 ./artex
}

echo "=============================="
echo " ARTEX Overview"
echo " 1) All Docker installations"
echo " 2) Run locally (go compile)."
echo "=============================="
case "$(ask 'Select' 1)" in
 1) install_docker;;
 2) install_local;;
 *) die "Invalid Selection";;
esac
