resource "random_password" "database" {
  length  = 32
  special = false
}

resource "random_password" "migration_database" {
  length  = 32
  special = false
}

resource "google_sql_database_instance" "primary" {
  project          = var.project_id
  name             = "heygo-postgres"
  region           = var.region
  database_version = "POSTGRES_16"

  deletion_protection = true

  settings {
    edition           = "ENTERPRISE"
    tier              = var.cloud_sql_tier
    availability_type = "ZONAL"
    disk_type         = "PD_SSD"
    disk_size         = 10
    disk_autoresize   = true

    backup_configuration {
      enabled                        = true
      point_in_time_recovery_enabled = true
      start_time                     = "02:00"
      transaction_log_retention_days = 7
    }

    ip_configuration {
      ipv4_enabled = true
      ssl_mode     = "ENCRYPTED_ONLY"
    }

    maintenance_window {
      day          = 7
      hour         = 3
      update_track = "stable"
    }
  }

  lifecycle {
    prevent_destroy = true
  }

  depends_on = [google_project_service.required]
}

resource "google_sql_database" "application" {
  project  = var.project_id
  name     = var.database_name
  instance = google_sql_database_instance.primary.name
}

resource "google_sql_user" "application" {
  project        = var.project_id
  name           = var.database_user
  instance       = google_sql_database_instance.primary.name
  password       = random_password.database.result
  database_roles = ["pg_read_all_data", "pg_write_all_data"]
}

resource "google_sql_user" "migration" {
  project  = var.project_id
  name     = "heygo_migrator"
  instance = google_sql_database_instance.primary.name
  password = random_password.migration_database.result
}

resource "google_project_iam_member" "runtime_cloud_sql_client" {
  for_each = local.runtime_services

  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:${google_service_account.runtime[each.value].email}"
}

resource "google_service_account" "migration" {
  project      = var.project_id
  account_id   = "heygo-migration"
  display_name = "HeyGo database migration"

  depends_on = [google_project_service.required]
}

resource "google_project_iam_member" "migration_cloud_sql_client" {
  project = var.project_id
  role    = "roles/cloudsql.client"
  member  = "serviceAccount:${google_service_account.migration.email}"
}
