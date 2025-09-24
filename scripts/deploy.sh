#!/bin/sh

set -e

echo "Deploying to Kubernetes cluster..."

kubectl apply -f k8s/dev/namespace.yaml
sleep 2
kubectl apply -f k8s/dev/secrets.yaml

kubectl apply -n uber-clone -f k8s/dev/

echo "Waiting for deployments to be ready..."
kubectl wait --for=condition=available deployment/api-gateway -n uber-clone --timeout=300s || true
kubectl wait --for=condition=available deployment/trip-service -n uber-clone --timeout=300s || true
kubectl wait --for=condition=available deployment/driver-service -n uber-clone --timeout=300s || true
kubectl wait --for=condition=available deployment/payment-service -n uber-clone --timeout=300s || true

echo "✅ Deployment completed successfully!"