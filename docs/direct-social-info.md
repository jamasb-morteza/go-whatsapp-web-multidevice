# Direct WhatsApp social profile lookup

Deployed 2026-10-25 on 192.168.50.41.

Request flow: `https://api.joolix.io/contact-social-info/get-wa-info?mobile=...&session_id=...`
→ APIPark/Apinto → `http://192.168.50.41:3900/user/social-info` → WhatsApp.

The gateway retains consumer authentication and replaces the upstream Authorization header with the existing WhatsApp Basic Auth credential. Do not place that credential in clients or documentation.

The handler selects the device with the existing device middleware (`X-Device-Id`, then `device_id`, then the social lookup's `session_id`). It checks registration, fetches user information and avatar, downloads at most 5 MiB, and returns the existing `data.user.avatar.base64`, `data.user.name`, and `meta` shape. Private avatars remain empty; other avatar errors retain the previous workflow fallback image. Unregistered numbers return HTTP 422 with `error.code=NOT_ON_WHATSAPP`.

Changed source: `src/usecase/social_profile.go`, `social_profile_fallback.go`, `social_profile_test.go`, `src/ui/rest/social_profile_rest.go`, `src/ui/rest/user.go`, and `src/ui/rest/middleware/device.go`.

REST deployment image: `whatsapp-go:direct-social-20261025`. Compose uses this image for `whatsapp_go`. The previous image `whatsapp-go:v8.6.0` remains available. The MCP container remains stopped as it was before this change.

APIPark runtime service/router were updated through its local gateway API. The `api_latest` and `upstream_latest` pointers reference new appended configuration versions in MySQL, so future publishes use the new upstream and rewrite. Existing historical releases were preserved. No APIPark console release was created; the runtime switch and saved draft configuration were updated directly with a rollback backup.

## Verification

`go test ./usecase ./ui/rest/...` passed on the host and in the Alpine build environment. Public HTTPS requests using one existing subscribed consumer passed:

- Real profile: HTTP 200, name present, real image returned as 157571 Base64 data-URI characters, avatar success true.
- Unknown `session_id`: HTTP 404, `DEVICE_NOT_FOUND`.
- Unregistered number: HTTP 422, `NOT_ON_WHATSAPP`.
- Direct endpoint without authentication: HTTP 401.
- Missing mobile: HTTP 400.

Laravel UI end-to-end verification was not run: the local default PHP CLI lacks the Redis extension. No Laravel source, Blade views, translations, or assets changed.

## Rebuild

The standard Docker build's unconditional `go mod download` currently fails with HTTP 403 for some modules (including the optional purego SQLite driver). The deployed image was successfully built with the native SQLite driver and the server's existing Go module cache:

```sh
rtk proxy docker run --rm \
  -v /opt/whatsapp-go/src:/source:ro \
  -v /root/go/pkg/mod:/go/pkg/mod \
  -v /tmp/wa-direct-build:/out -w /source \
  golang:1.26-alpine3.23 sh -c 'apk add --no-cache gcc musl-dev && go test ./usecase ./ui/rest/... && go build -ldflags="-w -s" -o /out/whatsapp'
```

The final image uses the existing runtime base plus that binary. Its Dockerfile is preserved with the deployment backup. This does not require changing go.mod or go.sum.

## Rollback

Gateway backup, switch/rollback scripts, and sanitized public verification results are in `/opt/apipark/backups/direct-whatsapp-20261025/`. The backup directory is root-only because runtime route backups contain upstream authentication.

Restore n8n routing and saved draft configuration:

```sh
rtk proxy python3 /opt/apipark/backups/direct-whatsapp-20261025/rollback_gateway.py
```

To also restore the previous WhatsApp REST image, restore `/opt/whatsapp-go/docker-compose.yml.before-direct-social-20261025` to `docker-compose.yml`, then run `rtk proxy docker compose -f /opt/whatsapp-go/docker-compose.yml up -d --no-deps whatsapp_go`.

The n8n workflow was retained for rollback and other n8n workflows remain active. WhatsApp startup logs also showed existing webhook callbacks returning HTTP 405; that separate webhook issue was not changed by this lookup deployment.
