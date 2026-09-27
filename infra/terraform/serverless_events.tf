resource "google_cloud_tasks_queue" "offer_expiry" {
  project  = var.project_id
  location = var.region
  name     = "driver-offer-expiry"

  rate_limits {
    max_concurrent_dispatches = 10
    max_dispatches_per_second = 10
  }

  retry_config {
    max_attempts       = 8
    max_retry_duration = "3600s"
    min_backoff        = "1s"
    max_backoff        = "300s"
    max_doublings      = 5
  }

  depends_on = [google_project_service.required]
}

resource "google_project_iam_member" "driver_tasks_enqueuer" {
  project = var.project_id
  role    = "roles/cloudtasks.enqueuer"
  member  = "serviceAccount:${google_service_account.runtime["driver-service"].email}"
}

resource "google_service_account_iam_member" "driver_event_invoker_user" {
  service_account_id = google_service_account.event_invoker.name
  role               = "roles/iam.serviceAccountUser"
  member             = "serviceAccount:${google_service_account.runtime["driver-service"].email}"
}

resource "google_cloud_run_v2_service_iam_member" "event_invoker" {
  for_each = var.deploy_services ? {
    api-gateway     = google_cloud_run_v2_service.api_gateway[0].name
    driver-service  = google_cloud_run_v2_service.driver[0].name
    payment-service = google_cloud_run_v2_service.payment[0].name
    trip-service    = google_cloud_run_v2_service.trip[0].name
  } : {}

  project  = var.project_id
  location = var.region
  name     = each.value
  role     = "roles/run.invoker"
  member   = "serviceAccount:${google_service_account.event_invoker.email}"
}

resource "google_cloud_scheduler_job" "outbox_recovery" {
  count = var.deploy_services ? 1 : 0

  project          = var.project_id
  region           = var.region
  name             = "outbox-recovery"
  description      = "Recover transactional outbox rows left by interrupted requests."
  schedule         = "* * * * *"
  time_zone        = "Etc/UTC"
  attempt_deadline = "180s"

  retry_config {
    retry_count          = 3
    min_backoff_duration = "5s"
    max_backoff_duration = "60s"
    max_doublings        = 3
  }

  http_target {
    uri         = "${google_cloud_run_v2_service.trip[0].uri}/internal/tasks/outbox/drain"
    http_method = "POST"
    body        = base64encode("{}")

    headers = {
      "Content-Type" = "application/json"
    }

    oidc_token {
      service_account_email = google_service_account.event_invoker.email
      audience              = google_cloud_run_v2_service.trip[0].uri
    }
  }

  depends_on = [google_cloud_run_v2_service_iam_member.event_invoker]
}

resource "google_cloud_scheduler_job" "gateway_maintenance" {
  count = var.deploy_services ? 1 : 0

  project          = var.project_id
  region           = var.region
  name             = "gateway-maintenance"
  description      = "Process privacy cleanup and queued notification delivery."
  schedule         = "* * * * *"
  time_zone        = "Etc/UTC"
  attempt_deadline = "180s"

  retry_config {
    retry_count          = 3
    min_backoff_duration = "5s"
    max_backoff_duration = "60s"
    max_doublings        = 3
  }

  http_target {
    uri         = "${google_cloud_run_v2_service.api_gateway[0].uri}/internal/tasks/maintenance"
    http_method = "POST"
    body        = base64encode("{}")

    headers = {
      "Content-Type" = "application/json"
    }

    oidc_token {
      service_account_email = google_service_account.event_invoker.email
      audience              = "https://pubsub.heygo.internal/api-gateway"
    }
  }

  depends_on = [google_cloud_run_v2_service_iam_member.event_invoker]
}
