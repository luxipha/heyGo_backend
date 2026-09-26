output "artifact_registry_repository" {
  description = "Regional Docker repository used by the deployment workflow."
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.containers.repository_id}"
}

output "github_deployer_service_account" {
  description = "Service account impersonated by trusted GitHub Actions jobs."
  value       = google_service_account.github_deployer.email
}

output "runtime_service_accounts" {
  description = "Dedicated runtime identity for each future Cloud Run service."
  value       = { for name, account in google_service_account.runtime : name => account.email }
}

output "workload_identity_provider" {
  description = "Full provider name required by google-github-actions/auth."
  value       = google_iam_workload_identity_pool_provider.github.name
}

output "pubsub_topics" {
  description = "Application Pub/Sub topics provisioned for HeyGo events and commands."
  value       = sort(keys(google_pubsub_topic.events))
}

output "pubsub_subscriptions" {
  description = "Per-service pull subscriptions replacing Kafka consumer groups."
  value       = sort(keys(google_pubsub_subscription.events))
}

output "cloud_sql_instance_connection_name" {
  description = "Cloud SQL connection name mounted into Cloud Run services."
  value       = google_sql_database_instance.primary.connection_name
}

output "runtime_secret_ids" {
  description = "Secret Manager containers. CasperID and Monnify secrets require manually supplied versions before deployment."
  value       = { for name, secret in google_secret_manager_secret.runtime : name => secret.secret_id }
}

output "cloud_run_service_urls" {
  description = "Cloud Run service URLs after deploy_services is enabled."
  value = var.deploy_services ? {
    api_gateway     = google_cloud_run_v2_service.api_gateway[0].uri
    driver_service  = google_cloud_run_v2_service.driver[0].uri
    payment_service = google_cloud_run_v2_service.payment[0].uri
    trip_service    = google_cloud_run_v2_service.trip[0].uri
  } : null
}
