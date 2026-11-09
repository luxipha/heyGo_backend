CREATE TABLE rider_feature_ideas (
 key TEXT PRIMARY KEY CHECK(key ~ '^[a-z][a-z0-9_]{1,63}$'),
 title TEXT NOT NULL CHECK(length(title) BETWEEN 2 AND 120),
 description TEXT NOT NULL CHECK(length(description) BETWEEN 2 AND 4000),
 tagline TEXT NOT NULL DEFAULT '' CHECK(length(tagline)<=240),
 bullets TEXT[] NOT NULL DEFAULT '{}',
 question TEXT NOT NULL DEFAULT '' CHECK(length(question)<=240),
 interest_tags TEXT[] NOT NULL DEFAULT '{}',
 idea_id BIGINT UNIQUE CHECK(idea_id>0),
 status TEXT NOT NULL DEFAULT 'draft' CHECK(status IN ('draft','published','disabled','live')),
 created_by UUID NOT NULL REFERENCES admin_users(id),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 CHECK(status<>'published' OR idea_id IS NOT NULL)
);
CREATE TABLE rider_feature_subscriptions (
 feature_key TEXT NOT NULL REFERENCES rider_feature_ideas(key),
 rider_id UUID NOT NULL REFERENCES users(id),
 interest_tag TEXT NOT NULL DEFAULT '',
 subscribed BOOLEAN NOT NULL DEFAULT FALSE,
 status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','confirmed')),
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 confirmed_at TIMESTAMPTZ,
 PRIMARY KEY(feature_key,rider_id),
 CHECK((status='confirmed')=(confirmed_at IS NOT NULL))
);
