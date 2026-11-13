terraform {
  required_version = ">= 1.8.0, < 2.0.0"
  required_providers {
    google = { source = "hashicorp/google", version = ">= 7.0.0, < 8.0.0" }
  }
  backend "gcs" {
    bucket = "heygo-ng-terraform-state"
    prefix = "geo"
  }
}
provider "google" {
  project               = var.project_id
  billing_project       = var.project_id
  user_project_override = true
}
variable "project_id" { type = string }
variable "android_sha1" {
  type        = string
  description = "Certificate SHA-1 used to sign this testing build. Use a separate release key for Play signing."
  validation {
    condition     = can(regex("^([0-9A-Fa-f]{2}:){19}[0-9A-Fa-f]{2}$", var.android_sha1))
    error_message = "Supply the exact signing-certificate SHA-1."
  }
}
variable "billing_account" { type = string }
variable "monthly_budget_usd" {
  type = number
  validation {
    condition     = var.monthly_budget_usd > 0 && floor(var.monthly_budget_usd) == var.monthly_budget_usd
    error_message = "Choose a positive whole-dollar alert amount."
  }
}
variable "billing_service_ids" {
  type        = set(string)
  description = "Cloud Billing catalog IDs for Google Maps Platform; obtain from catalog, not Service Usage names."
  validation {
    condition     = length(var.billing_service_ids) > 0 && alltrue([for id in var.billing_service_ids : can(regex("^services/", id))])
    error_message = "Supply verified Maps billing service IDs so the alert is scoped to geo."
  }
}
locals {
  services = toset(["apikeys.googleapis.com", "billingbudgets.googleapis.com", "maps-android-backend.googleapis.com", "maps-ios-backend.googleapis.com", "navigationsdk.googleapis.com", "places.googleapis.com"])
  android  = { driver = "ng.heygo.partner", rider = "ng.heygo.rider" }
  ios      = { driver = "ng.heygo.partner", rider = "ng.heygo.rider" }
}
resource "google_project_service" "geo" {
  for_each           = local.services
  project            = var.project_id
  service            = each.value
  disable_on_destroy = false
}
resource "google_apikeys_key" "android" {
  for_each     = local.android
  project      = var.project_id
  name         = "heygo-${each.key}-android-geo-test"
  display_name = "HeyGo ${each.key} Android geo testing"
  restrictions {
    android_key_restrictions {
      allowed_applications {
        package_name     = each.value
        sha1_fingerprint = lower(replace(var.android_sha1, ":", ""))
      }
    }
    api_targets { service = "maps-android-backend.googleapis.com" }
    dynamic "api_targets" {
      for_each = each.key == "driver" ? [1] : []
      content { service = "navigationsdk.googleapis.com" }
    }
  }
  depends_on = [google_project_service.geo]
}
resource "google_apikeys_key" "ios" {
  for_each     = local.ios
  project      = var.project_id
  name         = "heygo-${each.key}-ios-geo-test"
  display_name = "HeyGo ${each.key} iOS geo testing"
  restrictions {
    ios_key_restrictions { allowed_bundle_ids = [each.value] }
    api_targets { service = "maps-ios-backend.googleapis.com" }
    dynamic "api_targets" {
      for_each = each.key == "driver" ? [1] : []
      content { service = "navigationsdk.googleapis.com" }
    }
  }
  depends_on = [google_project_service.geo]
}
resource "google_apikeys_key" "places" {
  project      = var.project_id
  name         = "heygo-server-places"
  display_name = "HeyGo server Places only"
  restrictions {
    api_targets { service = "places.googleapis.com" }
  }
  depends_on = [google_project_service.geo]
}
data "google_project" "current" { project_id = var.project_id }
resource "google_billing_budget" "geo" {
  billing_account = var.billing_account
  display_name    = "HeyGo Google Maps Platform"
  depends_on      = [google_project_service.geo]
  budget_filter {
    projects = ["projects/${data.google_project.current.number}"]
    services = var.billing_service_ids
  }
  amount {
    specified_amount {
      currency_code = "USD"
      units         = tostring(var.monthly_budget_usd)
    }
  }
  threshold_rules { threshold_percent = 0.5 }
  threshold_rules { threshold_percent = 0.9 }
  threshold_rules { threshold_percent = 1.0 }
}
# No key-string outputs: retrieve directly into ignored native config / Secret Manager.
# Budgets alert; they do not stop spending. Quotas need deliberate per-API tuning.
