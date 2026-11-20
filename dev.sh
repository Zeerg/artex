#!/usr/bin/env bash
# Development mode: backend(:8787) + Traffic proxy(:8788) With frontend next dev.(:5173) run together.
# Frontend /api reverse-proxied to backend; Ctrl-C exits both processes together.
#
# Single binary (embedded in frontend) method see README 'Single Binary' section, do not use this script.
set -euo pipefail
cd "$(dirname "$0")"

# When exiting, end all child processes in this process group (backend) +) frontend.
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# Backend (normal go run, no embedded frontend); concurrent work agent number configured in 'System Settings'.
go run ./cmd/artex -addr:8787 -proxy 127.0.0.1:8788 &

# Frontend hot update (Vite/Next dev server, /api reverse-proxy to:8787).
(cd web && npm run dev) &

echo "[dev] backend:8787 / Proxy:8788 / Frontend HTTP://localhost:5173 (Ctrl-C Exit)"
wait
