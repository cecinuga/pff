# secrets/

Mounted read-only into the backend at `/run/pff-secrets`. Everything in this folder except this README is git-ignored.

| File | Needed when | Where to get it |
|---|---|---|
| `firebase-service-account.json` | `PFF_AUTH_MODE=firebase` or `PFF_PUSH_PROVIDER=fcm` | Firebase console → Project settings → Service accounts → Generate new private key |
| `enablebanking.pem` | `PFF_BANK_PROVIDER=enablebanking` | Private key of your application in the Enable Banking Control Panel |

The backend runs as a non-root user (uid 65532), so make the files readable: `chmod 644 secrets/*`. File permissions alone don't protect them. Keep the host locked down.
