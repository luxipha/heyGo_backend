locals {
  runtime_image_prefix = "${var.region}-docker.pkg.dev/${var.project_id}/${var.artifact_repository_id}"

  api_gateway_secrets = {
    DATABASE_URL           = "database-url"
    INTERNAL_SERVICE_TOKEN = "internal-service-token"
    CASPERID_APP_ID        = "casperid-app-id"
    CASPERID_API_SECRET    = "casperid-api-secret"
  }
  driver_service_secrets = {
    DATABASE_URL           = "database-url"
    INTERNAL_SERVICE_TOKEN = "internal-service-token"
  }
  payment_service_secrets = {
    DATABASE_URL           = "database-url"
    INTERNAL_SERVICE_TOKEN = "internal-service-token"
    MONNIFY_API_KEY        = "monnify-api-key"
    MONNIFY_SECRET_KEY     = "monnify-secret-key"
    MONNIFY_CONTRACT_CODE  = "monnify-contract-code"
  }
  trip_service_secrets = {
    DATABASE_URL           = "database-url"
    INTERNAL_SERVICE_TOKEN = "internal-service-token"
    CASPERID_APP_ID        = "casperid-app-id"
    CASPERID_API_SECRET    = "casperid-api-secret"
  }
}

resource "google_cloud_run_v2_service" "driver" {
  count = var.deploy_services ? 1 : 0

  project             = var.project_id
  name                = "driver-service"
  location            = var.region
  deletion_protection = true
  ingress             = "INGRESS_TRAFFIC_ALL"

  lifecycle {
    ignore_changes = [template[0].containers[0].image]
  }

  template {
    service_account                  = google_service_account.runtime["driver-service"].email
    timeout                          = "300s"
    max_instance_request_concurrency = 80

    scaling {
      min_instance_count = 1
      max_instance_count = 3
    }

    volumes {
      name = "cloudsql"
      cloud_sql_instance {
        instances = [google_sql_database_instance.primary.connection_name]
      }
    }

    containers {
      image = "${local.runtime_image_prefix}/driver-service:${var.image_tag}"

      ports {
        name           = "h2c"
        container_port = 8080
      }

      resources {
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
        cpu_idle          = false
        startup_cpu_boost = true
      }

      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }

      env {
        name  = "ENVIRONMENT"
        value = "production"
      }
      env {
        name  = "GCP_PROJECT_ID"
        value = var.project_id
      }

      dynamic "env" {
        for_each = local.driver_service_secrets
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime[env.value].secret_id
              version = "latest"
            }
          }
        }
      }

      startup_probe {
        initial_delay_seconds = 5
        timeout_seconds       = 5
        period_seconds        = 10
        failure_threshold     = 24
        grpc {
          port = 8080
        }
      }
    }
  }

  depends_on = [
    google_project_iam_member.runtime_cloud_sql_client,
    google_secret_manager_secret_iam_member.runtime_accessor,
  ]
}

resource "google_cloud_run_v2_service" "trip" {
  count = var.deploy_services ? 1 : 0

  project             = var.project_id
  name                = "trip-service"
  location            = var.region
  deletion_protection = true
  ingress             = "INGRESS_TRAFFIC_ALL"

  lifecycle {
    ignore_changes = [template[0].containers[0].image]
  }

  template {
    service_account                  = google_service_account.runtime["trip-service"].email
    timeout                          = "300s"
    max_instance_request_concurrency = 80

    scaling {
      min_instance_count = 1
      max_instance_count = 3
    }

    volumes {
      name = "cloudsql"
      cloud_sql_instance {
        instances = [google_sql_database_instance.primary.connection_name]
      }
    }

    containers {
      image = "${local.runtime_image_prefix}/trip-service:${var.image_tag}"

      ports {
        name           = "h2c"
        container_port = 8080
      }

      resources {
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
        cpu_idle          = false
        startup_cpu_boost = true
      }

      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }

      env {
        name  = "ENV"
        value = "production"
      }
      env {
        name  = "GCP_PROJECT_ID"
        value = var.project_id
      }
      env {
        name  = "CASPERID_BASE_URL"
        value = var.casperid_base_url
      }

      dynamic "env" {
        for_each = local.trip_service_secrets
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime[env.value].secret_id
              version = "latest"
            }
          }
        }
      }

      startup_probe {
        initial_delay_seconds = 5
        timeout_seconds       = 5
        period_seconds        = 10
        failure_threshold     = 24
        grpc {
          port = 8080
        }
      }
    }
  }

  depends_on = [
    google_project_iam_member.runtime_cloud_sql_client,
    google_secret_manager_secret_iam_member.runtime_accessor,
  ]
}

