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

    local body_length=${#body}
    printf "HTTP/1.1 %s %s\r\n" "$status_code" "$status_text"
    printf "Content-Type: %s\r\n" "$content_type"
    printf "Content-Length: %d\r\n" "$body_length"
    printf "Connection: close\r\n"
    printf "\r\n"
    printf "%s" "$body"
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

        # If refresh=true, invalidate ALL cached credentials to force full re-auth
        if [ "$refresh" = "true" ]; then
            log "Refresh requested, invalidating all cached credentials for profile: $profile"
            # Clear both role credential cache AND SSO session cache
            find ~/.aws/cli/cache/ -name "*.json" -delete 2>/dev/null || true
            find ~/.aws/sso/cache/ -name "*.json" -delete 2>/dev/null || true
        fi

        # Check if already authenticated (no lock needed for cached creds)
        if aws sts get-caller-identity --profile "$profile" >/dev/null 2>&1; then
            # Credentials are cached, no browser needed
            cred_output=$(get_credentials "$profile" "$format")
            if [ $? -eq 0 ] && [ -n "$cred_output" ]; then
                log "GET /credentials?profile=$profile → 200 (cached)"
                if [ "$format" = "json" ]; then
                    send_json 200 "$cred_output"
                else
                    send_response 200 "OK" "text/plain" "$cred_output"
                fi
            else
                log_warn "GET /credentials?profile=$profile → 500 Failed to export cached credentials"
                send_json 500 '{"error": "Failed to export credentials"}'
            fi
            exit 0
        fi

        # Need browser SSO - acquire blocking lock (browser handles one SSO at a time)
        log "Waiting for SSO lock (profile=$profile)..."
        exec 200>"$LOCK_FILE"
        flock -x 200

        # Re-check after acquiring lock — another request may have completed SSO
        if aws sts get-caller-identity --profile "$profile" >/dev/null 2>&1; then
            cred_output=$(get_credentials "$profile" "$format")
            if [ $? -eq 0 ] && [ -n "$cred_output" ]; then
                log "GET /credentials?profile=$profile → 200 (cached after lock wait)"
                if [ "$format" = "json" ]; then
                    send_json 200 "$cred_output"
                else
                    send_response 200 "OK" "text/plain" "$cred_output"
                fi
            else
                log_warn "GET /credentials?profile=$profile → 500 Failed to export credentials after lock wait"
                send_json 500 '{"error": "Failed to export credentials"}'
            fi
            exit 0
        fi

        # Actually do SSO browser login
        log "GET /credentials?profile=$profile → SSO browser login started..."
        cred_output=$(get_credentials "$profile" "$format")
        exit_code=$?

        # Lock released automatically when fd 200 closes (process exit)
        if [ $exit_code -eq 0 ] && [ -n "$cred_output" ]; then
            log "GET /credentials?profile=$profile → 200 (SSO login)"
            if [ "$format" = "json" ]; then
                send_json 200 "$cred_output"
            else
                send_response 200 "OK" "text/plain" "$cred_output"
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
