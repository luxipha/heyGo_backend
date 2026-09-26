variable "project_id" {
  description = "Google Cloud project ID."
  type        = string
  default     = "heygo-ng"

  validation {
    condition     = var.project_id == "heygo-ng"
    error_message = "This foundation is intentionally scoped to the heygo-ng project."
  }
}

variable "region" {
  description = "Primary Google Cloud region for HeyGo services and artifacts."
  type        = string
  default     = "europe-west1"
}

variable "artifact_repository_id" {
  description = "Artifact Registry repository for deployable container images."
  type        = string
  default     = "heygo"
}

variable "github_repository" {
  description = "GitHub repository allowed to deploy."
  type        = string
  default     = "luxipha/heyGo_backend"
}

variable "github_repository_id" {
  description = "Immutable GitHub repository ID used in the OIDC condition."
  type        = string
  default     = "1369862034"
}

variable "github_owner_id" {
  description = "Immutable GitHub repository owner ID used in the OIDC condition."
  type        = string
  default     = "202601895"
}

variable "github_deploy_branch" {
  description = "Only this protected branch can exchange GitHub OIDC tokens for deployment credentials."
  type        = string
  default     = "main"
}
