#!/bin/bash
set -euo pipefail

export KUBECONFIG="$HOME/.kube/config"
export $(xargs <./development/envfile)

go build ./cmd/console

./console \
    --cert-file=./development/server.pem \
    --key-file=./development/server-key.pem
