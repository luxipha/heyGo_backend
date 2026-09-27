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

variable "deploy_services" {
  description = "Create the Cloud Run services after all externally managed secret versions have been populated."
  type        = bool
  default     = false
}

variable "retain_pull_subscriber_permissions" {
  description = "Retain legacy runtime Pub/Sub pull permissions until authenticated push delivery has been deployed and verified."
  type        = bool
  default     = true

  validation {
    condition     = var.retain_pull_subscriber_permissions || var.deploy_services
    error_message = "retain_pull_subscriber_permissions cannot be false while deploy_services is false. Deploy and verify authenticated push delivery before removing pull permissions."
  }
}

variable "image_tag" {
  description = "Immutable 40-character Git commit SHA published to Artifact Registry. Required when deploy_services is true."
  type        = string
  default     = ""

  validation {
    condition     = var.image_tag == "" || can(regex("^[0-9a-f]{40}$", var.image_tag))
    error_message = "image_tag must be empty or a full lowercase 40-character Git SHA."
  }

  validation {
    condition     = !var.deploy_services || var.image_tag != ""
    error_message = "image_tag is required when deploy_services is true."
  }
}

variable "cloud_sql_tier" {
  description = "Cloud SQL machine tier. db-f1-micro is the low-cost startup default and is not highly available."
  type        = string
  default     = "db-f1-micro"
}

variable "database_name" {
  description = "Application PostgreSQL database name."
  type        = string
  default     = "heygo"
}

variable "database_user" {
  description = "Application PostgreSQL user name."
  type        = string
  default     = "heygo_app"
}

variable "allowed_origins" {
  description = "Comma-separated browser origins accepted by API Gateway. Set this before enabling Cloud Run."
  type        = string
  default     = ""

  validation {
    condition     = !var.deploy_services || trimspace(var.allowed_origins) != ""
    error_message = "allowed_origins must be set when deploy_services is true."
  }
}

variable "app_url" {
  description = "Public HeyGo application URL used for payment redirects. Set this before enabling Cloud Run."
  type        = string
  default     = ""

  validation {
    condition     = !var.deploy_services || can(regex("^https://", var.app_url))
    error_message = "app_url must be an HTTPS URL when deploy_services is true."
  }
}

variable "api_gateway_concurrency" {
  description = "Cloud Run concurrency for API Gateway. Keep 80 until the WebSocket workload is load-tested, then raise toward 400-500."
  type        = number
  default     = 80

  validation {
    condition     = var.api_gateway_concurrency >= 1 && var.api_gateway_concurrency <= 1000
    error_message = "api_gateway_concurrency must be between 1 and 1000."
  }
}

variable "casperid_base_url" {
  description = "CasperID API base URL."
  type        = string
  default     = "https://casperid.com"
}

variable "casperid_jwks_url" {
  description = "CasperID JSON Web Key Set URL."
  type        = string
  default     = "https://casperid.com/.well-known/jwks.json"
}

variable "casperid_issuer" {
  description = "Expected CasperID token issuer."
  type        = string
  default     = "casperid.com"
}

variable "casperid_token_url" {
  description = "CasperID OAuth token endpoint."
  type        = string
  default     = "https://apis.casperid.com/api/oauth/token"
}

variable "casperid_driver_redirect_uri" {
  description = "Driver app OAuth callback URI."
  type        = string
  default     = "com.heygo.driver://oauth/callback"
}

variable "monnify_base_url" {
  description = "Monnify API base URL. Keep the sandbox default until production credentials are approved."
  type        = string
  default     = "https://sandbox.monnify.com"
}
