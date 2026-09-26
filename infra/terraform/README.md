# HeyGo Google Cloud foundation

This Terraform root provisions the shared deployment foundation in the
`heygo-ng` project:

- required Google Cloud APIs;
- the `europe-west1` Docker Artifact Registry;
- dedicated runtime service accounts for each backend service;
- a least-privilege GitHub deployment service account; and
- keyless GitHub Actions authentication through Workload Identity Federation.

It intentionally does not create Cloud Run services, databases, Kafka, or
application secrets. Those resources depend on the Cloud Run compatibility and
managed-dependency work that follows this foundation.

## Trust boundary

The OIDC provider accepts tokens only when all of these claims match:

- repository ID `1369862034` (`luxipha/heyGo_backend`);
- repository owner ID `202601895` (`luxipha`); and
- branch `refs/heads/main`.

No service-account key is created or stored in GitHub.

## State

Terraform state is stored in the versioned, access-controlled GCS bucket
`heygo-ng-terraform-state` under the `foundation` prefix. The bucket is a
one-time bootstrap resource and is not managed by this root configuration.

## Commands

```sh
terraform -chdir=infra/terraform init
terraform -chdir=infra/terraform fmt -check
terraform -chdir=infra/terraform validate
terraform -chdir=infra/terraform plan -out=foundation.tfplan
terraform -chdir=infra/terraform apply foundation.tfplan
```

Review every plan before applying it. Never commit `terraform.tfvars`, state,
plan files, generated GitHub credentials, or application secrets.

## Default service account hardening

The automatically assigned Editor role has been removed from the unused Compute
Engine default service account. Confirm it remains absent with:

```sh
gcloud projects get-iam-policy heygo-ng \
  --flatten=bindings[].members \
  --filter='bindings.members:392496443029-compute@developer.gserviceaccount.com'
```

The project currently has no Google Cloud organization parent. Google only
allows `roles/orgpolicy.policyAdmin` at the organization level, so this project
cannot yet enforce the
`constraints/iam.automaticIamGrantsForDefaultServiceAccounts` preventive policy.
If the project is moved into an organization, grant the infrastructure operator
that role at the organization and add the constraint before creating any new
default service accounts.

The one-time role removal is intentionally not modeled as an authoritative
Terraform IAM binding because doing so would make this root responsible for
every project member that legitimately holds the Editor role.
