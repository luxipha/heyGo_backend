#!/bin/sh
set -e

export API_URL=$NEXT_PUBLIC_API_URL
export WS_URL=$NEXT_PUBLIC_WS_URL
export STRIPE_PUBLISHABLE_KEY=$NEXT_PUBLIC_STRIPE_PUBLISHABLE_KEY

# Replace env placeholders inside .next static files
for file in $(find .next -type f -name '*.js'); do
  sed -i "s#__NEXT_PUBLIC_API_URL__#${API_URL}#g" $file
  sed -i "s#__NEXT_PUBLIC_WS_URL__#${WS_URL}#g" $file
  sed -i "s#__NEXT_PUBLIC_STRIPE_PUBLISHABLE_KEY__#${STRIPE_PUBLISHABLE_KEY}#g" $file
done

exec npm start