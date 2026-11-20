#!/usr/bin/env bash
# ARTEX Update script: ① Docker Update (pull new image rebuild) ② local compile update (rebuild binary)
# With install.sh Corresponds to install responsible for initial landing, update responsible for upgrading to new version.
# DB Migration does not require manual execution——artex Idempotently rerun schema on every startup.sql (Contains ADD COLUMN/CREATE
# INDEX IF NOT EXISTS), Retry“Restart for migration”. Data (pgdata volume,./data, ./skills)Unaffected.
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){ printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){ printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){ local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# ── Optional: Synchronize the repository to the latest code (depends on compose/scripts/local source code compilation for updates)───────
sync_repo(){
 [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "Skip git pull for non-git working copies."; return; }
 [ "$(ask 'Pull latest code (git pull --ff-only)? (y/n)' y)" = y ] || return
 if! git pull --ff-only; then
 warn "git pull Unable to fast-forward (local changes or branch divergence)——Please manually handle and retry, continuing with the current code."
 fi
}

# ── ① Docker Update ───────────────────────────────
update_docker(){
 command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
 || die "Docker or docker-compose not detected, please use first. ./install.sh Installation deployment"
 [ -f .env ] || die "Not Found .env, Please Run First ./install.sh Complete Initial Deployment"

 # Optional: Upgrade to a specified version tag (if not filled, use the current one). .env ARTEX_TAG in the middle, default is latest)
 local tag; tag="$(ask 'Target image tag (press Enter to carry over). .env / latest)' '')"
 if [ -n "$tag" ]; then
 if grep -q '^ARTEX_TAG=' .env; then
 sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env && rm -f .env.bak
 else
 printf '\nARTEX_TAG=%s\n' "$tag" >> .env
 fi
 ok "ARTEX_TAG has been set to ${tag}"
 fi

 # Only modify artex: postgres is fixed at 16-alpine and does not need to be upgraded (pulling it is purely a waste of bandwidth,
 # And there is compatibility risk with major version changes). artex declares depends_on postgres, so it includes the service name
 # up If pg doesn't start automatically, it will be pulled up. Running instances will be retained as is, no re-creation.
 info "Pull new image (only artex)..."
 docker compose pull artex
 info "Rebuild and start (artex restarts automatically migrate schema)..."
 docker compose up -d artex
 ok "Update Complete, → http://localhost:8787"
 info "View logs: docker compose logs -f artex"
 info "Clean up old images (optional): docker image prune -f"
}

# ── ② Local compilation update ──────────────────────────────
update_local(){
 command -v go >/dev/null 2>&1 || die "No Go detected (>=1.26): https://go.dev/dl/"
 [ -f config.json ] || warn "Config not found.json——If first deployment, please use ./install.sh"
 ok "Go: $(go version)"

 if command -v npm >/dev/null 2>&1; then
 info "Rebuild frontend static assets...."
 (cd web && npm ci && npm run build:static)
 rm -rf server/webui/dist && cp -r web/out server/webui/dist
 info "Recompile embedded single binary..."
 CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
 else
 warn "No npm detected: Compile**No embedded frontend**Backend (frontend needs to run npm run dev)"
 CGO_ENABLED=0 go build -o artex ./cmd/artex
 fi
 ok "Compilation Complete, → ./artex"
 warn "Restart the running artex process for it to take effect (it will automatically migrate schema upon restart)."
}

echo "=============================="
echo " ARTEX Update"
echo " 1) Docker Update (pull new image rebuild)"
echo " 2) Local update (go recompile),"
echo "=============================="
case "$(ask 'Select' 1)" in
 1) sync_repo; update_docker;;
 2) sync_repo; update_local;;
 *) die "Invalid Selection";;
esac
