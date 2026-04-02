#!/bin/bash

set -e

# Source shared library
# shellcheck source=get-credentials-lib.sh
source "$(dirname "$0")/get-credentials-lib.sh"

usage() {
    cat >&2 <<EOF
Usage: $0 --profile <profile_name> [options]

Options:
  --profile <name>    AWS profile name (required)
  --list              List available profiles
  --format <format>   Output format: json (default), env, export
  --serve             Start HTTP credential server (requires socat)
  --port <port>       HTTP server port (default: 8080, use with --serve)
  --help              Show this help message

Examples:
  $0 --profile blackhole_staging
  $0 --profile payments_production --format env
  $0 --profile blackhole_production --format export
  $0 --list
  $0 --serve --port 8080
EOF
    exit 1
}

# Generate ~/.aws/config from sso-config.json if it doesn't exist
generate_aws_config() {
    local sso_config="/config/sso-config.json"
    local aws_config="/root/.aws/config"

    [ ! -f "$sso_config" ] && return

    # Skip if config already exists
    [ -f "$aws_config" ] && return

    mkdir -p /root/.aws
    info "Generating $aws_config from $sso_config..."

    local profiles
    profiles=$(jq -r '.profiles | keys[]' "$sso_config")

    for p in $profiles; do
        echo "[profile $p]"
        jq -r ".profiles[\"$p\"] | to_entries[] | \"\(.key) = \(.value)\"" "$sso_config"
        echo "output = json"
        echo ""
    done > "$aws_config"

    info "Generated AWS config with profiles: $(echo $profiles | tr '\n' ' ')"
}

generate_aws_config

# Parse arguments
profile=""
format="json"
action="get"
serve_port="8080"

while [ $# -gt 0 ]; do
    case "$1" in
        --profile)
            shift
            profile=$1
            shift
            ;;
        --format)
            shift
            format=$1
            shift
            ;;
        --list)
            action="list"
            shift
            ;;
        --serve)
            action="serve"
            shift
            ;;
        --port)
            shift
            serve_port=$1
            shift
            ;;
        --help|-h)
            usage
            ;;
        *)
            warn "Unknown option: $1"
            shift
            ;;
    esac
done

# Execute action
case "$action" in
    list)
        list_profiles
        ;;
    get)
        [ -z "$profile" ] && usage
        get_credentials "$profile" "$format"
        ;;
    serve)
        if ! command -v socat >/dev/null 2>&1; then
            error "socat is required for --serve mode but not found"
        fi
        info "Starting HTTP credential server on port $serve_port..."
        exec socat "TCP-LISTEN:${serve_port},fork,reuseaddr" \
            SYSTEM:"/usr/local/bin/serve-credentials.sh"
        ;;
esac
