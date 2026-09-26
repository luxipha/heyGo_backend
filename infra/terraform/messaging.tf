locals {
  pubsub_service_agent = "service-${data.google_project.current.number}@gcp-sa-pubsub.iam.gserviceaccount.com"

  pubsub_topics = toset([
    "trip.event.created",
    "trip.event.driver_assigned",
    "trip.event.arrived",
    "trip.event.started",
    "trip.event.completed",
    "trip.event.cancelled",
    "trip.event.settlement_updated",
    "trip.event.no_show_claim_updated",
    "trip.event.no_drivers_found",
    "trip.event.driver_not_interested",
    "trip.event.expired",
    "trip.event.reassigned",
    "driver.cmd.trip_request",
    "driver.cmd.trip_accept",
    "driver.cmd.trip_decline",
    "driver.cmd.location",
    "driver.cmd.register",
    "driver.event.location_updated",
    "driver.event.command_acknowledged",
    "payment.event.session_created",
    "payment.event.success",
    "payment.event.failed",
    "payment.event.cancelled",
    "payment.cmd.create_session",
    "trip.cmd.arrive",
    "trip.cmd.start",
    "trip.cmd.complete",
    "trip.cmd.cancel",
    "trip.cmd.rate",
  ])

  pubsub_consumers = {
    api-gateway-group = {
      service = "api-gateway"
      topics = toset([
        "trip.event.no_drivers_found",
        "trip.event.driver_assigned",
        "driver.cmd.trip_request",
        "payment.event.session_created",
        "payment.event.success",
        "payment.event.failed",
        "payment.event.cancelled",
        "trip.event.started",
        "trip.event.arrived",
        "trip.event.completed",
        "trip.event.cancelled",
        "trip.event.settlement_updated",
        "trip.event.no_show_claim_updated",
        "trip.event.expired",
        "trip.event.reassigned",
        "driver.event.command_acknowledged",
        "driver.event.location_updated",
      ])
    }
    driver-service-group = {
      service = "driver-service"
      topics = toset([
        "trip.event.created",
        "trip.event.driver_not_interested",
        "driver.cmd.trip_decline",
        "driver.cmd.location",
      ])
    }
    payment-service-group = {
      service = "payment-service"
      topics  = toset(["payment.cmd.create_session"])
    }
    trip-service-group = {
      service = "trip-service"
      topics = toset([
        "driver.cmd.trip_accept",
        "trip.cmd.arrive",
        "trip.cmd.start",
        "trip.cmd.complete",
        "trip.cmd.cancel",
        "trip.cmd.rate",
      ])
    }
  }

  pubsub_subscriptions = merge([
    for group_id, consumer in local.pubsub_consumers : {
      for topic in consumer.topics :
      "${group_id}-${replace(topic, ".", "-")}" => {
        group_id = group_id
        service  = consumer.service
        topic    = topic
      }
    }
  ]...)

}

data "google_project" "current" {
  project_id = var.project_id
}

resource "google_pubsub_topic" "events" {
  for_each = local.pubsub_topics

  project = var.project_id
  name    = each.value

  message_retention_duration = "604800s"

  depends_on = [google_project_service.required]
}

resource "google_pubsub_topic" "dead_letter" {
  for_each = local.pubsub_topics

  project = var.project_id
  name    = "${each.value}.dlq"

  message_retention_duration = "1209600s"

  depends_on = [google_project_service.required]
}

resource "google_project_iam_member" "runtime_pubsub_publisher" {
  for_each = local.runtime_services

  project = var.project_id
  role    = "roles/pubsub.publisher"
  member  = "serviceAccount:${google_service_account.runtime[each.value].email}"
}

resource "google_pubsub_topic_iam_member" "dead_letter_publisher" {
  for_each = local.pubsub_topics

  project = var.project_id
  topic   = google_pubsub_topic.dead_letter[each.value].name
  role    = "roles/pubsub.publisher"
  member  = "serviceAccount:${local.pubsub_service_agent}"
}

resource "google_pubsub_subscription" "events" {
  for_each = local.pubsub_subscriptions

  project = var.project_id
  name    = each.key
  topic   = google_pubsub_topic.events[each.value.topic].id

  ack_deadline_seconds       = 30
  enable_message_ordering    = true
  message_retention_duration = "604800s"
  retain_acked_messages      = false

  dead_letter_policy {
    dead_letter_topic     = google_pubsub_topic.dead_letter[each.value.topic].id
    max_delivery_attempts = 5
  }

  retry_policy {
    minimum_backoff = "10s"
    maximum_backoff = "600s"
  }

  depends_on = [google_pubsub_topic_iam_member.dead_letter_publisher]
}

resource "google_pubsub_subscription_iam_member" "runtime_subscriber" {
  for_each = local.pubsub_subscriptions

  project      = var.project_id
  subscription = google_pubsub_subscription.events[each.key].name
  role         = "roles/pubsub.subscriber"
  member       = "serviceAccount:${google_service_account.runtime[each.value.service].email}"
}

resource "google_pubsub_subscription_iam_member" "dead_letter_forwarder" {
  for_each = local.pubsub_subscriptions

  project      = var.project_id
  subscription = google_pubsub_subscription.events[each.key].name
  role         = "roles/pubsub.subscriber"
  member       = "serviceAccount:${local.pubsub_service_agent}"
}
