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
