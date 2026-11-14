#!/bin/sh
# Starts a single-node SeaweedFS with its S3 gateway on port 8333. The S3
# credentials come from .env and are written into SeaweedFS's identity config
# here, so .env stays the only place they're set.
set -eu

: "${S3_ACCESS_KEY:?set S3_ACCESS_KEY in .env}"
: "${S3_SECRET_KEY:?set S3_SECRET_KEY in .env}"

cat > /tmp/s3.json <<EOF
{
  "identities": [
    {
      "name": "trend-detective",
      "credentials": [
        { "accessKey": "${S3_ACCESS_KEY}", "secretKey": "${S3_SECRET_KEY}" }
      ],
      "actions": ["Admin", "Read", "Write", "List", "Tagging"]
    }
  ]
}
EOF

# volumeSizeLimitMB keeps volume files small on a laptop. allowedOrigins lets
# the browser use presigned URLs directly.
exec weed server \
  -dir=/data \
  -ip.bind=0.0.0.0 \
  -master.volumeSizeLimitMB=1024 \
  -volume.max=0 \
  -s3 \
  -s3.port=8333 \
  -s3.config=/tmp/s3.json \
  -s3.allowedOrigins="${S3_ALLOWED_ORIGINS:-*}"
