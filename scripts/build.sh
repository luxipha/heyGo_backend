#!/bin/sh

set -e

echo "Building Docker images..."

# Web frontend
echo "Building web frontend..."
docker build -t uber-clone/web:latest -f web-frontend/Dockerfile .

# Use Minikube's Docker daemon
eval $(minikube docker-env)

# API Gateway
echo "Building api-gateway..."
docker build -t uber-clone/api-gateway:latest -f services/api-gateway/Dockerfile .

# Payment service
echo "Building payment-service..."
docker build -t uber-clone/payment-service:latest -f services/payment-service/Dockerfile .

# Trip service
echo "Building trip-service..."
docker build -t uber-clone/trip-service:latest -f services/trip-service/Dockerfile .

# Driver service
echo "Building driver-service..."
docker build -t uber-clone/driver-service:latest -f services/driver-service/Dockerfile .

echo "Docker images built successfully."