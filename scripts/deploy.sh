#!/bin/sh

set -e

echo "Deploying to Kubernetes cluster..."

kubectl apply -f infra/kubernetes/dev/namespace.yaml
kubectl apply -f infra/kubernetes/dev/secrets.yaml
kubectl apply -f infra/kubernetes/dev/apache-kafka.yaml
kubectl apply -f infra/kubernetes/dev/api-gateway.yaml
kubectl apply -f infra/kubernetes/dev/trip-service.yaml
kubectl apply -f infra/kubernetes/dev/driver-service.yaml
kubectl apply -f infra/kubernetes/dev/payment-service.yaml

echo "Waiting for deployments to be ready..."
kubectl rollout status statefulset/apache-kafka -n uber-clone --timeout=300s
kubectl rollout status deployment/api-gateway -n uber-clone --timeout=300s
kubectl rollout status deployment/trip-service -n uber-clone --timeout=300s
kubectl rollout status deployment/driver-service -n uber-clone --timeout=300s
kubectl rollout status deployment/payment-service -n uber-clone --timeout=300s

echo "✅ Deployment completed successfully!"
