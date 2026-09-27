resource "google_cloud_run_v2_job" "migration" {
  count = var.image_tag != "" ? 1 : 0

  project             = var.project_id
  name                = "heygo-migration"
  location            = var.region
  deletion_protection = true

  lifecycle {
    ignore_changes = [
      client,
      client_version,
      template[0].template[0].containers[0].image,
    ]
  }

  template {
    template {
      service_account = google_service_account.migration.email
      timeout         = "1200s"
      max_retries     = 0

      volumes {
        name = "cloudsql"
        cloud_sql_instance {
          instances = [google_sql_database_instance.primary.connection_name]
        }
      }

      containers {
        image = "${local.runtime_image_prefix}/migration:${var.image_tag}"

        resources {
          limits = {
            cpu    = "1"
            memory = "512Mi"
          }
        }

        volume_mounts {
          name       = "cloudsql"
          mount_path = "/cloudsql"
        }

        env {
          name = "DATABASE_URL"
          value_source {
            secret_key_ref {
              secret  = google_secret_manager_secret.runtime["migration-database-url"].secret_id
              version = "latest"
            }
          }
        }
      }
    }
  }

  depends_on = [
    google_project_iam_member.migration_cloud_sql_client,
    google_secret_manager_secret_iam_member.migration_accessor,
  ]
}