resource "google_cloud_run_v2_service" "payment" {
  count = var.deploy_services ? 1 : 0

  project             = var.project_id
  name                = "payment-service"
  location            = var.region
  deletion_protection = true
  ingress             = "INGRESS_TRAFFIC_ALL"

  lifecycle {
    ignore_changes = [template[0].containers[0].image]
  }

  template {
    service_account                  = google_service_account.runtime["payment-service"].email
    timeout                          = "300s"
    max_instance_request_concurrency = 80

    scaling {
      min_instance_count = 1
      max_instance_count = 3
    }

    volumes {
      name = "cloudsql"
      cloud_sql_instance {
        instances = [google_sql_database_instance.primary.connection_name]
      }
    }

    containers {
      image = "${local.runtime_image_prefix}/payment-service:${var.image_tag}"

      ports {
        name           = "http1"
        container_port = 8080
      }

      resources {
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
        cpu_idle          = false
        startup_cpu_boost = true
      }

      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }

      env {
        name  = "ENVIRONMENT"
        value = "production"
      }
      env {
        name  = "GCP_PROJECT_ID"
        value = var.project_id
      }
      env {
        name  = "APP_URL"
        value = var.app_url
      }
      env {
        name  = "MONNIFY_BASE_URL"
        value = var.monnify_base_url
      }
      env {
        name  = "MONNIFY_REDIRECT_URL"
        value = "${trimsuffix(var.app_url, "/")}?payment=pending"
      }

      dynamic "env" {
        for_each = local.payment_service_secrets
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime[env.value].secret_id
              version = "latest"
            }
          }
        }
      }

      startup_probe {
        initial_delay_seconds = 5
        timeout_seconds       = 5
        period_seconds        = 10
        failure_threshold     = 24
        http_get {
          path = "/health"
          port = 8080
        }
      }
    }
  }

  depends_on = [
    google_project_iam_member.runtime_cloud_sql_client,
    google_secret_manager_secret_iam_member.runtime_accessor,
  ]
}

resource "google_cloud_run_v2_service" "api_gateway" {
  count = var.deploy_services ? 1 : 0

  project             = var.project_id
  name                = "api-gateway"
  location            = var.region
  deletion_protection = true
  ingress             = "INGRESS_TRAFFIC_ALL"

  lifecycle {
    ignore_changes = [template[0].containers[0].image]
  }

  template {
    service_account                  = google_service_account.runtime["api-gateway"].email
    timeout                          = "300s"
    max_instance_request_concurrency = 80

    # The API Gateway consumes one shared pull subscription and owns in-memory
    # WebSocket connections. Keep one instance until Pub/Sub push/fan-out is used.
    scaling {
      min_instance_count = 1
      max_instance_count = 1
    }

    volumes {
      name = "cloudsql"
      cloud_sql_instance {
        instances = [google_sql_database_instance.primary.connection_name]
      }
    }

    containers {
      image = "${local.runtime_image_prefix}/api-gateway:${var.image_tag}"

      ports {
        name           = "http1"
        container_port = 8080
      }

      resources {
        limits = {
          cpu    = "1"
          memory = "1Gi"
        }
        cpu_idle          = false
        startup_cpu_boost = true
      }

      volume_mounts {
        name       = "cloudsql"
        mount_path = "/cloudsql"
      }

      env {
        name  = "ENV"
        value = "production"
      }
      env {
        name  = "GCP_PROJECT_ID"
        value = var.project_id
      }
      env {
        name  = "ALLOWED_ORIGINS"
        value = var.allowed_origins
      }
      env {
        name  = "DRIVER_SERVICE_URL"
        value = "${trimprefix(google_cloud_run_v2_service.driver[0].uri, "https://")}:443"
      }
      env {
        name  = "DRIVER_SERVICE_AUDIENCE"
        value = google_cloud_run_v2_service.driver[0].uri
      }
      env {
        name  = "TRIP_SERVICE_URL"
        value = "${trimprefix(google_cloud_run_v2_service.trip[0].uri, "https://")}:443"
      }
      env {
        name  = "TRIP_SERVICE_AUDIENCE"
        value = google_cloud_run_v2_service.trip[0].uri
      }
      env {
        name  = "PAYMENT_SERVICE_URL"
        value = google_cloud_run_v2_service.payment[0].uri
      }
      env {
        name  = "PAYMENT_SERVICE_AUDIENCE"
        value = google_cloud_run_v2_service.payment[0].uri
      }
      env {
        name  = "CASPERID_JWKS_URL"
        value = var.casperid_jwks_url
      }
      env {
        name  = "CASPERID_ISSUER"
        value = var.casperid_issuer
      }
      env {
        name  = "CASPERID_TOKEN_URL"
        value = var.casperid_token_url
      }
      env {
        name  = "CASPERID_DRIVER_REDIRECT_URI"
        value = var.casperid_driver_redirect_uri
      }

      dynamic "env" {
        for_each = local.api_gateway_secrets
        content {
          name = env.key
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime[env.value].secret_id
              version = "latest"
            }
          }
        }
      }

      startup_probe {
        initial_delay_seconds = 5
        timeout_seconds       = 5
        period_seconds        = 10
        failure_threshold     = 24
        http_get {
          path = "/health"
          port = 8080
        }
      }
    }
  }

  depends_on = [
    google_project_iam_member.runtime_cloud_sql_client,
    google_secret_manager_secret_iam_member.runtime_accessor,
  ]
}

resource "google_cloud_run_v2_service_iam_member" "public_api_gateway" {
  count = var.deploy_services ? 1 : 0

  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.api_gateway[0].name
  role     = "roles/run.invoker"
  member   = "allUsers"
}

resource "google_cloud_run_v2_service_iam_member" "public_payment_webhook" {
  count = var.deploy_services ? 1 : 0

  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.payment[0].name
  role     = "roles/run.invoker"
  member   = "allUsers"
}

resource "google_cloud_run_v2_service_iam_member" "api_gateway_internal_invoker" {
  for_each = var.deploy_services ? toset(["driver-service", "trip-service"]) : toset([])

  project  = var.project_id
  location = var.region
  name = {
    driver-service = google_cloud_run_v2_service.driver[0].name
    trip-service   = google_cloud_run_v2_service.trip[0].name
  }[each.value]
  role   = "roles/run.invoker"
  member = "serviceAccount:${google_service_account.runtime["api-gateway"].email}"
}
