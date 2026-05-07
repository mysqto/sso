#!/bin/bash

# Shared library for SSO credential operations.
# Sourced by get-credentials.sh and serve-credentials.sh.

timestamp() {
    date '+%Y-%m-%d %H:%M:%S'
}

info() {
    echo -e "\033[32m[$(timestamp)] INFO: $*\033[0m" >&2
}

warn() {
    echo -e "\033[33m[$(timestamp)] WARN: $*\033[0m" >&2
}

error() {
    echo -e "\033[31m[$(timestamp)] ERROR: $*\033[0m" >&2
    exit 1
}

# Wait for browserless chrome to be ready
wait_for_browserless() {
    local max_attempts=${1:-60}
    local attempt=1
    local browserless_url
    case "$BROWSER_MODE" in
        browserless-v2)
            browserless_url="http://browserless_chromium:3000/json/version?token=${BROWSERLESS_TOKEN:-browserless}"
            ;;
        *)
            browserless_url="http://browserless_chrome:3000/json/version"
            ;;
    esac

    info "Waiting for browserless chrome to be ready..."
    while [ $attempt -le $max_attempts ]; do
        if curl -sf "$browserless_url" >/dev/null 2>&1; then
            info "Browserless chrome is ready (attempt $attempt/$max_attempts)"
            return 0
        fi
        if [ $((attempt % 10)) -eq 0 ]; then
            info "Still waiting for browserless chrome... (attempt $attempt/$max_attempts)"
        fi
        sleep 1
        attempt=$((attempt + 1))
    done
    error "Browserless chrome is not ready after $max_attempts attempts"
}

# Get remote URL based on browser mode
get_remote_url() {
    case "$BROWSER_MODE" in
        browserless-v1)
            echo "ws://browserless_chrome:3000"
            ;;
        browserless-v2)
            echo "ws://browserless_chromium:3000?token=${BROWSERLESS_TOKEN:-browserless}"
            ;;
        *)
            echo "ws://browserless_chrome:3000"
            ;;
    esac
}

# Perform SSO login
sso_login() {
    local url=$1
    [ -z "$url" ] && error "SSO URL is not specified"

    [ -z "$SSO_EMAIL" ] && error "SSO_EMAIL is not specified"
    [ -z "$SSO_PASSWORD" ] && error "SSO_PASSWORD is not specified"
    [ -z "$SSO_OTP_SECRET" ] && error "SSO_OTP_SECRET is not specified"
    [ -z "$BROWSER_MODE" ] && error "BROWSER_MODE is not specified"

    local remote_url
    remote_url=$(get_remote_url)
    info "Starting SSO automation for URL: $url (mode: $BROWSER_MODE, remote: $remote_url)"

    local sso_exit_code=0
    if timeout 300 /usr/local/bin/sso --remote-url "$remote_url" \
        --sso-url "$url" >&2; then
        sso_exit_code=0
    else
        sso_exit_code=$?
    fi

    if [ $sso_exit_code -eq 0 ]; then
        info "SSO completed successfully"
        return 0
    elif [ $sso_exit_code -eq 124 ]; then
        warn "SSO timed out after 300 seconds"
        return 1
    else
        warn "SSO exited with code: $sso_exit_code"
        return 1
    fi
}

# List available profiles
list_profiles() {
    local config_file="/config/sso-config.json"
    if [ ! -f "$config_file" ]; then
        error "Config file not found: $config_file"
    fi

    info "Available profiles:"
    jq -r '.profiles | keys[]' "$config_file"
}

# List profiles as JSON array (for HTTP API)
list_profiles_json() {
    local config_file="/config/sso-config.json"
    if [ ! -f "$config_file" ]; then
        echo '{"error": "Config file not found"}'
        return 1
    fi

    jq -c '.profiles | keys' "$config_file"
}

# Returns the expiry time (ISO-8601) of cached role credentials, or empty string.
credentials_expiry_iso() {
    local profile=$1
    aws configure export-credentials --profile "$profile" --format process 2>/dev/null \
        | jq -r '.Expiration // empty'
}

# Returns the unix epoch at which cached role credentials for $profile expire, or 0.
# Uses python (always available because aws-cli depends on it) to avoid busybox date
# limitations with ISO-8601 "T" separator and "+00:00" timezone offset.
credentials_expiry_epoch() {
    local profile=$1
    local exp
    exp=$(credentials_expiry_iso "$profile")
    [ -z "$exp" ] && { echo 0; return; }
    python3 -c "
import datetime, sys
s = sys.argv[1].replace('Z', '+00:00')
try:
    print(int(datetime.datetime.fromisoformat(s).timestamp()))
except Exception:
    print(0)
" "$exp" 2>/dev/null || echo 0
}

# Returns seconds of life remaining for cached creds, or 0 if none/expired/error.
credentials_valid_for() {
    local profile=$1
    local now exp
    now=$(date -u +%s)
    exp=$(credentials_expiry_epoch "$profile")
    [ "$exp" -le "$now" ] && { echo 0; return; }
    echo $((exp - now))
}

# Purge only the role-credentials cache. Keeps SSO session, so the next
# `aws configure export-credentials` re-derives fresh role creds without a browser login (~1s).
purge_role_cache() {
    find ~/.aws/cli/cache/ -name "*.json" -delete 2>/dev/null || true
}

