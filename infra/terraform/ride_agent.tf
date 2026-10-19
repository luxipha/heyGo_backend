resource "google_cloud_run_v2_service" "ride_agent" {
  count = var.deploy_services && var.deploy_ride_agent ? 1 : 0

  project             = var.project_id
  name                = "ride-agent-gateway"
  location            = var.region
  deletion_protection = true
  ingress             = "INGRESS_TRAFFIC_ALL"

  lifecycle {
    ignore_changes = [client, client_version, template[0].containers[0].image]
  }

  template {
    service_account                  = google_service_account.runtime["ride-agent-gateway"].email
    timeout                          = "30s"
    max_instance_request_concurrency = 80

    scaling {
      min_instance_count = 0
      max_instance_count = 1
    }

    containers {
      image = "${local.runtime_image_prefix}/ride-agent-gateway:${var.image_tag}"
      ports {
        name           = "http1"
        container_port = 8080
      }
      resources {
        limits            = { cpu = "1", memory = "256Mi" }
        cpu_idle          = true
        startup_cpu_boost = true
      }
      # The existing deterministic Places tools remain enabled. LLM planning
      # requires a separately reviewed provider/configuration before activation.
      env {
        name  = "RIDE_AGENT_LLM_ENABLED"
        value = "false"
      }
      dynamic "env" {
        for_each = {
          RIDE_AGENT_INTERNAL_TOKEN = "ride-agent-internal-token"
          RIDE_AGENT_PLACES_KEY     = "ride-agent-places-key"
        }
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
        initial_delay_seconds = 2
        timeout_seconds       = 5
        period_seconds        = 10
        failure_threshold     = 12
        http_get {
          path = "/health"
          port = 8080
        }
      }
    }
  }
  depends_on = [google_secret_manager_secret_iam_member.runtime_accessor]
}

resource "google_cloud_run_v2_service_iam_member" "github_ride_agent_invoker" {
  count    = var.deploy_services && var.deploy_ride_agent ? 1 : 0
  project  = var.project_id
  location = var.region
  name     = google_cloud_run_v2_service.ride_agent[0].name
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.github_deployer.email}"
}
