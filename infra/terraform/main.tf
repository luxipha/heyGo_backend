locals {
  required_services = toset([
    "artifactregistry.googleapis.com",
    "cloudresourcemanager.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "orgpolicy.googleapis.com",
    "run.googleapis.com",
    "secretmanager.googleapis.com",
    "sts.googleapis.com",
  ])

  runtime_services = toset([
    "api-gateway",
    "driver-service",
    "payment-service",
    "trip-service",
  ])
}

resource "google_project_service" "required" {
  for_each = local.required_services

  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
}

resource "google_artifact_registry_repository" "containers" {
  project       = var.project_id
  location      = var.region
  repository_id = var.artifact_repository_id
  description   = "Immutable HeyGo backend container images"
  format        = "DOCKER"

  docker_config {
    immutable_tags = true
  }

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.required]
}

resource "google_service_account" "runtime" {
  for_each = local.runtime_services

  project      = var.project_id
  account_id   = each.key
  display_name = "HeyGo ${each.key} runtime"
  description  = "Runtime identity for the ${each.key} Cloud Run service."

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.required]
}
