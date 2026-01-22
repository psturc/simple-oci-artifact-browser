# Simple OCI Artifact Browser

A lightweight web file browser for OCI artifacts stored in Quay.io. Automatically syncs and displays artifacts from configured repositories.

## Features

- Syncs OCI artifacts from Quay.io repositories at configurable intervals
- Browse and view files directly in the browser
- Automatic decompression of .gz files
- Supports viewing HTML, logs, XML, JSON, and other text-based files

## Environment Variables

- `QUAY_ORG_REPOS` - Comma-separated list of Quay.io repositories (required)
- `PORT` - Server port (required)
- `SYNC_INTERVAL_MINUTES` - Sync interval in minutes (default: 1)

## Run Locally

```bash
export QUAY_ORG_REPOS=org/repo/name
export PORT=8080
go run main.go
```

## Deploy on Kubernetes

```bash
kubectl apply -f deploy.yaml
```

Update the `QUAY_ORG_REPOS` environment variable in `deploy.yaml` with your repositories.
