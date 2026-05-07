#!/bin/bash

# HTTP request handler for socat-based credential server.
# Each connection forks this script via:
#   socat TCP-LISTEN:port,fork,reuseaddr SYSTEM:serve-credentials.sh
# stdin/stdout are the TCP socket.

# Source shared library
# shellcheck source=get-credentials-lib.sh
source "$(dirname "$0")/get-credentials-lib.sh" 2>/dev/null || \
    source /usr/local/bin/get-credentials-lib.sh

LOCK_FILE="/tmp/sso-login.lock"

# Log to stderr so it appears in docker logs (stdout is the HTTP socket)
log() {
    echo -e "\033[32m[$(date '+%Y-%m-%d %H:%M:%S')] $*\033[0m" >&2
}

log_warn() {
    echo -e "\033[33m[$(date '+%Y-%m-%d %H:%M:%S')] WARN: $*\033[0m" >&2
}

# --- HTTP response helpers ---

send_response() {
    local status_code=$1
    local status_text=$2
    local content_type=$3
    local body=$4
    local extra_headers=$5

    local body_length=${#body}
    printf "HTTP/1.1 %s %s\r\n" "$status_code" "$status_text"
    printf "Content-Type: %s\r\n" "$content_type"
    printf "Content-Length: %d\r\n" "$body_length"
    printf "Connection: close\r\n"
    if [ -n "$extra_headers" ]; then
        # extra_headers is already CR-LF terminated per line
        printf "%b" "$extra_headers"
    fi
    printf "\r\n"
    printf "%s" "$body"
}

# Build extra-headers string exposing credential expiry to clients.
# Emits "Header: value\r\n" (literal backslash-r-n) lines, interpreted by printf %b.
credential_headers() {
    local profile=$1
    local expiry_iso ttl headers=""
    expiry_iso=$(credentials_expiry_iso "$profile")
    ttl=$(credentials_valid_for "$profile")
    [ -n "$expiry_iso" ] && headers+="X-Credentials-Expiry: ${expiry_iso}\\r\\n"
    [ "$ttl" -gt 0 ] && headers+="X-Credentials-TTL-Seconds: ${ttl}\\r\\n"
    printf "%s" "$headers"
}

send_json() {
    local status_code=$1
    local body=$2
    local status_text
    case "$status_code" in
        200) status_text="OK" ;;
        400) status_text="Bad Request" ;;
        404) status_text="Not Found" ;;
        500) status_text="Internal Server Error" ;;
        503) status_text="Service Unavailable" ;;
        *) status_text="Unknown" ;;
    esac
    send_response "$status_code" "$status_text" "application/json" "$body"
}

# --- Request parsing ---

parse_query_param() {
    local query=$1
    local key=$2
    echo "$query" | tr '&' '\n' | grep "^${key}=" | head -1 | cut -d'=' -f2- | sed 's/%20/ /g; s/+/ /g'
}

# --- Main ---

# Read HTTP request line (with timeout to avoid hanging)
read -r -t 10 request_line || {
    send_json 400 '{"error": "Request timeout"}'
    exit 0
}

# Strip trailing \r
request_line="${request_line%%$'\r'}"

# Consume remaining headers (read until empty line)
while read -r -t 5 header_line; do
    header_line="${header_line%%$'\r'}"
    [ -z "$header_line" ] && break
done

# Parse method and path
method=$(echo "$request_line" | awk '{print $1}')
full_path=$(echo "$request_line" | awk '{print $2}')

# Split path and query string
path="${full_path%%\?*}"
query=""
if [[ "$full_path" == *"?"* ]]; then
    query="${full_path#*\?}"
fi

# Only support GET
if [ "$method" != "GET" ]; then
    log_warn "$method $full_path → 400 Method not allowed"
    send_json 400 '{"error": "Method not allowed, use GET"}'
    exit 0
fi

# --- Route requests ---

