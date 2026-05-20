# Single-stage builder doing npm install + Vite/TS build in ONE RUN, then
# a slim nginx runtime. Mirrors fullstack-chat's pattern — see that file for
# the Kaniko-bug rationale behind collapsing install + build into one layer.

# --- Builder (install + Vite/TS build, all in ONE RUN) ------------------
FROM node:24-alpine AS builder
WORKDIR /app

COPY . .

ARG VITE_MOSES_CHART_ID=""
ARG VITE_MOSES_DEPLOYMENT_ID=""
ARG VITE_MOSES_API_BASE=""

ARG NPM_REGISTRY=""

RUN NPM_REGISTRY="$NPM_REGISTRY" \
    VITE_MOSES_CHART_ID="$VITE_MOSES_CHART_ID" \
    VITE_MOSES_DEPLOYMENT_ID="$VITE_MOSES_DEPLOYMENT_ID" \
    VITE_MOSES_API_BASE="$VITE_MOSES_API_BASE" \
    sh -c 'if [ -n "$NPM_REGISTRY" ]; then npm config set registry "$NPM_REGISTRY"; fi && npm install && npx tsc -p tsconfig.build.json && npx vite build'

# --- Runtime ------------------------------------------------------------
FROM nginx:1.28-alpine

RUN apk add --no-cache gettext

COPY nginx.conf /etc/nginx/nginx.conf.template

COPY --from=builder /app/dist /usr/share/nginx/html

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://localhost:8080/health || exit 1

ENTRYPOINT ["/entrypoint.sh"]
