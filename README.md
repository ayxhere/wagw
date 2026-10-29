# wagw — AyX WhatsApp gateway (Go + whatsmeow)

Experimental replacement for the Node.js/Baileys gateway in AyXWhatsapp.

## Status: TEMPORARY — login only

Right now this does **QR pairing + status** and nothing else. No messaging,
no media, no groups. Messaging comes later (see the migration plan).

## Run

```sh
go mod tidy
WAGW_API_TOKEN=secret WAGW_PORT=18080 WAGW_AUTH_DIR=./auth go run .
```

## Endpoints (so far)

| Method | Path      | Auth | Description                              |
|--------|-----------|------|------------------------------------------|
| GET    | /health   | no   | `{ok:true}`                              |
| GET    | /status   | yes  | paired/connected/loggedIn/hasQR/device_name |
| GET    | /qr.png   | yes  | raw QR string (app renders it)           |

Auth = `X-Api-Token` header (GET /health exempt), same as the Node gateway.

## Device name

Reads `<WAGW_AUTH_DIR>/../device_name` (same file the Node gateway uses).
whatsmeow note: the Linked-Devices display name's customizable part is the
OS slot — see the investigation in the main repo.