case "$path" in
    /health)
        send_json 200 '{"status": "ok"}'
        ;;

    /profiles)
        log "GET /profiles"
        profiles_json=$(list_profiles_json)
        if [ $? -eq 0 ] && [ -n "$profiles_json" ]; then
            log "GET /profiles → 200"
            send_json 200 "$profiles_json"
        else
            log_warn "GET /profiles → 500 Failed to list profiles"
            send_json 500 '{"error": "Failed to list profiles"}'
        fi
        ;;

    /credentials)
        profile=$(parse_query_param "$query" "profile")
        format=$(parse_query_param "$query" "format")
        refresh=$(parse_query_param "$query" "refresh")
        [ -z "$format" ] && format="env"

        if [ -z "$profile" ]; then
            log_warn "GET /credentials → 400 Missing profile parameter"
            send_json 400 '{"error": "Missing required parameter: profile"}'
            exit 0
        fi

        log "GET /credentials?profile=$profile&format=$format&refresh=$refresh"

        # Validate format
        case "$format" in
            json|env|export) ;;
            *)
                log_warn "GET /credentials → 400 Invalid format: $format"
                send_json 400 "{\"error\": \"Invalid format: $format. Use json, env, or export\"}"
                exit 0
                ;;
        esac

        MIN_TTL=${MIN_CREDENTIAL_TTL_SECONDS:-600}

        serve_cached() {
            local reason=$1
            local cred_output
            cred_output=$(get_credentials "$profile" "$format")
            if [ $? -eq 0 ] && [ -n "$cred_output" ]; then
                local hdrs
                hdrs=$(credential_headers "$profile")
                log "GET /credentials?profile=$profile → 200 ($reason, ttl=$(credentials_valid_for "$profile")s)"
                if [ "$format" = "json" ]; then
                    send_response 200 "OK" "application/json" "$cred_output" "$hdrs"
                else
                    send_response 200 "OK" "text/plain" "$cred_output" "$hdrs"
                fi
                return 0
            fi
            log_warn "GET /credentials?profile=$profile → 500 Failed to export credentials ($reason)"
            send_json 500 '{"error": "Failed to export credentials"}'
            return 1
        }

        # Fast path: cached creds with sufficient TTL, no refresh requested.
        if [ "$refresh" != "true" ]; then
            ttl=$(credentials_valid_for "$profile")
            if [ "$ttl" -ge "$MIN_TTL" ]; then
                serve_cached "cached, ttl ok"
                exit 0
            fi
        fi

        # Either refresh=true or TTL below threshold — take the lock before any cache mutation
        # so concurrent requests don't race the wipe.
        log "Waiting for SSO lock (profile=$profile, refresh=$refresh)..."
        exec 200>"$LOCK_FILE"
        flock -x 200

        # refresh=true: purge only role creds INSIDE the lock. Keep the SSO session so we
        # can re-derive fresh role creds (~1s) without a browser login. If the SSO session
        # itself is expired, get_credentials() will detect that and fall back to browser SSO.
        if [ "$refresh" = "true" ]; then
            log "Refresh requested, purging role credential cache for profile: $profile"
            purge_role_cache
        fi

        # Re-check under lock — another request may have just completed SSO with fresh creds.
        ttl=$(credentials_valid_for "$profile")
        if [ "$ttl" -ge "$MIN_TTL" ]; then
            serve_cached "cached after lock"
            exit 0
        fi

        # Actually do SSO browser login via get_credentials (it handles the full flow).
        log "GET /credentials?profile=$profile → SSO browser login started..."
        cred_output=$(get_credentials "$profile" "$format")
        exit_code=$?

        # Lock released automatically when fd 200 closes (process exit)
        if [ $exit_code -eq 0 ] && [ -n "$cred_output" ]; then
            hdrs=$(credential_headers "$profile")
            log "GET /credentials?profile=$profile → 200 (SSO login, ttl=$(credentials_valid_for "$profile")s)"
            if [ "$format" = "json" ]; then
                send_response 200 "OK" "application/json" "$cred_output" "$hdrs"
            else
                send_response 200 "OK" "text/plain" "$cred_output" "$hdrs"
            fi
        else
            log_warn "GET /credentials?profile=$profile → 500 SSO login failed"
            send_json 500 '{"error": "SSO login failed"}'
        fi
        ;;

    *)
        log_warn "GET $path → 404 Not found"
        send_json 404 '{"error": "Not found"}'
        ;;
esac
