# SSO credential server

Local HTTP server that automates AWS SSO browser login (via browserless Chrome) and serves temporary role credentials to client tools.

## Running

```bash
docker compose up -d browserless_chrome sso_server
```

The credential server listens on `127.0.0.1:${SSO_SERVER_PORT:-8080}`.

## Client usage

Fetch credentials for an AWS profile:

```bash
eval $(curl -sf --max-time 45 "http://localhost:${SSO_SERVER_PORT:-8080}/credentials?profile=payments_us_production&format=export")
export AWS_CONFIG_FILE=/dev/null
aws sts get-caller-identity
```

Supported formats: `env` (default), `export`, `json`. Add `&refresh=true` to purge the cached role credentials and re-derive a fresh set. This is cheap (~1s) when the SSO session is still valid; it escalates to a full browser login only if the SSO session has also expired.

### Port note for client skills

Some client tools default `SSO_CREDENTIAL_URL` to `http://localhost:6789`, which is wrong for this server. Either:

- Override with `export SSO_CREDENTIAL_URL=http://localhost:8080`, or
- Run the server on 6789 via `SSO_SERVER_PORT=6789 docker compose up -d sso_server`.

## Endpoints

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/health` | Liveness check |
| GET | `/profiles` | JSON array of available profiles |
| GET | `/credentials?profile=<name>&format=<env\|export\|json>&refresh=<true\|false>` | Get credentials (`refresh=true` purges role cache only) |
| POST | `/login` | Perform SSO login and persist browser session (Go binary, separate process) |
| POST | `/backoffice-screenshot` | Capture a booking screenshot |

## Fresh-credentials guarantee

Whenever cached role credentials have less than `MIN_CREDENTIAL_TTL_SECONDS` (default `600` = 10 minutes) of life remaining, the `/credentials` endpoint purges the role-credentials cache and re-derives a fresh set from the existing SSO session (~1s, no browser). It falls back to a full browser SSO login only when the SSO session itself has expired. This prevents clients from receiving near-expired credentials that fail with `ExpiredToken` mid-use.

All cache mutation (role-cache purge on `refresh=true` and TTL-triggered re-auth) runs inside a shared `flock` mutex, so concurrent requests cannot race the cache wipe.

Successful responses include:

- `X-Credentials-Expiry`: ISO-8601 timestamp when credentials expire.
- `X-Credentials-TTL-Seconds`: integer seconds of life remaining.
- For `format=json`, an `expiration` field in the body.

Override the threshold by setting `MIN_CREDENTIAL_TTL_SECONDS` in your shell or `.env`:

```bash
MIN_CREDENTIAL_TTL_SECONDS=300 docker compose up -d sso_server
```

Set to `0` to disable the TTL guard (restores pre-fix behaviour).
