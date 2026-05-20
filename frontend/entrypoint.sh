#!/bin/sh
set -eu

# Render Moses-aware embedding policy at container start. Mirrors the
# moses-only default from the platform resolver
# (moses-platform-prep/backend/internal/services/embedding_policy_resolver.go).

: "${MOSES_BASE_PATH:=/}"
: "${MOSES_EMBEDDING_FRAMING:=}"
: "${MOSES_EMBEDDING_ALLOWED_ANCESTORS:=}"
: "${MOSES_EMBEDDING_REPORT_URI:=}"
: "${MOSES_DOMAIN:=}"

if [ -z "$MOSES_EMBEDDING_FRAMING" ]; then
  MOSES_EMBEDDING_FRAMING="moses-only"
fi

: "${BACKEND_SERVICE_HOST:=moses-chat-bot-backend}"
: "${BACKEND_SERVICE_PORT:=8080}"

case "$MOSES_EMBEDDING_FRAMING" in
  public)
    MOSES_CSP_FRAME_ANCESTORS="*"
    MOSES_X_FRAME_OPTIONS=""
    ;;
  denied)
    MOSES_CSP_FRAME_ANCESTORS="'none'"
    MOSES_X_FRAME_OPTIONS="DENY"
    ;;
  moses-only|*)
    if [ -n "$MOSES_EMBEDDING_ALLOWED_ANCESTORS" ]; then
      MOSES_CSP_FRAME_ANCESTORS="$MOSES_EMBEDDING_ALLOWED_ANCESTORS"
    else
      MOSES_CSP_FRAME_ANCESTORS="'self' tauri://localhost http://tauri.localhost https://tauri.localhost"
      if [ -n "$MOSES_DOMAIN" ]; then
        case "$MOSES_DOMAIN" in
          localhost|localhost.|localhost:*|*.localhost|*.localhost.)
            MOSES_CSP_FRAME_ANCESTORS="${MOSES_CSP_FRAME_ANCESTORS} http://${MOSES_DOMAIN}:* https://${MOSES_DOMAIN}:*"
            ;;
          *)
            MOSES_CSP_FRAME_ANCESTORS="${MOSES_CSP_FRAME_ANCESTORS} https://*.${MOSES_DOMAIN}"
            ;;
        esac
      fi
    fi
    MOSES_X_FRAME_OPTIONS=""
    ;;
esac

if [ -n "$MOSES_EMBEDDING_REPORT_URI" ]; then
  MOSES_CSP_REPORT_URI="report-uri ${MOSES_EMBEDDING_REPORT_URI};"
else
  MOSES_CSP_REPORT_URI=""
fi

if [ -n "$MOSES_X_FRAME_OPTIONS" ]; then
  MOSES_X_FRAME_OPTIONS_LINE="add_header X-Frame-Options \"${MOSES_X_FRAME_OPTIONS}\" always;"
else
  MOSES_X_FRAME_OPTIONS_LINE=""
fi

MOSES_BASE_PATH_PREFIX="$(printf '%s' "$MOSES_BASE_PATH" | sed 's:/*$::')"
if [ -n "$MOSES_BASE_PATH_PREFIX" ]; then
  MOSES_SUBPATH_LOCATION_BLOCK="location ^~ ${MOSES_BASE_PATH_PREFIX}/api/ {
    proxy_pass http://${BACKEND_SERVICE_HOST}:${BACKEND_SERVICE_PORT}/api/;
    proxy_http_version 1.1;
    proxy_set_header Host \$host;
    proxy_set_header X-Real-IP \$remote_addr;
    proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto \$scheme;

    proxy_pass_header X-Moses-Tenant-ID;
    proxy_pass_header X-Moses-User-ID;
    proxy_pass_header X-Moses-Chart-ID;
    proxy_pass_header X-Moses-Request-ID;
  }
  location ^~ ${MOSES_BASE_PATH_PREFIX}/__moses/ {
    proxy_pass http://${BACKEND_SERVICE_HOST}:${BACKEND_SERVICE_PORT}/__moses/;
    proxy_http_version 1.1;
    proxy_set_header Host \$host;
    proxy_set_header X-Real-IP \$remote_addr;
    proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto \$scheme;
  }
  location ^~ ${MOSES_BASE_PATH_PREFIX}/ {
    absolute_redirect off;
    rewrite ^${MOSES_BASE_PATH_PREFIX}/(.*)\$ /\$1 last;
  }"
else
  MOSES_SUBPATH_LOCATION_BLOCK=""
fi

export MOSES_BASE_PATH
export MOSES_BASE_PATH_PREFIX
export MOSES_CSP_FRAME_ANCESTORS
export MOSES_CSP_REPORT_URI
export MOSES_X_FRAME_OPTIONS_LINE
export MOSES_SUBPATH_LOCATION_BLOCK
export BACKEND_SERVICE_HOST
export BACKEND_SERVICE_PORT

envsubst '${BACKEND_SERVICE_HOST} ${BACKEND_SERVICE_PORT} ${MOSES_BASE_PATH} ${MOSES_BASE_PATH_PREFIX} ${MOSES_CSP_FRAME_ANCESTORS} ${MOSES_CSP_REPORT_URI} ${MOSES_X_FRAME_OPTIONS_LINE} ${MOSES_SUBPATH_LOCATION_BLOCK}' \
  < /etc/nginx/nginx.conf.template > /etc/nginx/nginx.conf

INDEX_HTML="/usr/share/nginx/html/index.html"
if [ -f "$INDEX_HTML" ]; then
  TMP="$(mktemp)"
  sed "s|__MOSES_BASE_PATH__|${MOSES_BASE_PATH}|g" "$INDEX_HTML" > "$TMP"
  mv "$TMP" "$INDEX_HTML"
  chmod 644 "$INDEX_HTML"
fi

exec nginx -g 'daemon off;'
