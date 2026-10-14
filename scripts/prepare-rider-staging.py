#!/usr/bin/env python3
"""Create local-only credentials; never load the existing .env.local."""
from pathlib import Path
import secrets

root = Path(__file__).resolve().parent.parent
target = root / '.dev/rider-staging.env'
target.parent.mkdir(exist_ok=True)
password = secrets.token_hex(24)
config = f'''ENV=staging
ENVIRONMENT=staging
RIDER_STAGING_STAFF_PASSWORD={secrets.token_urlsafe(32)}
RIDER_STAGING_DB_PASSWORD={password}
DATABASE_URL=postgres://rider_staging:{password}@database:5432/heygo_rider_staging?sslmode=disable
GCP_PROJECT_ID=heygo-rider-staging-local
PUBSUB_EMULATOR_HOST=broker:8085
PUBSUB_DELIVERY_MODE=pull
INTERNAL_SERVICE_TOKEN={secrets.token_hex(32)}
OTP_PEPPER={secrets.token_hex(32)}
HEYGO_ADMIN_SETTINGS_KEY={secrets.token_hex(32)}
MIGRATE_ON_STARTUP=true
ADMIN_COOKIE_SECURE=false
TRIP_SERVICE_URL=trip:9000
DRIVER_SERVICE_URL=driver:9100
RIDE_AGENT_URL=http://agent:8085
RIDE_AGENT_INTERNAL_TOKEN={secrets.token_hex(32)}
RIDE_AGENT_LLM_ENABLED=false
RIDE_AGENT_PLACES_KEY=
SMS_PROVIDER_URL=
SMS_PROVIDER_TOKEN=
'''
with target.open('x') as stream:
    target.chmod(0o600)
    stream.write(config)
print('Created .dev/rider-staging.env with private local-only credentials')
