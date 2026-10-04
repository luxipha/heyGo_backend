#!/bin/sh
set -eu

required="POSTGRES_URL INTERNAL_SERVICE_TOKEN OTP_PEPPER HEYGO_ADMIN_SETTINGS_KEY MONNIFY_API_KEY MONNIFY_SECRET_KEY MONNIFY_CONTRACT_CODE"
for name in $required; do
  eval "value=\${$name:-}"
  if [ -z "$value" ]; then
    echo "Missing required environment variable: $name" >&2
    exit 1
  fi
done

case "$HEYGO_ADMIN_SETTINGS_KEY" in
  *[!0-9a-fA-F]*|'')
    echo "HEYGO_ADMIN_SETTINGS_KEY must contain exactly 64 hexadecimal characters" >&2
    exit 1
    ;;
esac
if [ "${#HEYGO_ADMIN_SETTINGS_KEY}" -ne 64 ]; then
  echo "HEYGO_ADMIN_SETTINGS_KEY must contain exactly 64 hexadecimal characters" >&2
  exit 1
fi

namespace="${K8S_NAMESPACE:-heygo}"
output="${SECRETS_FILE:-infra/kubernetes/dev/secrets.yaml}"
umask 077

{
  kubectl create secret generic postgres -n "$namespace" \
    --from-literal=url="$POSTGRES_URL" \
    --from-literal=internal-service-token="$INTERNAL_SERVICE_TOKEN" \
    --dry-run=client -o yaml
  echo '---'
  kubectl create secret generic phone-auth -n "$namespace" \
    --from-literal=otp-pepper="$OTP_PEPPER" \
    --from-literal=admin-settings-key="$HEYGO_ADMIN_SETTINGS_KEY" \
    --from-literal=sms-provider-token="${SMS_PROVIDER_TOKEN:-}" \
    --dry-run=client -o yaml
  echo '---'
  kubectl create secret generic external-apis -n "$namespace" \
    --from-literal=osrm="${OSRM_API:-http://router.project-osrm.org/route/v1/driving}" \
    --dry-run=client -o yaml
  echo '---'
  kubectl create secret generic moniepoint -n "$namespace" \
    --from-literal=api-key="$MONNIFY_API_KEY" \
    --from-literal=secret-key="$MONNIFY_SECRET_KEY" \
    --from-literal=contract-code="$MONNIFY_CONTRACT_CODE" \
    --dry-run=client -o yaml
} > "$output"

echo "Created $output with mode 600. Review it, then run: kubectl apply -f $output"
