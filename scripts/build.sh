#!/bin/sh

set -e

echo "Building Docker images..."

IMAGE_TAG="${IMAGE_TAG:-0.1.0}"

# Use Minikube's Docker daemon
eval "$(minikube docker-env)"

# API Gateway
echo "Building api-gateway..."
docker build -t "heygo/api-gateway:${IMAGE_TAG}" -f services/api-gateway/Dockerfile .

# Payment service
echo "Building payment-service..."
docker build -t "heygo/payment-service:${IMAGE_TAG}" -f services/payment-service/Dockerfile .

# Trip service
echo "Building trip-service..."
docker build -t "heygo/trip-service:${IMAGE_TAG}" -f services/trip-service/Dockerfile .

# Driver service
echo "Building driver-service..."
docker build -t "heygo/driver-service:${IMAGE_TAG}" -f services/driver-service/Dockerfile .

echo "Docker images built successfully."