# Purge SSO session cache. Forces a full browser login on next auth.
purge_sso_cache() {
    find ~/.aws/sso/cache/ -name "*.json" -delete 2>/dev/null || true
}

# Purge everything (role + SSO session). Used when SSO session itself is suspected expired.
purge_credential_caches() {
    purge_role_cache
    purge_sso_cache
}

# Get credentials for a profile
get_credentials() {
    local profile=$1
    local format=${2:-json}
    local min_ttl=${MIN_CREDENTIAL_TTL_SECONDS:-600}

    local ttl
    ttl=$(credentials_valid_for "$profile")

    # Fast path: cached role creds with sufficient TTL.
    if [ "$ttl" -ge "$min_ttl" ] && aws sts get-caller-identity --profile "$profile" >/dev/null 2>&1; then
        info "Already authenticated for profile: $profile (ttl=${ttl}s)"
    else
        # Role creds missing or below TTL. Purge them and try re-deriving from the SSO session
        # (cheap: no browser). Only fall through to full SSO login if the SSO session itself is expired.
        if [ "$ttl" -gt 0 ] && [ "$ttl" -lt "$min_ttl" ]; then
            info "Cached role creds for $profile expire in ${ttl}s (< ${min_ttl}s) — purging role cache"
        fi
        purge_role_cache
        if aws sts get-caller-identity --profile "$profile" >/dev/null 2>&1; then
            ttl=$(credentials_valid_for "$profile")
            info "Role creds refreshed from SSO session for $profile (ttl=${ttl}s, no browser)"
        else
            # SSO session also expired — do a full browser login.
            purge_sso_cache
            info "SSO session expired for $profile — starting browser SSO login"

        # Wait for browserless
        wait_for_browserless 60

        # Start SSO login
        local output
        output=$(mktemp)
        aws sso login --profile "$profile" --no-browser 2>&1 | tee "$output" >&2 &
        local aws_pid=$!

        # Wait for the SSO URL
        local url=""
        local timeout=60
        local elapsed=0
        while [ -z "$url" ] && [ $elapsed -lt $timeout ]; do
            url=$(grep -Eo "https://.*user_code=.*" "$output" 2>/dev/null | head -n 1)
            sleep 1
            elapsed=$((elapsed + 1))
        done

        [ -z "$url" ] && error "Failed to get SSO URL"

        # Perform browser SSO
        if ! sso_login "$url"; then
            kill $aws_pid 2>/dev/null || true
            rm -f "$output"
            error "SSO login failed"
        fi

        # Wait for AWS SSO to complete
        local max_wait=300
        elapsed=0
        while ! grep -q "Successfully logged into Start URL" "$output" 2>/dev/null; do
            if [ $elapsed -ge $max_wait ]; then
                kill $aws_pid 2>/dev/null || true
                rm -f "$output"
                error "AWS SSO login timeout"
            fi
            sleep 2
            elapsed=$((elapsed + 2))
        done

        rm -f "$output"
        info "AWS SSO login completed successfully"
        fi
    fi

    # Export credentials
    info "Exporting credentials for profile: $profile"
    local creds
    creds=$(aws configure export-credentials --profile "$profile" --format env 2>/dev/null)

    if [ -z "$creds" ]; then
        error "Failed to export credentials"
    fi

    # Parse credentials
    local access_key secret_key session_token
    access_key=$(echo "$creds" | grep "AWS_ACCESS_KEY_ID" | cut -d'=' -f2)
    secret_key=$(echo "$creds" | grep "AWS_SECRET_ACCESS_KEY" | cut -d'=' -f2)
    session_token=$(echo "$creds" | grep "AWS_SESSION_TOKEN" | cut -d'=' -f2)

    # Get region from profile config
    local region
    region=$(aws configure get region --profile "$profile" 2>/dev/null || true)

    local expiration
    expiration=$(credentials_expiry_iso "$profile")

    # Output in requested format
    case "$format" in
        json)
            local json="{\"access_key_id\": \"$access_key\", \"secret_access_key\": \"$secret_key\", \"session_token\": \"$session_token\", \"profile\": \"$profile\""
            if [ -n "$region" ]; then
                json="$json, \"region\": \"$region\""
            fi
            if [ -n "$expiration" ]; then
                json="$json, \"expiration\": \"$expiration\""
            fi
            json="$json}"
            echo "$json" | jq .
            ;;
        env)
            echo "AWS_ACCESS_KEY_ID=$access_key"
            echo "AWS_SECRET_ACCESS_KEY=$secret_key"
            echo "AWS_SESSION_TOKEN=$session_token"
            [ -n "$region" ] && echo "AWS_DEFAULT_REGION=$region"
            ;;
        export)
            echo "export AWS_ACCESS_KEY_ID=$access_key"
            echo "export AWS_SECRET_ACCESS_KEY=$secret_key"
            echo "export AWS_SESSION_TOKEN=$session_token"
            [ -n "$region" ] && echo "export AWS_DEFAULT_REGION=$region"
            ;;
        *)
            error "Unknown format: $format"
            ;;
    esac
}
