# syntax=docker/dockerfile:1
# Multi-stage: build the Vite frontend (reusing @platform/ui from the submodule), build a
# static Go server, and ship both on a distroless base — no Node, no Go toolchain, no shell.
# In-cluster judging uses the API backend (JUDGE_BACKEND=api), so the runtime carries neither
# Node nor the `claude` CLI: the image stays Go-only and small.

# ── 1. Frontend: the Vite build that imports @platform/ui ──────────────────────────────
FROM node:22-bookworm-slim AS web
WORKDIR /app
# BASE_PATH is baked into the client's asset URLs at build time; it MUST equal the server's
# runtime BASE_PATH or every asset 404s. platform-cicd's build.sh passes /job-searcher/.
ARG BASE_PATH=/
ENV BASE_PATH=$BASE_PATH
# @platform/ui resolves (file:) into the submodule, so it must be present before npm ci.
# Copied first, with the lockfile, so the install layer caches across source changes.
COPY package.json package-lock.json ./
COPY web/vendor ./web/vendor
RUN npm ci
COPY web ./web
COPY vite.config.ts tsconfig.json ./
RUN npm run build            # → web/dist

# ── 2. Server: a fully static Go binary ────────────────────────────────────────────────
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# CGO off → a static binary that runs on distroless/static.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gui ./cmd/gui

# ── 3. Runtime: binary + built assets + the public dataset. No Node, no shell. ──────────
FROM gcr.io/distroless/static-debian12:nonroot AS run
WORKDIR /app
ARG VERSION
ARG GIT_SHA
ARG BUILD_DATE
ARG BASE_PATH=/
LABEL org.opencontainers.image.title="job-searcher" \
      org.opencontainers.image.description="Job Searcher — ghost-job-filtering job search, as a platform service" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${GIT_SHA}" \
      org.opencontainers.image.created="${BUILD_DATE}"
COPY --from=build /out/gui /app/gui
COPY --from=web /app/web/dist /app/web
# The verified-results dataset the public, sign-in-free mock/preview serves.
COPY results.cache.csv /app/results.cache.csv
# Defaults the image ships with; the deploy's env overrides any of them. APP_VERSION is read
# at startup and served from <base>version. BASE_PATH must equal the frontend build arg above
# so the runtime mount matches the URLs Vite baked into the assets. A bare `docker build` (no
# --build-arg VERSION) leaves APP_VERSION empty and the server reports "snapshot".
ENV APP_VERSION=${VERSION} \
    BASE_PATH=${BASE_PATH} \
    ADDR=0.0.0.0:8080 \
    WEB_DIR=/app/web \
    CACHE_PATH=/app/results.cache.csv \
    PROFILE_PATH=/tmp/profile.yaml \
    JUDGE_BACKEND=api \
    JUDGE_MODEL=claude-haiku-4-5 \
    APIFY_TOKEN_FILE=/etc/.secrets/apify-token \
    ANTHROPIC_API_KEY_FILE=/etc/.secrets/anthropic-api-key
EXPOSE 8080
ENTRYPOINT ["/app/gui"]
