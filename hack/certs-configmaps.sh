#!/usr/bin/env bash

set -e

# certs
kubectl create configmap certs -n default --from-file=./certs/generated
