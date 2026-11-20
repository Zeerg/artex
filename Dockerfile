# syntax=docker/dockerfile:1
#
# Run image (do not compile inside the image): only install common tools, place them in**Precompiled Linux single binary**.
# Binary compiled across platforms by CI's binaries job (pure Go, no QEMU), placed according to target architecture at
# Build context dist/<TARGETARCH>/artex. When building for multiple architectures, arm64 only needs to simulate the apt layer,
# No longer simulate Next/Go compilation, much faster.
#
# When manually building images locally, prepare the binary first:
# cd web && npm run build:static && cd ..
# cp -r web/out server/webui/dist
# CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
# docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# Common tools: ripgrep / curl / vim, add a batch of recon ready-to-use items (add/remove as needed).
# Node Install NodeSource 20.x: bookworm Built-in apt nodejs is 18, Playwright requires >=20.
RUN apt-get update && apt-get install -y --no-install-recommends \
 ca-certificates ripgrep curl wget vim git jq unzip \
 dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
 && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && rm -rf /var/lib/apt/lists/*
# Pre-install Playwright MCP & CLI (globally), no longer download via npx at runtime.
# @playwright/mcp: browser MCP Verification `npx @playwright/mcp` (Already globally installed, no need for -y/@latest).
# @playwright/cli: Provide playwright-cli, install and verify its executability with --help.
# Reinstall playwright (providing browser management), then install with --with-deps to pre-install chromium and its system dependencies,
# So MCP/CLI can be used immediately within the container without needing an internet download of a browser.
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
 && playwright-cli --help \
 && playwright install --with-deps chromium \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# Precompiled corresponding architecture binary (dist/amd64/artex or dist/arm64/artex)
COPY dist/${TARGETARCH}/artex /app/artex
# Daemon startup script: restart if the process exits with a non-zero exit code, page one-click update relies on it to complete the transition.
# It also handles forwarding SIGTERM to artex —— docker stop Only send signals to PID 1,
# If not forwarded, artex will not receive or perform graceful shutdown; it will be forcefully killed with SIGKILL after 10 seconds.
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# data/ (SQLite + jwt.key)Persistence point
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
