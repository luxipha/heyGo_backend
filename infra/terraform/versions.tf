terraform {
  required_version = ">= 1.8.0, < 2.0.0"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = ">= 7.0.0, < 8.0.0"
    }
  }

  backend "gcs" {
    bucket = "heygo-ng-terraform-state"
    prefix = "foundation"
  }
}
