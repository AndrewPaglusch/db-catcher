# db-catcher

db-catcher receives database dumps over HTTPS and stores them in S3-compatible object storage. Each database server pushes its dump with `curl` from a cron job, so there's no agent to install. db-catcher checks that every dump is complete, compresses it with zstd, keeps the newest N per database, and calls a webhook when a database stops sending dumps.

It deliberately doesn't do restores, scheduling, encryption, or a web UI. Restores come straight from the bucket. Encryption at rest is the bucket's job.

## Quick start

You need a bucket and an access key for it. Backblaze B2 is used in the examples below, though AWS S3, MinIO, SeaweedFS and other S3-compatible services work too.

```sh
curl -O https://raw.githubusercontent.com/AndrewPaglusch/db-catcher/main/compose.example.yaml
curl -o config.yaml https://raw.githubusercontent.com/AndrewPaglusch/db-catcher/main/config.example.yaml
mv compose.example.yaml compose.yaml

cat > .env <<'EOF'
AWS_ACCESS_KEY_ID=your-key-id
AWS_SECRET_ACCESS_KEY=your-application-key
EOF
```

Edit `config.yaml` with your bucket, webhook and backups, then:

```sh
docker compose up -d
docker compose logs -f
```

db-catcher listens on plain HTTP port 8080. Put it behind a reverse proxy that terminates TLS, or give it a certificate directly (see [TLS](#tls)). Dumps carry your data and the bearer token is the only credential, so don't expose it without TLS.

## Sending dumps

Every backup in the config has an `id`. That UUID is both its name on the wire and its password, so treat it like one. Generate a new one per database:

```sh
uuidgen | tr A-Z a-z
```

PostgreSQL (plain SQL format, which is pg_dump's default; `-Fc` custom format is not accepted):

```sh
#!/bin/bash
set -o pipefail
pg_dump mydb | curl -sSf -T - -H "Authorization: Bearer $DUMP_ID" https://catcher.example.com/upload
```

MySQL / MariaDB:

```sh
#!/bin/bash
set -o pipefail
mysqldump --single-transaction mydb | curl -sSf -T - -H "Authorization: Bearer $DUMP_ID" https://catcher.example.com/upload
```

Both parts of that pipeline matter. `set -o pipefail` makes the script fail when the dump tool fails, and `curl -f` makes it fail when db-catcher rejects the upload. Without them, cron will report success for a dump that never landed.

db-catcher only answers `200` once the dump is fully stored, and the body of that response is the object key. Any other status means nothing was stored.

### What counts as a valid dump

A dump is accepted when the upload finished cleanly, it isn't empty, and the tool's completion marker shows up in its last 4 KiB:

| type | marker |
|---|---|
| `postgres` | `-- PostgreSQL database dump complete` |
| `mysql` | `-- Dump completed` |

pg_dump and mysqldump only write that line once they've finished, so a dump cut short by a crash, a full disk or a killed process is missing it. Rejected dumps are thrown away. They don't count toward retention and they don't reset the alert timer.

## Configuration

See [`config.example.yaml`](config.example.yaml) for a full example.

| key | required | meaning |
|---|---|---|
| `listen` | no | Address to listen on. Default `:8080`. |
| `tls_cert`, `tls_key` | no | Serve HTTPS directly with this certificate and key. |
| `s3.endpoint` | no | S3 API endpoint. Leave out for AWS S3. |
| `s3.region` | yes | Region, e.g. `us-west-004` for B2. |
| `s3.bucket` | yes | Bucket name. |
| `notify_webhook` | * | Default URL alerts are POSTed to. |
| `alert_on_invalid` | no | Alert immediately when a dump is rejected. Default `false`. |
| `backups[].id` | yes | Random (v4) UUID, lowercase. The secret the client sends. |
| `backups[].name` | yes | Used in logs, alerts and as the object key prefix. Letters, digits, `.`, `_`, `-`. |
| `backups[].type` | yes | `postgres` or `mysql`. |
| `backups[].keep` | yes | How many valid dumps to keep. |
| `backups[].alert_after_minutes` | yes | Alert when no valid dump has arrived for this long. |
| `backups[].notify_webhook` | * | Overrides `notify_webhook` for this backup. |
| `backups[].alert_on_invalid` | no | Overrides `alert_on_invalid` for this backup. |

\* Every backup needs a webhook from one place or the other.

S3 credentials come from the standard `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` environment variables, never the config file.

The config is checked when it loads. Every problem is listed at once and db-catcher refuses to start until they're fixed.

### Reloading

Send `SIGHUP` to reload the config without dropping uploads:

```sh
docker kill -s HUP db-catcher
```

Backups and webhooks take effect right away. Changes to `listen`, `tls_*` and `s3` need a restart. If the new config has a mistake, db-catcher logs it and keeps running on the old one. Uploads already running finish with the settings they started with.

## Storage

Dumps are stored as `<name>/<UTC timestamp>.sql.zst`, for example `prod-orders/20261108T213000.123456Z.sql.zst`. After each successful upload, db-catcher lists that backup's prefix and deletes everything but the newest `keep` dumps. The prefix belongs to db-catcher, so don't keep anything else under it.

Large dumps are sent as a multipart upload in 16 MiB parts. Each upload in progress uses about 50 MiB of memory regardless of the dump's size, and nothing is written to local disk. If anything goes wrong partway through, the multipart upload is aborted so no partial object is left behind.

### Backblaze B2

1. Create a private bucket.
2. Under **Lifecycle Settings**, choose **Keep only the last version of the file**. B2 keeps every version of a file by default, and an S3 delete only hides it, so without this setting pruned dumps keep using (and billing for) storage.
3. Create an application key with read and write access to that bucket only. The key ID goes in `AWS_ACCESS_KEY_ID`, the key in `AWS_SECRET_ACCESS_KEY`.
4. Set `s3.endpoint` and `s3.region` from the bucket's endpoint. For `s3.us-west-004.backblazeb2.com` that's endpoint `https://s3.us-west-004.backblazeb2.com` and region `us-west-004`.

### AWS S3, MinIO and others

For AWS S3, leave `s3.endpoint` out. For anything else, set it to the service's S3 URL. Path-style requests are used whenever an endpoint is set. On a versioned bucket, add a lifecycle rule that expires noncurrent versions, for the same reason as the B2 setting above.

The access key needs `s3:PutObject`, `s3:ListBucket`, `s3:DeleteObject` and `s3:AbortMultipartUpload` on the bucket.

## TLS

The simplest setup is a reverse proxy in front. With Caddy:

```
catcher.example.com {
	reverse_proxy db-catcher:8080
}
```

With nginx, turn off request buffering, or nginx will write the whole dump to its own disk before passing it on:

```nginx
location /upload {
    proxy_pass http://db-catcher:8080;
    proxy_request_buffering off;
    proxy_http_version 1.1;
    client_max_body_size 0;
    proxy_read_timeout 1h;
}
```

To serve HTTPS from db-catcher itself, mount the certificate and key and set `tls_cert` and `tls_key`.

## Alerts

db-catcher checks every minute. When a backup goes longer than `alert_after_minutes` without a valid dump, it sends one `overdue` alert. When the next valid dump arrives, it sends one `recovered` alert. Nothing repeats in between. If the webhook can't be reached, the alert is retried each minute until it gets through.

After a restart, the timer for each backup picks up from its newest dump in the bucket. A backup with no dumps yet starts counting when db-catcher starts.

Alerts are a JSON POST:

```json
{
  "event": "overdue",
  "backup": "prod-orders",
  "last_valid": "2026-11-07T21:30:00Z",
  "minutes_since": 1501,
  "text": "prod-orders: no valid dump in 1501 minutes"
}
```

`event` is `overdue`, `recovered`, or `invalid` (with `alert_on_invalid` on; those also carry a `reason`). The `text` field means Slack incoming webhooks work as-is. ntfy works too: point the webhook at `https://ntfy.sh/your-topic` and the JSON shows up as the notification body.

## Restoring

Pick a dump from the bucket and pipe it back in. With the AWS CLI against B2:

```sh
aws s3 cp --endpoint-url https://s3.us-west-004.backblazeb2.com \
    s3://my-db-dumps/prod-orders/20261108T213000.123456Z.sql.zst - \
  | zstd -d | psql mydb
```

For MySQL, end the pipe with `mysql mydb` instead. Any S3 client works, and so does downloading the file from the B2 web UI and running `zstd -d` on it.

## Status codes

| code | meaning |
|---|---|
| 200 | Dump stored. The body is the object key. |
| 400 | The upload was cut off partway through. |
| 401 | Missing or unknown token. |
| 409 | Another upload for this backup is still running. |
| 422 | Dump rejected: empty, or the completion marker is missing. |
| 502 | The storage service returned an error. |

## Building

Images for `linux/amd64` and `linux/arm64` are published to `ghcr.io/andrewpaglusch/db-catcher`:

| tag | follows |
|---|---|
| `latest`, `1.2.3`, `1.2` | releases |
| `main` | the main branch |
| `sha-abc1234` | a specific commit |

To build it yourself:

```sh
docker build -t db-catcher .
# or, without Docker:
CGO_ENABLED=0 go build -o db-catcher .
```

`make test` runs the unit tests. `make test-integration` starts a throwaway SeaweedFS container and runs a full upload, prune and rejected-upload cycle against it.

## Disclaimer

This project is 100% AI generated. Every line of code, test and documentation was written by an AI model, with a human directing the design and reviewing the result.
