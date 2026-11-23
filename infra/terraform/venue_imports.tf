# These existing containers and narrow grants were created by the venue
# configuration rollout. Import them instead of trying to create duplicates.
# Secret values/versions remain externally provisioned; no key data enters Terraform.
import {
  for_each = toset(["venue-partner-service-key", "venue-partner-inbound-key"])
  to       = google_secret_manager_secret.runtime[each.value]
  id       = "projects/${var.project_id}/secrets/heygo-${each.value}"
}

import {
  for_each = toset(["venue-partner-service-key", "venue-partner-inbound-key"])
  to       = google_secret_manager_secret_iam_member.runtime_accessor["api-gateway:${each.value}"]
  id       = "projects/${var.project_id}/secrets/heygo-${each.value} roles/secretmanager.secretAccessor serviceAccount:api-gateway@${var.project_id}.iam.gserviceaccount.com"
}
