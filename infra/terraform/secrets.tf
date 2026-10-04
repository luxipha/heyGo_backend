locals {
  runtime_secret_ids = toset([
    "database-url",
    "migration-database-url",
    "internal-service-token",
    "otp-pepper",
    "admin-settings-key",
    "sms-provider-token",
    "casperid-app-id",
    "casperid-api-secret",
    "monnify-api-key",
    "monnify-secret-key",
    "monnify-contract-code",
  ])

  service_secret_access = {
    api-gateway = setunion(toset([
      "database-url",
      "internal-service-token",
      "otp-pepper",
      "admin-settings-key",
    ]), var.sms_provider_url == "" ? toset([]) : toset(["sms-provider-token"]))
    driver-service = toset([
      "database-url",
      "internal-service-token",
    ])
    payment-service = toset([
      "database-url",
      "internal-service-token",
      "monnify-api-key",
      "monnify-secret-key",
      "monnify-contract-code",
    ])
    trip-service = toset([
      "database-url",
      "internal-service-token",
    ])
  }

  service_secret_bindings = merge([
    for service, secret_ids in local.service_secret_access : {
      for secret_id in secret_ids :
      "${service}:${secret_id}" => {
        service   = service
        secret_id = secret_id
      }
    }
  ]...)
}

resource "random_password" "internal_service_token" {
  length  = 48
  special = false
}

resource "random_password" "otp_pepper" {
  length  = 64
  special = false
}

resource "random_id" "admin_settings_key" {
  byte_length = 32
}

resource "google_secret_manager_secret" "runtime" {
  for_each = local.runtime_secret_ids

  project   = var.project_id
  secret_id = "heygo-${each.value}"

  replication {
    auto {}
  }

  depends_on = [google_project_service.required]
}

resource "google_secret_manager_secret_version" "database_url" {
  secret = google_secret_manager_secret.runtime["database-url"].id
  secret_data = format(
    "postgresql://%s:%s@/%s?host=/cloudsql/%s&sslmode=disable",
    var.database_user,
    random_password.database.result,
    var.database_name,
    google_sql_database_instance.primary.connection_name,
  )
}

resource "google_secret_manager_secret_version" "migration_database_url" {
  secret = google_secret_manager_secret.runtime["migration-database-url"].id
  secret_data = format(
    "postgresql://%s:%s@/%s?host=/cloudsql/%s&sslmode=disable",
    google_sql_user.migration.name,
    random_password.migration_database.result,
    var.database_name,
    google_sql_database_instance.primary.connection_name,
  )
}

resource "google_secret_manager_secret_version" "internal_service_token" {
  secret      = google_secret_manager_secret.runtime["internal-service-token"].id
  secret_data = random_password.internal_service_token.result
}

resource "google_secret_manager_secret_version" "otp_pepper" {
  secret      = google_secret_manager_secret.runtime["otp-pepper"].id
  secret_data = random_password.otp_pepper.result
}

resource "google_secret_manager_secret_version" "admin_settings_key" {
  secret      = google_secret_manager_secret.runtime["admin-settings-key"].id
  secret_data = random_id.admin_settings_key.hex
}

resource "google_secret_manager_secret_iam_member" "runtime_accessor" {
  for_each = local.service_secret_bindings

  project   = var.project_id
  secret_id = google_secret_manager_secret.runtime[each.value.secret_id].secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.runtime[each.value.service].email}"
}

resource "google_secret_manager_secret_iam_member" "migration_accessor" {
  project   = var.project_id
  secret_id = google_secret_manager_secret.runtime["migration-database-url"].secret_id
  role      = "roles/secretmanager.secretAccessor"
  member    = "serviceAccount:${google_service_account.migration.email}"
}
